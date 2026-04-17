package httptape

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"mime"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

// SSEEvent represents a single Server-Sent Event parsed from a
// text/event-stream response. It is a pure value type with no I/O.
type SSEEvent struct {
	// OffsetMS is the number of milliseconds from the moment the response
	// headers were received to when this event was fully dispatched
	// (blank line seen). Monotonically non-decreasing across a stream.
	OffsetMS int64 `json:"offset_ms"`

	// Type is the SSE event type (from the "event:" field).
	// Empty string means the default "message" type per the SSE spec.
	Type string `json:"type,omitempty"`

	// Data is the event payload. Multiple "data:" lines are joined with
	// "\n" per the SSE specification. Always present (may be empty).
	Data string `json:"data"`

	// ID is the event ID (from the "id:" field). Empty when absent.
	ID string `json:"id,omitempty"`

	// Retry is the reconnection time in milliseconds (from the "retry:"
	// field). Zero when absent.
	Retry int `json:"retry,omitempty"`
}

// SSETimingMode controls how inter-event timing is applied during SSE
// replay. It is a sealed interface implemented by three unexported types.
type SSETimingMode interface {
	// delay returns the duration to wait before emitting the event at
	// index i, given the full list of events. The implementation
	// computes the inter-event gap from OffsetMS values.
	delay(events []SSEEvent, i int) time.Duration
	sseTimingMode() // seal
}

// sseTimingRealtime replays events with the original recorded inter-event timing.
type sseTimingRealtime struct{}

func (sseTimingRealtime) sseTimingMode() {}
func (sseTimingRealtime) delay(events []SSEEvent, i int) time.Duration {
	if i == 0 {
		return 0
	}
	gap := events[i].OffsetMS - events[i-1].OffsetMS
	if gap < 0 {
		gap = 0
	}
	return time.Duration(gap) * time.Millisecond
}

// sseTimingAccelerated divides all inter-event gaps by the given factor.
type sseTimingAccelerated struct {
	factor float64
}

func (sseTimingAccelerated) sseTimingMode() {}
func (a sseTimingAccelerated) delay(events []SSEEvent, i int) time.Duration {
	if i == 0 {
		return 0
	}
	gap := events[i].OffsetMS - events[i-1].OffsetMS
	if gap < 0 {
		gap = 0
	}
	return time.Duration(float64(gap)/a.factor) * time.Millisecond
}

// sseTimingInstant emits all events back-to-back with no deliberate delay.
type sseTimingInstant struct{}

func (sseTimingInstant) sseTimingMode() {}
func (sseTimingInstant) delay(_ []SSEEvent, _ int) time.Duration {
	return 0
}

// SSETimingRealtime returns an SSETimingMode that replays events with the
// original recorded inter-event timing.
func SSETimingRealtime() SSETimingMode {
	return sseTimingRealtime{}
}

// SSETimingAccelerated returns an SSETimingMode that divides all
// inter-event gaps by the given factor. Factor must be > 0; panics
// otherwise (constructor guard).
func SSETimingAccelerated(factor float64) SSETimingMode {
	if factor <= 0 {
		panic("httptape: SSETimingAccelerated factor must be > 0")
	}
	return sseTimingAccelerated{factor: factor}
}

// SSETimingInstant returns an SSETimingMode that emits all events
// back-to-back with no deliberate delay.
func SSETimingInstant() SSETimingMode {
	return sseTimingInstant{}
}

// isSSEContentType reports whether the Content-Type header value indicates
// a text/event-stream response. Case-insensitive on type/subtype,
// parameter-tolerant (e.g., "text/event-stream; charset=utf-8").
func isSSEContentType(contentType string) bool {
	if contentType == "" {
		return false
	}
	mediaType, _, err := mime.ParseMediaType(contentType)
	if err != nil {
		// Fallback: try a simple case-insensitive prefix check.
		return strings.HasPrefix(strings.ToLower(strings.TrimSpace(contentType)), "text/event-stream")
	}
	return strings.EqualFold(mediaType, "text/event-stream")
}

