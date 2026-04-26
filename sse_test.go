package httptape

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// SSEEvent type tests
// ---------------------------------------------------------------------------

func TestSSEEvent_JSONRoundTrip(t *testing.T) {
	tests := []struct {
		name string
		ev   SSEEvent
	}{
		{
			name: "full event",
			ev:   SSEEvent{OffsetMS: 100, Type: "update", Data: `{"key":"val"}`, ID: "42", Retry: 3000},
		},
		{
			name: "data only",
			ev:   SSEEvent{OffsetMS: 0, Data: "hello"},
		},
		{
			name: "empty data",
			ev:   SSEEvent{OffsetMS: 50, Data: ""},
		},
		{
			name: "multiline data",
			ev:   SSEEvent{OffsetMS: 200, Data: "line1\nline2\nline3"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			b, err := json.Marshal(tt.ev)
			if err != nil {
				t.Fatalf("Marshal: %v", err)
			}
			var got SSEEvent
			if err := json.Unmarshal(b, &got); err != nil {
				t.Fatalf("Unmarshal: %v", err)
			}
			if got != tt.ev {
				t.Errorf("round-trip mismatch: got %+v, want %+v", got, tt.ev)
			}
		})
	}
}

func TestSSEEvent_OmitEmpty(t *testing.T) {
	ev := SSEEvent{OffsetMS: 0, Data: "hello"}
	b, _ := json.Marshal(ev)
	s := string(b)
	// Type, ID, Retry should be omitted.
	if strings.Contains(s, "type") {
		t.Errorf("expected type to be omitted, got %s", s)
	}
	if strings.Contains(s, "\"id\"") {
		t.Errorf("expected id to be omitted, got %s", s)
	}
	if strings.Contains(s, "retry") {
		t.Errorf("expected retry to be omitted, got %s", s)
	}
	// Data should always be present (even if empty).
	if !strings.Contains(s, "\"data\"") {
		t.Errorf("expected data to be present, got %s", s)
	}
}

// ---------------------------------------------------------------------------
// isSSEContentType tests
// ---------------------------------------------------------------------------

func TestIsSSEContentType(t *testing.T) {
	tests := []struct {
		ct   string
		want bool
	}{
		{"text/event-stream", true},
		{"text/event-stream; charset=utf-8", true},
		{"TEXT/EVENT-STREAM", true},
		{"Text/Event-Stream", true},
		{"application/json", false},
		{"text/plain", false},
		{"", false},
		{"text/event-stream-extra", false},
	}
	for _, tt := range tests {
		t.Run(tt.ct, func(t *testing.T) {
			if got := isSSEContentType(tt.ct); got != tt.want {
				t.Errorf("isSSEContentType(%q) = %v, want %v", tt.ct, got, tt.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// parseSSEStream tests
// ---------------------------------------------------------------------------

func TestParseSSEStream(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  []SSEEvent
	}{
		{
			name:  "single simple event",
			input: "data: hello\n\n",
			want: []SSEEvent{
				{Data: "hello"},
			},
		},
		{
			name:  "event with type",
			input: "event: update\ndata: payload\n\n",
			want: []SSEEvent{
				{Type: "update", Data: "payload"},
			},
		},
		{
			name:  "event with id",
			input: "id: 42\ndata: payload\n\n",
			want: []SSEEvent{
				{ID: "42", Data: "payload"},
			},
		},
		{
			name:  "event with retry",
			input: "retry: 3000\ndata: payload\n\n",
			want: []SSEEvent{
				{Retry: 3000, Data: "payload"},
			},
		},
		{
			name:  "multiline data",
			input: "data: line1\ndata: line2\ndata: line3\n\n",
			want: []SSEEvent{
				{Data: "line1\nline2\nline3"},
			},
		},
		{
			name:  "multiple events",
			input: "data: first\n\ndata: second\n\n",
			want: []SSEEvent{
				{Data: "first"},
				{Data: "second"},
			},
		},
		{
			name:  "comment lines ignored",
			input: ": this is a comment\ndata: hello\n\n",
			want: []SSEEvent{
				{Data: "hello"},
			},
		},
		{
			name:  "unknown field ignored",
			input: "foo: bar\ndata: hello\n\n",
			want: []SSEEvent{
				{Data: "hello"},
			},
		},
		{
			name:  "line without colon treated as unknown field",
			input: "novalue\ndata: hello\n\n",
			want: []SSEEvent{
				{Data: "hello"},
			},
		},
		{
			name:  "blank lines between events",
			input: "\n\ndata: hello\n\n\n\n",
			want: []SSEEvent{
				{Data: "hello"},
			},
		},
		{
			name:  "partial event at EOF discarded",
			input: "data: complete\n\ndata: incomplete",
			want: []SSEEvent{
				{Data: "complete"},
			},
		},
		{
			name:  "data with no space after colon",
			input: "data:nospace\n\n",
			want: []SSEEvent{
				{Data: "nospace"},
			},
		},
		{
			name:  "empty data line",
			input: "data:\n\n",
			want: []SSEEvent{
				{Data: ""},
			},
		},
		{
			name:  "data with only space after colon",
			input: "data: \n\n",
			want: []SSEEvent{
				{Data: ""},
			},
		},
		{
			name:  "full event with all fields",
			input: "event: msg\nid: 1\nretry: 5000\ndata: payload\n\n",
			want: []SSEEvent{
				{Type: "msg", ID: "1", Retry: 5000, Data: "payload"},
			},
		},
		{
			name:  "negative retry ignored",
			input: "retry: -1\ndata: hello\n\n",
			want: []SSEEvent{
				{Data: "hello"},
			},
		},
		{
			name:  "non-numeric retry ignored",
			input: "retry: abc\ndata: hello\n\n",
			want: []SSEEvent{
				{Data: "hello"},
			},
		},
		{
			name:  "id with null character ignored",
			input: "id: ab\x00cd\ndata: hello\n\n",
			want: []SSEEvent{
				{Data: "hello"},
			},
		},
		{
			name:  "empty input",
			input: "",
			want:  nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			start := time.Now()
			var events []SSEEvent
			err := parseSSEStream(strings.NewReader(tt.input), start, func(ev SSEEvent) {
				// Zero out OffsetMS for comparison (timing-dependent).
				ev.OffsetMS = 0
				events = append(events, ev)
			})
			if err != nil {
				t.Fatalf("parseSSEStream error: %v", err)
			}
			if len(events) != len(tt.want) {
				t.Fatalf("got %d events, want %d", len(events), len(tt.want))
			}
			for i := range events {
				if events[i] != tt.want[i] {
					t.Errorf("event[%d] = %+v, want %+v", i, events[i], tt.want[i])
				}
			}
		})
	}
}

func TestParseSSEStream_OffsetMS(t *testing.T) {
	// Verify that OffsetMS is computed from startTime.
	start := time.Now().Add(-100 * time.Millisecond)
	var events []SSEEvent
	input := "data: hello\n\n"
	err := parseSSEStream(strings.NewReader(input), start, func(ev SSEEvent) {
		events = append(events, ev)
	})
	if err != nil {
		t.Fatalf("error: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("got %d events, want 1", len(events))
	}
	// OffsetMS should be >= 100ms since start was 100ms ago.
	if events[0].OffsetMS < 100 {
		t.Errorf("OffsetMS = %d, expected >= 100", events[0].OffsetMS)
	}
}

func TestParseSSEStream_ReaderError(t *testing.T) {
	errReader := &errReaderAt{after: 5, err: errors.New("read failed")}
	var events []SSEEvent
	err := parseSSEStream(errReader, time.Now(), func(ev SSEEvent) {
		events = append(events, ev)
	})
	if err == nil {
		t.Fatal("expected error from reader")
	}
}

// errReaderAt returns an error after reading `after` bytes.
type errReaderAt struct {
	after int
	read  int
	err   error
}

func (r *errReaderAt) Read(p []byte) (int, error) {
	remaining := r.after - r.read
	if remaining <= 0 {
		return 0, r.err
	}
	n := len(p)
	if n > remaining {
		n = remaining
	}
	for i := 0; i < n; i++ {
		p[i] = 'x'
	}
	r.read += n
	return n, nil
}

// ---------------------------------------------------------------------------
// writeSSEEvent tests
// ---------------------------------------------------------------------------

func TestWriteSSEEvent(t *testing.T) {
	tests := []struct {
		name string
		ev   SSEEvent
		want string
	}{
		{
			name: "simple data",
			ev:   SSEEvent{Data: "hello"},
			want: "data: hello\n\n",
		},
		{
			name: "with type",
			ev:   SSEEvent{Type: "update", Data: "payload"},
			want: "event: update\ndata: payload\n\n",
		},
		{
			name: "with id",
			ev:   SSEEvent{ID: "42", Data: "payload"},
			want: "id: 42\ndata: payload\n\n",
		},
		{
			name: "with retry",
			ev:   SSEEvent{Retry: 3000, Data: "payload"},
			want: "retry: 3000\ndata: payload\n\n",
		},
		{
			name: "multiline data",
			ev:   SSEEvent{Data: "line1\nline2"},
			want: "data: line1\ndata: line2\n\n",
		},
		{
			name: "all fields",
			ev:   SSEEvent{Type: "msg", ID: "1", Retry: 5000, Data: "hello"},
			want: "event: msg\nid: 1\nretry: 5000\ndata: hello\n\n",
		},
		{
			name: "empty data",
			ev:   SSEEvent{Data: ""},
			want: "data: \n\n",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var buf bytes.Buffer
			err := writeSSEEvent(&buf, tt.ev)
			if err != nil {
				t.Fatalf("writeSSEEvent error: %v", err)
			}
			if got := buf.String(); got != tt.want {
				t.Errorf("got %q, want %q", got, tt.want)
			}
		})
	}
}

func TestWriteSSEEvent_WriteError(t *testing.T) {
	ew := &errWriter{err: errors.New("write failed")}
	err := writeSSEEvent(ew, SSEEvent{Data: "hello"})
	if err == nil {
		t.Fatal("expected error")
	}
}

type errWriter struct {
	err error
}

func (w *errWriter) Write(p []byte) (int, error) {
	return 0, w.err
}

// ---------------------------------------------------------------------------
// SSETimingMode tests
// ---------------------------------------------------------------------------

func TestSSETimingRealtime(t *testing.T) {
	events := []SSEEvent{
		{OffsetMS: 0},
		{OffsetMS: 100},
		{OffsetMS: 300},
	}
	mode := SSETimingRealtime()

	if d := mode.delay(events, 0); d != 0 {
		t.Errorf("delay(0) = %v, want 0", d)
	}
	if d := mode.delay(events, 1); d != 100*time.Millisecond {
		t.Errorf("delay(1) = %v, want 100ms", d)
	}
	if d := mode.delay(events, 2); d != 200*time.Millisecond {
		t.Errorf("delay(2) = %v, want 200ms", d)
	}
}

func TestSSETimingAccelerated(t *testing.T) {
	events := []SSEEvent{
		{OffsetMS: 0},
		{OffsetMS: 200},
		{OffsetMS: 600},
	}
	mode := SSETimingAccelerated(2.0)

	if d := mode.delay(events, 0); d != 0 {
		t.Errorf("delay(0) = %v, want 0", d)
	}
	if d := mode.delay(events, 1); d != 100*time.Millisecond {
		t.Errorf("delay(1) = %v, want 100ms", d)
	}
	if d := mode.delay(events, 2); d != 200*time.Millisecond {
		t.Errorf("delay(2) = %v, want 200ms", d)
	}
}

func TestSSETimingAccelerated_PanicOnZero(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Error("expected panic for factor 0")
		}
	}()
	SSETimingAccelerated(0)
}

func TestSSETimingAccelerated_PanicOnNegative(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Error("expected panic for negative factor")
		}
	}()
	SSETimingAccelerated(-1.0)
}

func TestSSETimingInstant(t *testing.T) {
	events := []SSEEvent{
		{OffsetMS: 0},
		{OffsetMS: 1000},
		{OffsetMS: 5000},
	}
	mode := SSETimingInstant()

	for i := range events {
		if d := mode.delay(events, i); d != 0 {
			t.Errorf("delay(%d) = %v, want 0", i, d)
		}
	}
}

func TestSSETimingNonDecreasingOffset(t *testing.T) {
	// Events with non-decreasing (but equal) OffsetMS.
	events := []SSEEvent{
		{OffsetMS: 100},
		{OffsetMS: 100},
		{OffsetMS: 200},
	}
	mode := SSETimingRealtime()
	if d := mode.delay(events, 1); d != 0 {
		t.Errorf("delay(1) = %v, want 0 for equal offsets", d)
	}
}

// ---------------------------------------------------------------------------
// sseRecordingReader tests
// ---------------------------------------------------------------------------

func TestSSERecordingReader_BasicParsing(t *testing.T) {
	input := "data: event1\n\ndata: event2\n\n"
	upstream := io.NopCloser(strings.NewReader(input))

	var mu sync.Mutex
	var events []SSEEvent
	var doneErr error
	doneCh := make(chan struct{})

	reader := newSSERecordingReader(upstream, time.Now(), func(ev SSEEvent) {
		mu.Lock()
		events = append(events, ev)
		mu.Unlock()
	}, func(err error) {
		doneErr = err
		close(doneCh)
	})

	// Read all bytes through the reader.
	buf, err := io.ReadAll(reader)
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	reader.Close()

	// Wait for parser to complete.
	<-doneCh

	// Original bytes should pass through unchanged.
	if string(buf) != input {
		t.Errorf("passthrough bytes = %q, want %q", string(buf), input)
	}

	// Parser should find 2 events.
	mu.Lock()
	defer mu.Unlock()
	if len(events) != 2 {
		t.Fatalf("got %d events, want 2", len(events))
	}
	if events[0].Data != "event1" {
		t.Errorf("events[0].Data = %q, want %q", events[0].Data, "event1")
	}
	if events[1].Data != "event2" {
		t.Errorf("events[1].Data = %q, want %q", events[1].Data, "event2")
	}
	if doneErr != nil {
		t.Errorf("onDone error = %v, want nil", doneErr)
	}
}