// parseSSEStream reads an io.Reader line-by-line and parses SSE events
// per the W3C Server-Sent Events specification. It calls onEvent for each
// fully dispatched event (blank line seen). The startTime is used to
// compute OffsetMS for each event.
//
// Comment lines (starting with ':') are silently ignored.
// Unknown field names are silently ignored per the SSE spec.
// The function returns when the reader returns io.EOF or an error.
// On io.EOF, it returns nil. On other errors, it returns the error.
// A partially accumulated event (no blank line before EOF) is discarded.
func parseSSEStream(r io.Reader, startTime time.Time, onEvent func(SSEEvent)) error {
	scanner := bufio.NewScanner(r)
	// Increase the scanner buffer to handle large data lines (e.g., LLM completions).
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	var (
		eventType string
		data      []string
		id        string
		retry     int
		hasData   bool
	)

	dispatch := func() {
		if !hasData && eventType == "" && id == "" && retry == 0 {
			// Nothing accumulated -- skip (per spec: blank lines between events).
			return
		}
		ev := SSEEvent{
			OffsetMS: time.Since(startTime).Milliseconds(),
			Type:     eventType,
			Data:     strings.Join(data, "\n"),
			ID:       id,
			Retry:    retry,
		}
		onEvent(ev)
		// Reset per-event state.
		eventType = ""
		data = nil
		id = ""
		retry = 0
		hasData = false
	}

	for scanner.Scan() {
		line := scanner.Text()

		// Blank line dispatches the event.
		if line == "" {
			dispatch()
			continue
		}

		// Comment line: ignore.
		if strings.HasPrefix(line, ":") {
			continue
		}

		// Parse field name and value.
		var field, value string
		if idx := strings.IndexByte(line, ':'); idx >= 0 {
			field = line[:idx]
			value = line[idx+1:]
			// Per spec: if the character following the colon is a space,
			// remove it.
			if strings.HasPrefix(value, " ") {
				value = value[1:]
			}
		} else {
			// No colon: field name is the entire line, value is empty.
			field = line
			value = ""
		}

		switch field {
		case "event":
			eventType = value
		case "data":
			data = append(data, value)
			hasData = true
		case "id":
			// Per spec: if the value contains U+0000 NULL, ignore the field.
			if !strings.ContainsRune(value, 0) {
				id = value
			}
		case "retry":
			// Per spec: if the value consists of ASCII digits only, set retry.
			if n, err := strconv.Atoi(value); err == nil && n >= 0 {
				retry = n
			}
		default:
			// Unknown field: silently ignored per spec.
		}
	}

	if err := scanner.Err(); err != nil {
		return err
	}
	// Partial event at EOF is discarded (no blank line to dispatch it).
	return nil
}

// writeSSEEvent writes a single SSEEvent to w in SSE wire format.
// Multi-line Data is re-emitted as multiple "data:" lines.
// Returns any write error.
func writeSSEEvent(w io.Writer, ev SSEEvent) error {
	if ev.Type != "" {
		if _, err := fmt.Fprintf(w, "event: %s\n", ev.Type); err != nil {
			return fmt.Errorf("write event field: %w", err)
		}
	}
	if ev.ID != "" {
		if _, err := fmt.Fprintf(w, "id: %s\n", ev.ID); err != nil {
			return fmt.Errorf("write id field: %w", err)
		}
	}
	if ev.Retry > 0 {
		if _, err := fmt.Fprintf(w, "retry: %d\n", ev.Retry); err != nil {
			return fmt.Errorf("write retry field: %w", err)
		}
	}

	// Data: split on newlines and emit each as a separate "data:" line.
	lines := strings.Split(ev.Data, "\n")
	for _, line := range lines {
		if _, err := fmt.Fprintf(w, "data: %s\n", line); err != nil {
			return fmt.Errorf("write data field: %w", err)
		}
	}

	// Terminating blank line.
	if _, err := fmt.Fprint(w, "\n"); err != nil {
		return fmt.Errorf("write event terminator: %w", err)
	}

	return nil
}