func TestSSERecordingReader_UpstreamDisconnect(t *testing.T) {
	// Simulate upstream sending one complete event then error.
	input := "data: complete\n\n"
	pr, pw := io.Pipe()

	go func() {
		pw.Write([]byte(input))
		pw.CloseWithError(errors.New("upstream disconnected"))
	}()

	var mu sync.Mutex
	var events []SSEEvent
	var doneErr error
	doneCh := make(chan struct{})

	reader := newSSERecordingReader(pr, time.Now(), func(ev SSEEvent) {
		mu.Lock()
		events = append(events, ev)
		mu.Unlock()
	}, func(err error) {
		doneErr = err
		close(doneCh)
	})

	// The read should eventually error.
	_, _ = io.ReadAll(reader)
	reader.Close()

	<-doneCh

	mu.Lock()
	defer mu.Unlock()
	if len(events) != 1 {
		t.Fatalf("got %d events, want 1 (the complete one)", len(events))
	}
	if events[0].Data != "complete" {
		t.Errorf("events[0].Data = %q, want %q", events[0].Data, "complete")
	}
	// doneErr should carry the upstream error through the pipe.
	if doneErr == nil {
		t.Error("expected error from onDone, got nil")
	}
}

func TestSSERecordingReader_EmptyStream(t *testing.T) {
	upstream := io.NopCloser(strings.NewReader(""))

	var events []SSEEvent
	doneCh := make(chan struct{})

	reader := newSSERecordingReader(upstream, time.Now(), func(ev SSEEvent) {
		events = append(events, ev)
	}, func(err error) {
		close(doneCh)
	})

	buf, _ := io.ReadAll(reader)
	reader.Close()
	<-doneCh

	if len(buf) != 0 {
		t.Errorf("expected empty bytes, got %q", string(buf))
	}
	if len(events) != 0 {
		t.Errorf("expected 0 events, got %d", len(events))
	}
}

func TestSSERecordingReader_CloseIdempotent(t *testing.T) {
	upstream := io.NopCloser(strings.NewReader("data: hello\n\n"))
	doneCh := make(chan struct{})

	reader := newSSERecordingReader(upstream, time.Now(), func(ev SSEEvent) {}, func(err error) {
		close(doneCh)
	})

	io.ReadAll(reader)
	reader.Close()
	<-doneCh

	// Second close should not panic.
	reader.Close()
}

// ---------------------------------------------------------------------------
// replaySSEEvents tests
// ---------------------------------------------------------------------------

func TestReplaySSEEvents_Instant(t *testing.T) {
	events := []SSEEvent{
		{OffsetMS: 0, Data: "first"},
		{OffsetMS: 100, Data: "second"},
		{OffsetMS: 200, Data: "third"},
	}

	rec := httptest.NewRecorder()
	var w http.ResponseWriter = rec
	flusher, ok := w.(http.Flusher)
	if !ok {
		t.Fatal("httptest.ResponseRecorder does not implement Flusher")
	}

	start := time.Now()
	err := replaySSEEvents(context.Background(), rec, flusher, events, SSETimingInstant())
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("replaySSEEvents error: %v", err)
	}

	// Instant should complete quickly (within 100ms for any number of events).
	if elapsed > 100*time.Millisecond {
		t.Errorf("instant replay took %v, expected < 100ms", elapsed)
	}

	body := rec.Body.String()
	if !strings.Contains(body, "data: first") {
		t.Error("missing 'data: first' in output")
	}
	if !strings.Contains(body, "data: second") {
		t.Error("missing 'data: second' in output")
	}
	if !strings.Contains(body, "data: third") {
		t.Error("missing 'data: third' in output")
	}
}

func TestReplaySSEEvents_ContextCancellation(t *testing.T) {
	events := []SSEEvent{
		{OffsetMS: 0, Data: "first"},
		{OffsetMS: 10000, Data: "second"}, // 10s delay
	}

	rec := httptest.NewRecorder()
	var w http.ResponseWriter = rec
	flusher, ok := w.(http.Flusher)
	if !ok {
		t.Fatal("httptest.ResponseRecorder does not implement Flusher")
	}

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()

	start := time.Now()
	err := replaySSEEvents(ctx, rec, flusher, events, SSETimingRealtime())
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("expected context cancellation error")
	}

	// Should cancel well before 10s.
	if elapsed > 500*time.Millisecond {
		t.Errorf("context cancellation took %v, expected < 500ms", elapsed)
	}
}

func TestReplaySSEEvents_Realtime(t *testing.T) {
	events := []SSEEvent{
		{OffsetMS: 0, Data: "a"},
		{OffsetMS: 100, Data: "b"},
	}

	rec := httptest.NewRecorder()
	flusher := http.ResponseWriter(rec).(http.Flusher)

	start := time.Now()
	err := replaySSEEvents(context.Background(), rec, flusher, events, SSETimingRealtime())
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("error: %v", err)
	}

	// Should take ~100ms for the gap between events.
	assertTimingInRange(t, elapsed, 100*time.Millisecond)
}

func TestReplaySSEEvents_Accelerated(t *testing.T) {
	events := []SSEEvent{
		{OffsetMS: 0, Data: "a"},
		{OffsetMS: 200, Data: "b"},
	}

	rec := httptest.NewRecorder()
	flusher := http.ResponseWriter(rec).(http.Flusher)

	start := time.Now()
	err := replaySSEEvents(context.Background(), rec, flusher, events, SSETimingAccelerated(2.0))
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("error: %v", err)
	}

	// With 2x acceleration, 200ms gap should become ~100ms.
	assertTimingInRange(t, elapsed, 100*time.Millisecond)
}

// assertTimingInRange checks that elapsed is within an acceptable range of
// expected, following ADR-35's tolerance spec: +/- 50ms or +/- 20% of
// expected, whichever is larger.
func assertTimingInRange(t *testing.T, elapsed, expected time.Duration) {
	t.Helper()
	tolerance := 50 * time.Millisecond
	pctTolerance := time.Duration(float64(expected) * 0.20)
	if pctTolerance > tolerance {
		tolerance = pctTolerance
	}
	low := expected - tolerance
	if low < 0 {
		low = 0
	}
	high := expected + tolerance
	if elapsed < low || elapsed > high {
		t.Errorf("elapsed %v not in range [%v, %v] (expected %v)", elapsed, low, high, expected)
	}
}

// ---------------------------------------------------------------------------
// RecordedResp.IsSSE tests
// ---------------------------------------------------------------------------

func TestRecordedResp_IsSSE(t *testing.T) {
	tests := []struct {
		name string
		resp RecordedResp
		want bool
	}{
		{"nil events", RecordedResp{SSEEvents: nil}, false},
		{"empty events", RecordedResp{SSEEvents: []SSEEvent{}}, false},
		{"with events", RecordedResp{SSEEvents: []SSEEvent{{Data: "hi"}}}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.resp.IsSSE(); got != tt.want {
				t.Errorf("IsSSE() = %v, want %v", got, tt.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Schema backward compatibility
// ---------------------------------------------------------------------------

func TestSSEEvent_BackwardCompat_OldTape(t *testing.T) {
	// A tape JSON from before SSE support has no sse_events field.
	oldJSON := `{
		"status_code": 200,
		"headers": {},
		"body": "aGVsbG8="
	}`
	var resp RecordedResp
	if err := json.Unmarshal([]byte(oldJSON), &resp); err != nil {
		t.Fatalf("Unmarshal old tape: %v", err)
	}
	if resp.IsSSE() {
		t.Error("old tape without sse_events should not be treated as SSE")
	}
	if resp.StatusCode != 200 {
		t.Errorf("StatusCode = %d, want 200", resp.StatusCode)
	}
}

// ---------------------------------------------------------------------------
// Recorder SSE recording integration
// ---------------------------------------------------------------------------

func TestRecorder_SSERecording(t *testing.T) {
	// Upstream that serves an SSE stream.
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		flusher, ok := w.(http.Flusher)
		if !ok {
			http.Error(w, "no flusher", 500)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.WriteHeader(200)
		flusher.Flush()

		fmt.Fprint(w, "data: event1\n\n")
		flusher.Flush()
		time.Sleep(50 * time.Millisecond)
		fmt.Fprint(w, "event: custom\ndata: event2\n\n")
		flusher.Flush()
	}))
	defer upstream.Close()

	store := NewMemoryStore()
	rec := NewRecorder(store, WithAsync(false))
	client := &http.Client{Transport: rec}

	resp, err := client.Get(upstream.URL + "/stream")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}

	// Read the response body (this drives the SSE recording).
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	resp.Body.Close()

	// The caller should have received the raw SSE bytes.
	bodyStr := string(body)
	if !strings.Contains(bodyStr, "data: event1") {
		t.Error("caller should see raw SSE bytes: missing 'data: event1'")
	}
	if !strings.Contains(bodyStr, "data: event2") {
		t.Error("caller should see raw SSE bytes: missing 'data: event2'")
	}

	// Check the stored tape.
	tapes, err := store.List(context.Background(), Filter{})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(tapes) != 1 {
		t.Fatalf("got %d tapes, want 1", len(tapes))
	}
	tape := tapes[0]

	if !tape.Response.IsSSE() {
		t.Fatal("stored tape should be SSE")
	}
	if tape.Response.Body != nil {
		t.Error("SSE tape should have nil Body")
	}
	if len(tape.Response.SSEEvents) != 2 {
		t.Fatalf("got %d events, want 2", len(tape.Response.SSEEvents))
	}
	if tape.Response.SSEEvents[0].Data != "event1" {
		t.Errorf("event[0].Data = %q, want %q", tape.Response.SSEEvents[0].Data, "event1")
	}
	if tape.Response.SSEEvents[1].Data != "event2" {
		t.Errorf("event[1].Data = %q, want %q", tape.Response.SSEEvents[1].Data, "event2")
	}
	if tape.Response.SSEEvents[1].Type != "custom" {
		t.Errorf("event[1].Type = %q, want %q", tape.Response.SSEEvents[1].Type, "custom")
	}

	// Verify timing: second event should be ~50ms after first.
	gap := tape.Response.SSEEvents[1].OffsetMS - tape.Response.SSEEvents[0].OffsetMS
	if gap < 30 || gap > 200 {
		t.Errorf("inter-event gap = %dms, expected ~50ms", gap)
	}
}

func TestRecorder_SSERecordingDisabled(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(200)
		fmt.Fprint(w, "data: hello\n\n")
	}))
	defer upstream.Close()

	store := NewMemoryStore()
	rec := NewRecorder(store, WithAsync(false), WithSSERecording(false))
	client := &http.Client{Transport: rec}

	resp, err := client.Get(upstream.URL + "/stream")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	io.ReadAll(resp.Body)
	resp.Body.Close()

	tapes, _ := store.List(context.Background(), Filter{})
	if len(tapes) != 1 {
		t.Fatalf("got %d tapes, want 1", len(tapes))
	}
	// With SSE recording disabled, it should be a regular tape with Body.
	if tapes[0].Response.IsSSE() {
		t.Error("with SSE recording disabled, tape should not be SSE")
	}
	if tapes[0].Response.Body == nil {
		t.Error("with SSE recording disabled, tape should have Body")
	}
}

// ---------------------------------------------------------------------------
// Server SSE replay integration
// ---------------------------------------------------------------------------

func TestServer_SSEReplay_Instant(t *testing.T) {
	store := NewMemoryStore()

	// Create an SSE tape manually.
	tape := NewTape("", RecordedReq{
		Method:  "GET",
		URL:     "http://example.com/stream",
		Headers: http.Header{},
	}, RecordedResp{
		StatusCode: 200,
		Headers:    http.Header{"Content-Type": {"text/event-stream"}},
		SSEEvents: []SSEEvent{
			{OffsetMS: 0, Data: "first"},
			{OffsetMS: 100, Data: "second"},
			{OffsetMS: 200, Type: "custom", Data: "third", ID: "3"},
		},
	})
	store.Save(context.Background(), tape)

	srv := NewServer(store, WithSSETiming(SSETimingInstant()))
	ts := httptest.NewServer(srv)
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/stream")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		t.Errorf("status = %d, want 200", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "text/event-stream" {
		t.Errorf("Content-Type = %q, want %q", ct, "text/event-stream")
	}

	// Parse the SSE events from the response.
	var received []SSEEvent
	err = parseSSEStream(resp.Body, time.Now(), func(ev SSEEvent) {
		ev.OffsetMS = 0 // normalize
		received = append(received, ev)
	})
	if err != nil {
		t.Fatalf("parseSSEStream: %v", err)
	}

	if len(received) != 3 {
		t.Fatalf("got %d events, want 3", len(received))
	}
	if received[0].Data != "first" {
		t.Errorf("event[0].Data = %q, want %q", received[0].Data, "first")
	}
	if received[1].Data != "second" {
		t.Errorf("event[1].Data = %q, want %q", received[1].Data, "second")
	}
	if received[2].Data != "third" || received[2].Type != "custom" || received[2].ID != "3" {
		t.Errorf("event[2] = %+v, want Type=custom Data=third ID=3", received[2])
	}
}

func TestServer_SSEReplay_WithHeaders(t *testing.T) {
	store := NewMemoryStore()

	tape := NewTape("", RecordedReq{
		Method:  "GET",
		URL:     "http://example.com/stream",
		Headers: http.Header{},
	}, RecordedResp{
		StatusCode: 200,
		Headers:    http.Header{"X-Custom": {"original"}},
		SSEEvents:  []SSEEvent{{Data: "hi"}},
	})
	store.Save(context.Background(), tape)

	srv := NewServer(store,
		WithSSETiming(SSETimingInstant()),
		WithReplayHeaders("X-Override", "injected"),
	)
	ts := httptest.NewServer(srv)
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/stream")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	io.ReadAll(resp.Body)
	resp.Body.Close()

	// Check SSE-required headers.
	if ct := resp.Header.Get("Content-Type"); ct != "text/event-stream" {
		t.Errorf("Content-Type = %q, want text/event-stream", ct)
	}
	if cc := resp.Header.Get("Cache-Control"); cc != "no-cache" {
		t.Errorf("Cache-Control = %q, want no-cache", cc)
	}
	// Check custom header.
	if v := resp.Header.Get("X-Custom"); v != "original" {
		t.Errorf("X-Custom = %q, want original", v)
	}
	// Check replay header override.
	if v := resp.Header.Get("X-Override"); v != "injected" {
		t.Errorf("X-Override = %q, want injected", v)
	}
}