// replaySSEEvents writes a sequence of SSEEvents to an http.ResponseWriter
// using the given SSETimingMode. It flushes after each event. It respects
// ctx cancellation and returns promptly when the client disconnects.
// Returns nil on success, context.Canceled on client disconnect, or a
// write error.
func replaySSEEvents(ctx context.Context, w http.ResponseWriter, flusher http.Flusher, events []SSEEvent, timing SSETimingMode) error {
	for i, ev := range events {
		d := timing.delay(events, i)
		if d > 0 {
			timer := time.NewTimer(d)
			select {
			case <-ctx.Done():
				timer.Stop()
				return ctx.Err()
			case <-timer.C:
			}
		}

		// Check context after waking up from delay.
		if ctx.Err() != nil {
			return ctx.Err()
		}

		if err := writeSSEEvent(w, ev); err != nil {
			return err
		}
		flusher.Flush()
	}
	return nil
}

// sseRecordingReader wraps the upstream response body and tees SSE events
// to a callback as they are parsed, while still delivering the original
// bytes to the caller unchanged. It implements io.ReadCloser.
//
// The caller reads from sseRecordingReader as they would from the original
// body. Internally, an io.TeeReader feeds bytes to both the caller and a
// pipe whose read end is consumed by parseSSEStream in a background
// goroutine. When the caller closes the reader (or the upstream hits EOF),
// the pipe is closed and the background goroutine exits.
//
// Thread safety: the callback (onEvent) is called from the background
// goroutine. The caller must synchronize access to any shared state.
type sseRecordingReader struct {
	tee       io.Reader     // reads from upstream, writes to pipe
	upstream  io.ReadCloser // original body
	pipeW     *io.PipeWriter
	closeOnce sync.Once
	done      chan struct{} // closed when parser goroutine exits
	parseErr  error         // set by parser goroutine before closing done
	readErr   error         // last non-EOF error from Read, propagated to pipe on Close
}

// newSSERecordingReader creates an sseRecordingReader that parses SSE
// events from upstream in parallel with the caller's reads. onEvent is
// called for each dispatched event. onDone is called exactly once when
// the background parser exits (with nil on clean EOF, or the error).
func newSSERecordingReader(upstream io.ReadCloser, startTime time.Time, onEvent func(SSEEvent), onDone func(error)) *sseRecordingReader {
	pr, pw := io.Pipe()
	tee := io.TeeReader(upstream, pw)

	r := &sseRecordingReader{
		tee:      tee,
		upstream: upstream,
		pipeW:    pw,
		done:     make(chan struct{}),
	}

	go func() {
		err := parseSSEStream(pr, startTime, onEvent)
		r.parseErr = err
		if onDone != nil {
			onDone(err)
		}
		close(r.done)
	}()

	return r
}

// Read implements io.Reader. Bytes pass through the TeeReader, which
// copies them to the pipe for the background parser. If the upstream
// returns a non-EOF error, it is recorded so Close can propagate it
// to the parser via the pipe.
func (r *sseRecordingReader) Read(p []byte) (int, error) {
	n, err := r.tee.Read(p)
	if err != nil && err != io.EOF {
		r.readErr = err
	}
	return n, err
}

// Close closes the upstream body and the pipe writer, causing the
// background parser to see EOF (or an error) and exit. It waits for the
// parser goroutine to finish before returning.
//
// If the upstream returned a non-EOF error during reading, that error is
// propagated to the parser via PipeWriter.CloseWithError so the parser
// treats the stream as truncated.
func (r *sseRecordingReader) Close() error {
	var upstreamErr error
	r.closeOnce.Do(func() {
		upstreamErr = r.upstream.Close()
		if r.readErr != nil {
			r.pipeW.CloseWithError(r.readErr)
		} else {
			r.pipeW.Close()
		}
	})
	<-r.done
	return upstreamErr
}