func TestServer_NonSSETapeUnchanged(t *testing.T) {
	// Verify that non-SSE tapes are still served as before.
	store := NewMemoryStore()

	tape := NewTape("", RecordedReq{
		Method:  "GET",
		URL:     "http://example.com/api",
		Headers: http.Header{},
	}, RecordedResp{
		StatusCode: 200,
		Headers:    http.Header{"Content-Type": {"application/json"}},
		Body:       []byte(`{"ok":true}`),
	})
	store.Save(context.Background(), tape)

	srv := NewServer(store, WithSSETiming(SSETimingInstant()))
	ts := httptest.NewServer(srv)
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/api")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()

	if resp.StatusCode != 200 {
		t.Errorf("status = %d, want 200", resp.StatusCode)
	}
	if string(body) != `{"ok":true}` {
		t.Errorf("body = %q, want %q", string(body), `{"ok":true}`)
	}
}

// ---------------------------------------------------------------------------
// Proxy SSE tests
// ---------------------------------------------------------------------------

func TestProxy_SSE_SuccessPath(t *testing.T) {
	// Upstream serves SSE.
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		flusher, ok := w.(http.Flusher)
		if !ok {
			http.Error(w, "no flusher", 500)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(200)
		flusher.Flush()
		fmt.Fprint(w, "data: hello\n\n")
		flusher.Flush()
		fmt.Fprint(w, "data: world\n\n")
		flusher.Flush()
	}))
	defer upstream.Close()

	l1 := NewMemoryStore()
	l2 := NewMemoryStore()
	proxy := NewProxy(l1, l2, WithProxyTransport(upstream.Client().Transport))

	req, _ := http.NewRequest("GET", upstream.URL+"/stream", nil)
	resp, err := proxy.RoundTrip(req)
	if err != nil {
		t.Fatalf("RoundTrip: %v", err)
	}

	// Read the body to trigger SSE recording completion.
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()

	if !strings.Contains(string(body), "data: hello") {
		t.Error("caller should see raw SSE: missing 'data: hello'")
	}
	if !strings.Contains(string(body), "data: world") {
		t.Error("caller should see raw SSE: missing 'data: world'")
	}

	// Give the onDone a moment to save (it runs in the parser goroutine).
	time.Sleep(50 * time.Millisecond)

	// L1 and L2 should each have one SSE tape.
	l1Tapes, _ := l1.List(context.Background(), Filter{})
	if len(l1Tapes) != 1 {
		t.Fatalf("L1 has %d tapes, want 1", len(l1Tapes))
	}
	if !l1Tapes[0].Response.IsSSE() {
		t.Error("L1 tape should be SSE")
	}
	if len(l1Tapes[0].Response.SSEEvents) != 2 {
		t.Errorf("L1 tape has %d events, want 2", len(l1Tapes[0].Response.SSEEvents))
	}

	l2Tapes, _ := l2.List(context.Background(), Filter{})
	if len(l2Tapes) != 1 {
		t.Fatalf("L2 has %d tapes, want 1", len(l2Tapes))
	}
	if !l2Tapes[0].Response.IsSSE() {
		t.Error("L2 tape should be SSE")
	}
}

func TestProxy_SSE_L2Fallback(t *testing.T) {
	l1 := NewMemoryStore()
	l2 := NewMemoryStore()

	// Pre-populate L2 with an SSE tape.
	tape := NewTape("", RecordedReq{
		Method:  "GET",
		URL:     "http://example.com/stream",
		Headers: http.Header{},
	}, RecordedResp{
		StatusCode: 200,
		Headers:    http.Header{"Content-Type": {"text/event-stream"}},
		SSEEvents: []SSEEvent{
			{OffsetMS: 0, Data: "cached1"},
			{OffsetMS: 100, Data: "cached2"},
		},
	})
	l2.Save(context.Background(), tape)

	proxy := NewProxy(l1, l2,
		WithProxyTransport(failingTransport(errors.New("down"))),
		WithProxySSETiming(SSETimingInstant()),
	)

	req, _ := http.NewRequest("GET", "http://example.com/stream", nil)
	resp, err := proxy.RoundTrip(req)
	if err != nil {
		t.Fatalf("RoundTrip: %v", err)
	}
	defer resp.Body.Close()

	if src := resp.Header.Get("X-Httptape-Source"); src != "l2-cache" {
		t.Errorf("X-Httptape-Source = %q, want l2-cache", src)
	}

	// Parse the SSE events from the fallback response.
	var received []SSEEvent
	parseSSEStream(resp.Body, time.Now(), func(ev SSEEvent) {
		ev.OffsetMS = 0
		received = append(received, ev)
	})

	if len(received) != 2 {
		t.Fatalf("got %d events, want 2", len(received))
	}
	if received[0].Data != "cached1" {
		t.Errorf("event[0].Data = %q, want cached1", received[0].Data)
	}
	if received[1].Data != "cached2" {
		t.Errorf("event[1].Data = %q, want cached2", received[1].Data)
	}
}

// ---------------------------------------------------------------------------
// Sanitizer SSE tests
// ---------------------------------------------------------------------------

func TestRedactSSEEventData(t *testing.T) {
	tape := Tape{
		Response: RecordedResp{
			SSEEvents: []SSEEvent{
				{Data: `{"token":"secret","name":"Alice"}`},
				{Data: `{"token":"other-secret","name":"Bob"}`},
				{Data: "not json"},
			},
		},
	}

	fn := RedactSSEEventData("$.token")
	result := fn(tape)

	if len(result.Response.SSEEvents) != 3 {
		t.Fatalf("expected 3 events, got %d", len(result.Response.SSEEvents))
	}

	// Token should be redacted.
	if strings.Contains(result.Response.SSEEvents[0].Data, "secret") {
		t.Error("event[0] token should be redacted")
	}
	if !strings.Contains(result.Response.SSEEvents[0].Data, Redacted) {
		t.Error("event[0] should contain redacted value")
	}
	if strings.Contains(result.Response.SSEEvents[1].Data, "other-secret") {
		t.Error("event[1] token should be redacted")
	}

	// Name should NOT be redacted.
	if !strings.Contains(result.Response.SSEEvents[0].Data, "Alice") {
		t.Error("event[0] name should be preserved")
	}

	// Non-JSON data should be left unchanged.
	if result.Response.SSEEvents[2].Data != "not json" {
		t.Errorf("non-JSON data changed: %q", result.Response.SSEEvents[2].Data)
	}
}

func TestRedactSSEEventData_NoopForNonSSE(t *testing.T) {
	tape := Tape{
		Response: RecordedResp{
			Body: []byte(`{"token":"secret"}`),
		},
	}
	fn := RedactSSEEventData("$.token")
	result := fn(tape)
	// Body should not be modified.
	if string(result.Response.Body) != `{"token":"secret"}` {
		t.Errorf("non-SSE tape body was modified: %q", string(result.Response.Body))
	}
}

func TestFakeSSEEventData(t *testing.T) {
	tape := Tape{
		Response: RecordedResp{
			SSEEvents: []SSEEvent{
				{Data: `{"email":"alice@corp.example","name":"Alice"}`},
				{Data: `{"email":"bob@corp.example","name":"Bob"}`},
			},
		},
	}

	fn := FakeSSEEventData("test-seed", "$.email")
	result := fn(tape)

	// Emails should be faked.
	if strings.Contains(result.Response.SSEEvents[0].Data, "alice@corp.example") {
		t.Error("event[0] email should be faked")
	}
	if !strings.Contains(result.Response.SSEEvents[0].Data, "@example.com") {
		t.Error("event[0] should have faked email")
	}

	// Names should be preserved.
	if !strings.Contains(result.Response.SSEEvents[0].Data, "Alice") {
		t.Error("event[0] name should be preserved")
	}

	// Deterministic: same seed + same input = same output.
	fn2 := FakeSSEEventData("test-seed", "$.email")
	result2 := fn2(tape)
	if result.Response.SSEEvents[0].Data != result2.Response.SSEEvents[0].Data {
		t.Error("faking should be deterministic")
	}
}

func TestFakeSSEEventData_NoopForNonSSE(t *testing.T) {
	tape := Tape{
		Response: RecordedResp{
			Body: []byte(`{"email":"alice@corp.example"}`),
		},
	}
	fn := FakeSSEEventData("seed", "$.email")
	result := fn(tape)
	if string(result.Response.Body) != `{"email":"alice@corp.example"}` {
		t.Errorf("non-SSE tape body was modified: %q", string(result.Response.Body))
	}
}

// ---------------------------------------------------------------------------
// Full record-replay integration (SSE)
// ---------------------------------------------------------------------------

func TestIntegration_SSE_RecordReplay(t *testing.T) {
	// Upstream serves an SSE stream with JSON payloads.
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		flusher, ok := w.(http.Flusher)
		if !ok {
			http.Error(w, "no flusher", 500)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(200)
		flusher.Flush()

		events := []string{
			`{"chunk":"Hello"}`,
			`{"chunk":" world"}`,
			`{"chunk":"!"}`,
		}
		for _, ev := range events {
			fmt.Fprintf(w, "data: %s\n\n", ev)
			flusher.Flush()
			time.Sleep(10 * time.Millisecond)
		}
	}))
	defer upstream.Close()

	// Record.
	store := NewMemoryStore()
	rec := NewRecorder(store, WithAsync(false))
	client := &http.Client{Transport: rec}

	resp, err := client.Get(upstream.URL + "/llm/stream")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	origBody, _ := io.ReadAll(resp.Body)
	resp.Body.Close()

	// Verify caller got the raw bytes.
	if !strings.Contains(string(origBody), `"chunk":"Hello"`) {
		t.Error("caller should see raw SSE bytes")
	}

	// Replay.
	srv := NewServer(store, WithSSETiming(SSETimingInstant()))
	ts := httptest.NewServer(srv)
	defer ts.Close()

	replayResp, err := http.Get(ts.URL + "/llm/stream")
	if err != nil {
		t.Fatalf("replay GET: %v", err)
	}
	defer replayResp.Body.Close()

	if replayResp.StatusCode != 200 {
		t.Errorf("replay status = %d, want 200", replayResp.StatusCode)
	}
	if ct := replayResp.Header.Get("Content-Type"); ct != "text/event-stream" {
		t.Errorf("Content-Type = %q, want text/event-stream", ct)
	}

	var replayed []SSEEvent
	parseSSEStream(replayResp.Body, time.Now(), func(ev SSEEvent) {
		ev.OffsetMS = 0
		replayed = append(replayed, ev)
	})

	if len(replayed) != 3 {
		t.Fatalf("replayed %d events, want 3", len(replayed))
	}

	wantData := []string{`{"chunk":"Hello"}`, `{"chunk":" world"}`, `{"chunk":"!"}`}
	for i, ev := range replayed {
		if ev.Data != wantData[i] {
			t.Errorf("replayed[%d].Data = %q, want %q", i, ev.Data, wantData[i])
		}
	}
}

func TestIntegration_SSE_RecordReplay_WithSanitization(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		flusher, ok := w.(http.Flusher)
		if !ok {
			http.Error(w, "no flusher", 500)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(200)
		flusher.Flush()

		fmt.Fprint(w, "data: {\"token\":\"secret-123\",\"user\":\"Alice\"}\n\n")
		flusher.Flush()
		fmt.Fprint(w, "data: {\"token\":\"secret-456\",\"user\":\"Bob\"}\n\n")
		flusher.Flush()
	}))
	defer upstream.Close()

	store := NewMemoryStore()
	sanitizer := NewPipeline(
		RedactSSEEventData("$.token"),
	)
	rec := NewRecorder(store, WithAsync(false), WithSanitizer(sanitizer))
	client := &http.Client{Transport: rec}

	resp, err := client.Get(upstream.URL + "/stream")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	io.ReadAll(resp.Body)
	resp.Body.Close()

	// Verify the tape has redacted tokens.
	tapes, _ := store.List(context.Background(), Filter{})
	if len(tapes) != 1 {
		t.Fatalf("got %d tapes, want 1", len(tapes))
	}

	for i, ev := range tapes[0].Response.SSEEvents {
		if strings.Contains(ev.Data, "secret") {
			t.Errorf("event[%d] should have redacted token: %s", i, ev.Data)
		}
		if !strings.Contains(ev.Data, Redacted) {
			t.Errorf("event[%d] should contain redacted marker: %s", i, ev.Data)
		}
	}

	// Replay and verify redacted values are served.
	srv := NewServer(store, WithSSETiming(SSETimingInstant()))
	ts := httptest.NewServer(srv)
	defer ts.Close()

	replayResp, err := http.Get(ts.URL + "/stream")
	if err != nil {
		t.Fatalf("replay GET: %v", err)
	}
	replayBody, _ := io.ReadAll(replayResp.Body)
	replayResp.Body.Close()

	if strings.Contains(string(replayBody), "secret") {
		t.Error("replayed SSE should not contain raw secret")
	}
}

// ---------------------------------------------------------------------------
// Proxy SSE passthrough integration
// ---------------------------------------------------------------------------

func TestIntegration_SSE_ProxyPassthrough(t *testing.T) {
	// Upstream SSE server.
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		flusher, ok := w.(http.Flusher)
		if !ok {
			http.Error(w, "no flusher", 500)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(200)
		flusher.Flush()

		fmt.Fprint(w, "data: live1\n\n")
		flusher.Flush()
		fmt.Fprint(w, "data: live2\n\n")
		flusher.Flush()
	}))
	defer upstream.Close()

	l1 := NewMemoryStore()
	l2 := NewMemoryStore()
	proxy := NewProxy(l1, l2, WithProxyTransport(upstream.Client().Transport))

	// Use the proxy as an HTTP handler via httptest.
	proxyHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Build outgoing request to upstream.
		outReq, _ := http.NewRequestWithContext(r.Context(), r.Method, upstream.URL+r.URL.Path, r.Body)
		resp, err := proxy.RoundTrip(outReq)
		if err != nil {
			http.Error(w, err.Error(), 502)
			return
		}
		defer resp.Body.Close()

		// Copy response headers and body.
		for k, vs := range resp.Header {
			for _, v := range vs {
				w.Header().Add(k, v)
			}
		}
		w.WriteHeader(resp.StatusCode)
		io.Copy(w, resp.Body)
	})

	ts := httptest.NewServer(proxyHandler)
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/sse-stream")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()

	// Verify the client received live events.
	if !strings.Contains(string(body), "data: live1") {
		t.Error("client should receive live1")
	}
	if !strings.Contains(string(body), "data: live2") {
		t.Error("client should receive live2")
	}

	// Give onDone a moment.
	time.Sleep(50 * time.Millisecond)

	// L1 and L2 should have the tape.
	l1Tapes, _ := l1.List(context.Background(), Filter{})
	if len(l1Tapes) != 1 {
		t.Fatalf("L1 tapes = %d, want 1", len(l1Tapes))
	}
	if !l1Tapes[0].Response.IsSSE() {
		t.Error("L1 tape should be SSE")
	}

	l2Tapes, _ := l2.List(context.Background(), Filter{})
	if len(l2Tapes) != 1 {
		t.Fatalf("L2 tapes = %d, want 1", len(l2Tapes))
	}
}

// ---------------------------------------------------------------------------
// health.go writeSSEEvent refactor verification
// ---------------------------------------------------------------------------

func TestHealth_StreamUsesWriteSSEEvent(t *testing.T) {
	// Verify that health streaming still works after the refactor to use
	// the shared writeSSEEvent helper.
	l1 := NewMemoryStore()
	l2 := NewMemoryStore()

	proxy := NewProxy(l1, l2,
		WithProxyTransport(successTransport(200, "ok")),
		WithProxyUpstreamURL("http://upstream.example"),
		WithProxyHealthEndpoint(),
		WithProxyProbeInterval(0),
	)
	defer proxy.Close()

	srv := httptest.NewServer(proxy.HealthHandler())
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	req, _ := http.NewRequestWithContext(ctx, "GET", srv.URL+"/__httptape/health/stream", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET stream: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "text/event-stream" {
		t.Errorf("Content-Type = %q, want text/event-stream", ct)
	}

	// Read the initial seed event.
	br := bufio.NewReader(resp.Body)
	payload, err := readSSEEvent(br, 2*time.Second)
	if err != nil {
		t.Fatalf("readSSEEvent: %v", err)
	}

	var snap HealthSnapshot
	if err := json.Unmarshal([]byte(payload), &snap); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if snap.State != StateLive {
		t.Errorf("state = %q, want live", snap.State)
	}
}

// ---------------------------------------------------------------------------
// Timing mode round-trip verification
// ---------------------------------------------------------------------------

func TestSSETimingRealtime_PreservesGaps(t *testing.T) {
	events := []SSEEvent{
		{OffsetMS: 0, Data: "a"},
		{OffsetMS: 150, Data: "b"},
	}

	rec := httptest.NewRecorder()
	flusher := http.ResponseWriter(rec).(http.Flusher)

	start := time.Now()
	replaySSEEvents(context.Background(), rec, flusher, events, SSETimingRealtime())
	elapsed := time.Since(start)

	// 150ms gap expected.
	assertTimingInRange(t, elapsed, 150*time.Millisecond)
}

// ---------------------------------------------------------------------------
// deepCopyTape SSEEvents test
// ---------------------------------------------------------------------------

func TestDeepCopyTape_SSEEvents(t *testing.T) {
	original := Tape{
		Response: RecordedResp{
			SSEEvents: []SSEEvent{
				{OffsetMS: 0, Data: "original"},
			},
		},
	}

	cp := deepCopyTape(original)
	cp.Response.SSEEvents[0].Data = "modified"

	if original.Response.SSEEvents[0].Data != "original" {
		t.Error("deepCopyTape should not alias SSEEvents slice")
	}
}

// ---------------------------------------------------------------------------
// parseSSEStream round-trip (parse -> write -> parse)
// ---------------------------------------------------------------------------

func TestSSE_ParseWriteRoundTrip(t *testing.T) {
	input := "event: msg\nid: 42\nretry: 5000\ndata: line1\ndata: line2\n\n"

	// Parse.
	var events []SSEEvent
	parseSSEStream(strings.NewReader(input), time.Now(), func(ev SSEEvent) {
		ev.OffsetMS = 0
		events = append(events, ev)
	})

	if len(events) != 1 {
		t.Fatalf("parsed %d events, want 1", len(events))
	}

	// Write.
	var buf bytes.Buffer
	writeSSEEvent(&buf, events[0])

	// Parse again.
	var events2 []SSEEvent
	parseSSEStream(strings.NewReader(buf.String()), time.Now(), func(ev SSEEvent) {
		ev.OffsetMS = 0
		events2 = append(events2, ev)
	})

	if len(events2) != 1 {
		t.Fatalf("round-trip parsed %d events, want 1", len(events2))
	}

	if events[0] != events2[0] {
		t.Errorf("round-trip mismatch: %+v != %+v", events[0], events2[0])
	}
}

// ---------------------------------------------------------------------------
// Edge case: negative offset clamp
// ---------------------------------------------------------------------------

func TestSSETimingRealtime_NegativeGapClamped(t *testing.T) {
	events := []SSEEvent{
		{OffsetMS: 200},
		{OffsetMS: 100}, // out of order (shouldn't happen, but defensive)
	}
	mode := SSETimingRealtime()
	d := mode.delay(events, 1)
	if d != 0 {
		t.Errorf("negative gap should be clamped to 0, got %v", d)
	}
}

func TestSSETimingAccelerated_NegativeGapClamped(t *testing.T) {
	events := []SSEEvent{
		{OffsetMS: 200},
		{OffsetMS: 100},
	}
	mode := SSETimingAccelerated(2.0)
	d := mode.delay(events, 1)
	if d != 0 {
		t.Errorf("negative gap should be clamped to 0, got %v", d)
	}
}

// ---------------------------------------------------------------------------
// SSETimingAccelerated fractional factor
// ---------------------------------------------------------------------------

func TestSSETimingAccelerated_FractionalFactor(t *testing.T) {
	events := []SSEEvent{
		{OffsetMS: 0},
		{OffsetMS: 100},
	}
	mode := SSETimingAccelerated(0.5)
	d := mode.delay(events, 1)
	expected := 200 * time.Millisecond
	if math.Abs(float64(d-expected)) > float64(5*time.Millisecond) {
		t.Errorf("delay = %v, want ~%v", d, expected)
	}
}

// ---------------------------------------------------------------------------
// Concurrent SSE recording race test
// ---------------------------------------------------------------------------

func TestSSERecordingReader_ConcurrentReads(t *testing.T) {
	// Simulate concurrent reads on the recording reader.
	input := ""
	for i := 0; i < 100; i++ {
		input += fmt.Sprintf("data: event%d\n\n", i)
	}
	upstream := io.NopCloser(strings.NewReader(input))

	var mu sync.Mutex
	var events []SSEEvent
	doneCh := make(chan struct{})

	reader := newSSERecordingReader(upstream, time.Now(), func(ev SSEEvent) {
		mu.Lock()
		events = append(events, ev)
		mu.Unlock()
	}, func(err error) {
		close(doneCh)
	})

	// Read in small chunks to stress the TeeReader.
	buf := make([]byte, 16)
	for {
		_, err := reader.Read(buf)
		if err != nil {
			break
		}
	}
	reader.Close()
	<-doneCh

	mu.Lock()
	defer mu.Unlock()
	if len(events) != 100 {
		t.Errorf("got %d events, want 100", len(events))
	}
}

// ---------------------------------------------------------------------------
// sseTimingMode seal method coverage
// ---------------------------------------------------------------------------

func TestSSETimingMode_SealMethodsAreCallable(t *testing.T) {
	// The sseTimingMode() methods are seal methods that prevent external
	// implementations. They are no-op but should be covered for completeness.
	// We exercise them indirectly through the SSETimingMode interface.
	modes := []SSETimingMode{
		SSETimingRealtime(),
		SSETimingAccelerated(1.0),
		SSETimingInstant(),
	}
	for _, m := range modes {
		// Call the seal method via the interface. It's a no-op, but this
		// proves the method exists and executes without panic.
		m.sseTimingMode()
	}
}

// ---------------------------------------------------------------------------
// isSSEContentType: mime.ParseMediaType error fallback
// ---------------------------------------------------------------------------

func TestIsSSEContentType_FallbackOnMalformedMediaType(t *testing.T) {
	// A content type that mime.ParseMediaType fails to parse but still
	// has the right prefix should match via the fallback path.
	tests := []struct {
		name string
		ct   string
		want bool
	}{
		{
			name: "malformed params but correct type prefix triggers fallback match",
			ct:   "text/event-stream; ===invalid===",
			want: true,
		},
		{
			name: "malformed params with wrong type prefix does not match",
			ct:   "application/json; ===invalid===",
			want: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isSSEContentType(tt.ct); got != tt.want {
				t.Errorf("isSSEContentType(%q) = %v, want %v", tt.ct, got, tt.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// writeSSEEvent: error paths for each field type
// ---------------------------------------------------------------------------

// limitWriter allows the first N bytes to be written, then returns an error.
// This lets us trigger write failures at specific points in writeSSEEvent.
type limitWriter struct {
	remaining int
	err       error
}

func (w *limitWriter) Write(p []byte) (int, error) {
	if w.remaining <= 0 {
		return 0, w.err
	}
	if len(p) <= w.remaining {
		w.remaining -= len(p)
		return len(p), nil
	}
	n := w.remaining
	w.remaining = 0
	return n, w.err
}

func TestWriteSSEEvent_ErrorOnEventField(t *testing.T) {
	// Fail after writing 0 bytes -- the "event:" field write should fail.
	w := &limitWriter{remaining: 0, err: errors.New("disk full")}
	err := writeSSEEvent(w, SSEEvent{Type: "update", Data: "hello"})
	if err == nil {
		t.Fatal("expected error writing event field")
	}
	if !strings.Contains(err.Error(), "write event field") {
		t.Errorf("error = %q, want it to mention 'write event field'", err)
	}
}

func TestWriteSSEEvent_ErrorOnIDField(t *testing.T) {
	// Allow "event:" line to succeed, then fail on "id:" line.
	eventLine := "event: update\n"
	w := &limitWriter{remaining: len(eventLine), err: errors.New("disk full")}
	err := writeSSEEvent(w, SSEEvent{Type: "update", ID: "42", Data: "hello"})
	if err == nil {
		t.Fatal("expected error writing id field")
	}
	if !strings.Contains(err.Error(), "write id field") {
		t.Errorf("error = %q, want it to mention 'write id field'", err)
	}
}

func TestWriteSSEEvent_ErrorOnRetryField(t *testing.T) {
	// Allow "event:" and "id:" lines, then fail on "retry:".
	written := "event: update\nid: 42\n"
	w := &limitWriter{remaining: len(written), err: errors.New("disk full")}
	err := writeSSEEvent(w, SSEEvent{Type: "update", ID: "42", Retry: 5000, Data: "hello"})
	if err == nil {
		t.Fatal("expected error writing retry field")
	}
	if !strings.Contains(err.Error(), "write retry field") {
		t.Errorf("error = %q, want it to mention 'write retry field'", err)
	}
}

func TestWriteSSEEvent_ErrorOnTerminator(t *testing.T) {
	// Allow everything except the final blank line.
	dataLine := "data: hello\n"
	w := &limitWriter{remaining: len(dataLine), err: errors.New("disk full")}
	err := writeSSEEvent(w, SSEEvent{Data: "hello"})
	if err == nil {
		t.Fatal("expected error writing terminator")
	}
	if !strings.Contains(err.Error(), "write event terminator") {
		t.Errorf("error = %q, want it to mention 'write event terminator'", err)
	}
}

// ---------------------------------------------------------------------------
// replaySSEEvents: context cancelled between delay and write
// ---------------------------------------------------------------------------

func TestReplaySSEEvents_ContextCancelledAfterDelay(t *testing.T) {
	// Create events where the second event has a small delay. Cancel the
	// context during that delay window so the post-delay ctx.Err() check
	// catches it.
	events := []SSEEvent{
		{OffsetMS: 0, Data: "first"},
		{OffsetMS: 50, Data: "second"},
	}

	rec := httptest.NewRecorder()
	flusher := http.ResponseWriter(rec).(http.Flusher)

	// Cancel context right after the first event is written.
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		// Wait enough for the first event to be written and the delay
		// to start, but cancel before it finishes.
		time.Sleep(20 * time.Millisecond)
		cancel()
	}()

	err := replaySSEEvents(ctx, rec, flusher, events, SSETimingRealtime())
	if err == nil {
		t.Fatal("expected context cancellation error")
	}
	if !errors.Is(err, context.Canceled) {
		t.Errorf("error = %v, want context.Canceled", err)
	}
}

// ---------------------------------------------------------------------------
// replaySSEEvents: write error propagated
// ---------------------------------------------------------------------------

// failingResponseWriter is an http.ResponseWriter that fails on Write.
type failingResponseWriter struct {
	header http.Header
}

func (w *failingResponseWriter) Header() http.Header { return w.header }
func (w *failingResponseWriter) Write(_ []byte) (int, error) {
	return 0, errors.New("broken pipe")
}
func (w *failingResponseWriter) WriteHeader(_ int) {}

// noopFlusher implements http.Flusher as a no-op.
type noopFlusher struct{}

func (noopFlusher) Flush() {}

func TestReplaySSEEvents_ContextCancelledBetweenZeroDelayEvents(t *testing.T) {
	// When the delay is 0, the timer select is skipped and the post-delay
	// ctx.Err() check at line 284 is the only cancellation check point.
	// This test cancels the context before replay starts, so the first
	// event's post-delay check catches it immediately.
	events := []SSEEvent{
		{OffsetMS: 0, Data: "first"},
		{OffsetMS: 0, Data: "second"},
	}

	rec := httptest.NewRecorder()
	flusher := http.ResponseWriter(rec).(http.Flusher)

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel before replay begins

	err := replaySSEEvents(ctx, rec, flusher, events, SSETimingInstant())
	if err == nil {
		t.Fatal("expected context cancellation error")
	}
	if !errors.Is(err, context.Canceled) {
		t.Errorf("error = %v, want context.Canceled", err)
	}
}

func TestReplaySSEEvents_WriteErrorPropagated(t *testing.T) {
	events := []SSEEvent{
		{OffsetMS: 0, Data: "hello"},
	}

	w := &failingResponseWriter{header: http.Header{}}
	err := replaySSEEvents(context.Background(), w, noopFlusher{}, events, SSETimingInstant())
	if err == nil {
		t.Fatal("expected write error")
	}
	if !strings.Contains(err.Error(), "broken pipe") {
		t.Errorf("error = %q, want it to mention 'broken pipe'", err)
	}
}
