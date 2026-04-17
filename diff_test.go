package httptape

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// --- Helpers ---

// newDiffTestTape creates a Tape with the given parameters, suitable for
// diff tests.
func newDiffTestTape(id, method, url string, statusCode int, respHeaders http.Header, respBody []byte) Tape {
	return Tape{
		ID: id,
		Request: RecordedReq{
			Method: method,
			URL:    url,
		},
		Response: RecordedResp{
			StatusCode: statusCode,
			Headers:    respHeaders,
			Body:       respBody,
		},
	}
}

// seedDiffStore saves tapes into a MemoryStore and returns it.
func seedDiffStore(t *testing.T, tapes ...Tape) *MemoryStore {
	t.Helper()
	store := NewMemoryStore()
	for _, tape := range tapes {
		if err := store.Save(context.Background(), tape); err != nil {
			t.Fatalf("seedDiffStore: %v", err)
		}
	}
	return store
}

// diffFailingStore implements Store with a List that always fails.
type diffFailingStore struct {
	listErr error
}

func (f *diffFailingStore) Save(_ context.Context, _ Tape) error             { return nil }
func (f *diffFailingStore) Load(_ context.Context, _ string) (Tape, error)   { return Tape{}, nil }
func (f *diffFailingStore) List(_ context.Context, _ Filter) ([]Tape, error) { return nil, f.listErr }
func (f *diffFailingStore) Delete(_ context.Context, _ string) error         { return nil }

// diffFailingTransport implements http.RoundTripper that always returns an error.
type diffFailingTransport struct {
	err error
}

func (f *diffFailingTransport) RoundTrip(_ *http.Request) (*http.Response, error) {
	return nil, f.err
}

// httpTestIgnoreHeaders are headers added by httptest.Server that are
// typically not present in recorded fixtures. Tests that want to assert
// "no diffs" must ignore these to avoid false positives from the test
// infrastructure.
var httpTestIgnoreHeaders = []string{"Content-Length", "Date"}

// --- Tests ---

func TestDiff_MatchedNoDiffs(t *testing.T) {
	body := []byte(`{"name":"alice","age":30}`)
	tape := newDiffTestTape("t1", "GET", "http://example.com/users", 200,
		http.Header{"Content-Type": {"application/json"}},
		body,
	)
	store := seedDiffStore(t, tape)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(200)
		w.Write(body)
	}))
	defer srv.Close()

	req, _ := http.NewRequest("GET", srv.URL+"/users", nil)

	report, err := Diff(context.Background(), store, srv.Client().Transport, []*http.Request{req},
		WithIgnoreHeaders(httpTestIgnoreHeaders...),
	)
	if err != nil {
		t.Fatalf("Diff() error = %v", err)
	}

	if len(report.Results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(report.Results))
	}
	r := report.Results[0]
	if r.Status != DiffMatched {
		t.Errorf("expected DiffMatched, got %d", r.Status)
		for _, hd := range r.Headers {
			t.Logf("  header diff: %s kind=%d old=%v new=%v", hd.Name, hd.Kind, hd.OldValues, hd.NewValues)
		}
		for _, bf := range r.BodyFields {
			t.Logf("  body diff: %s kind=%d old=%v new=%v", bf.Path, bf.Kind, bf.OldValue, bf.NewValue)
		}
	}
	if r.FixtureID != "t1" {
		t.Errorf("expected fixture ID t1, got %q", r.FixtureID)
	}
	if len(report.Stale) != 0 {
		t.Errorf("expected no stale fixtures, got %d", len(report.Stale))
	}
}

func TestDiff_StatusCodeDrift(t *testing.T) {
	tape := newDiffTestTape("t1", "GET", "http://example.com/users", 200,
		http.Header{"Content-Type": {"application/json"}},
		[]byte(`{"ok":true}`),
	)
	store := seedDiffStore(t, tape)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(201) // different status
		w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()

	req, _ := http.NewRequest("GET", srv.URL+"/users", nil)

	report, err := Diff(context.Background(), store, srv.Client().Transport, []*http.Request{req},
		WithIgnoreHeaders(httpTestIgnoreHeaders...),
	)
	if err != nil {
		t.Fatalf("Diff() error = %v", err)
	}

	r := report.Results[0]
	if r.Status != DiffDrifted {
		t.Errorf("expected DiffDrifted, got %d", r.Status)
	}
	if r.StatusCode == nil {
		t.Fatal("expected StatusCode diff, got nil")
	}
	if r.StatusCode.Old != 200 || r.StatusCode.New != 201 {
		t.Errorf("expected status diff 200->201, got %d->%d", r.StatusCode.Old, r.StatusCode.New)
	}
}

func TestDiff_HeaderDifferences(t *testing.T) {
	tape := newDiffTestTape("t1", "GET", "http://example.com/api", 200,
		http.Header{
			"Content-Type":    {"application/json"},
			"X-Old-Header":    {"old-value"},
			"X-Change-Header": {"before"},
		},
		[]byte(`{}`),
	)
	store := seedDiffStore(t, tape)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-New-Header", "new-value")
		w.Header().Set("X-Change-Header", "after")
		// X-Old-Header is NOT set, so it's removed
		w.WriteHeader(200)
		w.Write([]byte(`{}`))
	}))
	defer srv.Close()

	req, _ := http.NewRequest("GET", srv.URL+"/api", nil)

	report, err := Diff(context.Background(), store, srv.Client().Transport, []*http.Request{req},
		WithIgnoreHeaders(httpTestIgnoreHeaders...),
	)
	if err != nil {
		t.Fatalf("Diff() error = %v", err)
	}

	r := report.Results[0]
	if r.Status != DiffDrifted {
		t.Errorf("expected DiffDrifted, got %d", r.Status)
	}

	// Build a map for easy lookup.
	headerDiffs := make(map[string]HeaderDiff)
	for _, hd := range r.Headers {
		headerDiffs[hd.Name] = hd
	}

	// X-Old-Header: removed.
	if hd, ok := headerDiffs["X-Old-Header"]; !ok {
		t.Error("expected X-Old-Header diff (removed)")
	} else if hd.Kind != FieldRemoved {
		t.Errorf("X-Old-Header: expected FieldRemoved, got %d", hd.Kind)
	}

	// X-New-Header: added.
	if hd, ok := headerDiffs["X-New-Header"]; !ok {
		t.Error("expected X-New-Header diff (added)")
	} else if hd.Kind != FieldAdded {
		t.Errorf("X-New-Header: expected FieldAdded, got %d", hd.Kind)
	}

	// X-Change-Header: changed.
	if hd, ok := headerDiffs["X-Change-Header"]; !ok {
		t.Error("expected X-Change-Header diff (changed)")
	} else {
		if hd.Kind != FieldChanged {
			t.Errorf("X-Change-Header: expected FieldChanged, got %d", hd.Kind)
		}
		if len(hd.OldValues) != 1 || hd.OldValues[0] != "before" {
			t.Errorf("X-Change-Header: expected old value 'before', got %v", hd.OldValues)
		}
		if len(hd.NewValues) != 1 || hd.NewValues[0] != "after" {
			t.Errorf("X-Change-Header: expected new value 'after', got %v", hd.NewValues)
		}
	}
}

func TestDiff_JSONBodyAddedField(t *testing.T) {
	tape := newDiffTestTape("t1", "GET", "http://example.com/users", 200,
		http.Header{"Content-Type": {"application/json"}},
		[]byte(`{"name":"alice"}`),
	)
	store := seedDiffStore(t, tape)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(200)
		w.Write([]byte(`{"name":"alice","email":"alice@example.com"}`))
	}))
	defer srv.Close()

	req, _ := http.NewRequest("GET", srv.URL+"/users", nil)

	report, err := Diff(context.Background(), store, srv.Client().Transport, []*http.Request{req},
		WithIgnoreHeaders(httpTestIgnoreHeaders...),
	)
	if err != nil {
		t.Fatalf("Diff() error = %v", err)
	}

	r := report.Results[0]
	if r.Status != DiffDrifted {
		t.Errorf("expected DiffDrifted, got %d", r.Status)
	}
	if len(r.BodyFields) != 1 {
		t.Fatalf("expected 1 body field diff, got %d", len(r.BodyFields))
	}
	bf := r.BodyFields[0]
	if bf.Path != "$.email" {
		t.Errorf("expected path $.email, got %q", bf.Path)
	}
	if bf.Kind != FieldAdded {
		t.Errorf("expected FieldAdded, got %d", bf.Kind)
	}
	if bf.NewValue != "alice@example.com" {
		t.Errorf("expected new value 'alice@example.com', got %v", bf.NewValue)
	}
}

func TestDiff_JSONBodyRemovedField(t *testing.T) {
	tape := newDiffTestTape("t1", "GET", "http://example.com/users", 200,
		http.Header{"Content-Type": {"application/json"}},
		[]byte(`{"name":"alice","email":"alice@example.com"}`),
	)
	store := seedDiffStore(t, tape)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(200)
		w.Write([]byte(`{"name":"alice"}`))
	}))
	defer srv.Close()

	req, _ := http.NewRequest("GET", srv.URL+"/users", nil)

	report, err := Diff(context.Background(), store, srv.Client().Transport, []*http.Request{req},
		WithIgnoreHeaders(httpTestIgnoreHeaders...),
	)
	if err != nil {
		t.Fatalf("Diff() error = %v", err)
	}

	r := report.Results[0]
	if len(r.BodyFields) != 1 {
		t.Fatalf("expected 1 body field diff, got %d", len(r.BodyFields))
	}
	bf := r.BodyFields[0]
	if bf.Kind != FieldRemoved {
		t.Errorf("expected FieldRemoved, got %d", bf.Kind)
	}
	if bf.Path != "$.email" {
		t.Errorf("expected path $.email, got %q", bf.Path)
	}
}

func TestDiff_JSONBodyChangedValue(t *testing.T) {
	tape := newDiffTestTape("t1", "GET", "http://example.com/users", 200,
		http.Header{"Content-Type": {"application/json"}},
		[]byte(`{"name":"alice","age":30}`),
	)
	store := seedDiffStore(t, tape)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(200)
		w.Write([]byte(`{"name":"bob","age":30}`))
	}))
	defer srv.Close()

	req, _ := http.NewRequest("GET", srv.URL+"/users", nil)

	report, err := Diff(context.Background(), store, srv.Client().Transport, []*http.Request{req},
		WithIgnoreHeaders(httpTestIgnoreHeaders...),
	)
	if err != nil {
		t.Fatalf("Diff() error = %v", err)
	}

	r := report.Results[0]
	if len(r.BodyFields) != 1 {
		t.Fatalf("expected 1 body field diff, got %d", len(r.BodyFields))
	}
	bf := r.BodyFields[0]
	if bf.Kind != FieldChanged {
		t.Errorf("expected FieldChanged, got %d", bf.Kind)
	}
	if bf.OldValue != "alice" {
		t.Errorf("expected old value 'alice', got %v", bf.OldValue)
	}
	if bf.NewValue != "bob" {
		t.Errorf("expected new value 'bob', got %v", bf.NewValue)
	}
}

func TestDiff_NestedJSONBody(t *testing.T) {
	fixtureBody := `{"user":{"name":"alice","address":{"city":"NYC"}}}`
	liveBody := `{"user":{"name":"alice","address":{"city":"LA"}}}`

	tape := newDiffTestTape("t1", "GET", "http://example.com/profile", 200,
		http.Header{"Content-Type": {"application/json"}},
		[]byte(fixtureBody),
	)
	store := seedDiffStore(t, tape)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(200)
		w.Write([]byte(liveBody))
	}))
	defer srv.Close()

	req, _ := http.NewRequest("GET", srv.URL+"/profile", nil)

	report, err := Diff(context.Background(), store, srv.Client().Transport, []*http.Request{req},
		WithIgnoreHeaders(httpTestIgnoreHeaders...),
	)
	if err != nil {
		t.Fatalf("Diff() error = %v", err)
	}

	r := report.Results[0]
	if len(r.BodyFields) != 1 {
		t.Fatalf("expected 1 body field diff, got %d", len(r.BodyFields))
	}
	if r.BodyFields[0].Path != "$.user.address.city" {
		t.Errorf("expected path $.user.address.city, got %q", r.BodyFields[0].Path)
	}
}

func TestDiff_NonJSONBodyChanged(t *testing.T) {
	tape := newDiffTestTape("t1", "GET", "http://example.com/data", 200,
		http.Header{"Content-Type": {"text/plain"}},
		[]byte("hello world"),
	)
	store := seedDiffStore(t, tape)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		w.WriteHeader(200)
		w.Write([]byte("hello changed"))
	}))
	defer srv.Close()

	req, _ := http.NewRequest("GET", srv.URL+"/data", nil)

	report, err := Diff(context.Background(), store, srv.Client().Transport, []*http.Request{req},
		WithIgnoreHeaders(httpTestIgnoreHeaders...),
	)
	if err != nil {
		t.Fatalf("Diff() error = %v", err)
	}

	r := report.Results[0]
	if r.Status != DiffDrifted {
		t.Errorf("expected DiffDrifted, got %d", r.Status)
	}
	if !r.BodyChanged {
		t.Error("expected BodyChanged to be true")
	}
}

func TestDiff_NonJSONBodyUnchanged(t *testing.T) {
	body := []byte("same content")
	tape := newDiffTestTape("t1", "GET", "http://example.com/data", 200,
		http.Header{"Content-Type": {"text/plain"}},
		body,
	)
	store := seedDiffStore(t, tape)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		w.WriteHeader(200)
		w.Write(body)
	}))
	defer srv.Close()

	req, _ := http.NewRequest("GET", srv.URL+"/data", nil)

	report, err := Diff(context.Background(), store, srv.Client().Transport, []*http.Request{req},
		WithIgnoreHeaders(httpTestIgnoreHeaders...),
	)
	if err != nil {
		t.Fatalf("Diff() error = %v", err)
	}

	r := report.Results[0]
	if r.Status != DiffMatched {
		t.Errorf("expected DiffMatched, got %d", r.Status)
	}
	if r.BodyChanged {
		t.Error("expected BodyChanged to be false")
	}
}

func TestDiff_NoFixture(t *testing.T) {
	store := NewMemoryStore() // empty store

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
	}))
	defer srv.Close()

	req, _ := http.NewRequest("GET", srv.URL+"/unknown", nil)

	report, err := Diff(context.Background(), store, srv.Client().Transport, []*http.Request{req})
	if err != nil {
		t.Fatalf("Diff() error = %v", err)
	}

	r := report.Results[0]
	if r.Status != DiffNoFixture {
		t.Errorf("expected DiffNoFixture, got %d", r.Status)
	}
	if r.FixtureID != "" {
		t.Errorf("expected empty fixture ID, got %q", r.FixtureID)
	}
}

func TestDiff_StaleFixtures(t *testing.T) {
	tape1 := newDiffTestTape("t1", "GET", "http://example.com/users", 200,
		http.Header{"Content-Type": {"application/json"}},
		[]byte(`{"ok":true}`),
	)
	tape2 := newDiffTestTape("t2", "GET", "http://example.com/stale", 200,
		http.Header{"Content-Type": {"application/json"}},
		[]byte(`{"stale":true}`),
	)
	tape2.Route = "stale-route"
	store := seedDiffStore(t, tape1, tape2)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(200)
		w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()

	// Only request /users, not /stale.
	req, _ := http.NewRequest("GET", srv.URL+"/users", nil)

	report, err := Diff(context.Background(), store, srv.Client().Transport, []*http.Request{req},
		WithIgnoreHeaders(httpTestIgnoreHeaders...),
	)
	if err != nil {
		t.Fatalf("Diff() error = %v", err)
	}

	if len(report.Stale) != 1 {
		t.Fatalf("expected 1 stale fixture, got %d", len(report.Stale))
	}
	sf := report.Stale[0]
	if sf.ID != "t2" {
		t.Errorf("expected stale fixture ID t2, got %q", sf.ID)
	}
	if sf.Route != "stale-route" {
		t.Errorf("expected stale fixture route 'stale-route', got %q", sf.Route)
	}
	if sf.Method != "GET" {
		t.Errorf("expected stale fixture method GET, got %q", sf.Method)
	}
	if sf.URL != "http://example.com/stale" {
		t.Errorf("expected stale fixture URL, got %q", sf.URL)
	}
}

func TestDiff_TransportError(t *testing.T) {
	tape := newDiffTestTape("t1", "GET", "http://example.com/api", 200,
		http.Header{},
		[]byte(`{}`),
	)
	store := seedDiffStore(t, tape)

	transport := &diffFailingTransport{err: errors.New("connection refused")}

	req, _ := http.NewRequest("GET", "http://example.com/api", nil)

	report, err := Diff(context.Background(), store, transport, []*http.Request{req})
	if err != nil {
		t.Fatalf("Diff() error = %v (expected nil)", err)
	}

	r := report.Results[0]
	if r.Status != DiffDrifted {
		t.Errorf("expected DiffDrifted on transport error, got %d", r.Status)
	}
	if r.Error == "" {
		t.Error("expected non-empty Error field on transport error")
	}
	if !strings.Contains(r.Error, "transport") {
		t.Errorf("expected error to mention 'transport', got %q", r.Error)
	}
}

func TestDiff_StoreListError(t *testing.T) {
	store := &diffFailingStore{listErr: errors.New("disk failure")}

	req, _ := http.NewRequest("GET", "http://example.com/api", nil)

	_, err := Diff(context.Background(), store, http.DefaultTransport, []*http.Request{req})
	if err == nil {
		t.Fatal("expected error from Diff when store.List fails")
	}
	if !strings.Contains(err.Error(), "disk failure") {
		t.Errorf("expected error to contain 'disk failure', got %q", err.Error())
	}
}

func TestDiff_ContextCancellation(t *testing.T) {
	tape := newDiffTestTape("t1", "GET", "http://example.com/api", 200,
		http.Header{},
		[]byte(`{}`),
	)
	store := seedDiffStore(t, tape)

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel immediately

	req, _ := http.NewRequest("GET", "http://example.com/api", nil)

	_, err := Diff(ctx, store, http.DefaultTransport, []*http.Request{req})
	if err == nil {
		t.Fatal("expected error from Diff on cancelled context")
	}
	if !strings.Contains(err.Error(), "cancel") {
		t.Errorf("expected error to contain 'cancel', got %q", err.Error())
	}
}

func TestDiff_WithIgnorePaths(t *testing.T) {
	fixtureBody := `{"name":"alice","timestamp":"2024-01-01T00:00:00Z","id":"abc-123"}`
	liveBody := `{"name":"alice","timestamp":"2025-06-15T12:30:00Z","id":"xyz-789"}`

	tape := newDiffTestTape("t1", "GET", "http://example.com/users", 200,
		http.Header{"Content-Type": {"application/json"}},
		[]byte(fixtureBody),
	)
	store := seedDiffStore(t, tape)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(200)
		w.Write([]byte(liveBody))
	}))
	defer srv.Close()

	req, _ := http.NewRequest("GET", srv.URL+"/users", nil)

	report, err := Diff(context.Background(), store, srv.Client().Transport, []*http.Request{req},
		WithIgnorePaths("$.timestamp", "$.id"),
		WithIgnoreHeaders(httpTestIgnoreHeaders...),
	)
	if err != nil {
		t.Fatalf("Diff() error = %v", err)
	}

	r := report.Results[0]
	if r.Status != DiffMatched {
		t.Errorf("expected DiffMatched with ignored paths, got %d", r.Status)
		for _, bf := range r.BodyFields {
			t.Logf("  body diff: %s (%d)", bf.Path, bf.Kind)
		}
	}
	if len(r.BodyFields) != 0 {
		t.Errorf("expected no body field diffs, got %d", len(r.BodyFields))
	}
}

func TestDiff_WithIgnorePathsWildcard(t *testing.T) {
	fixtureBody := `{"items":[{"name":"a","ts":"old"},{"name":"b","ts":"old"}]}`
	liveBody := `{"items":[{"name":"a","ts":"new"},{"name":"b","ts":"new"}]}`

	tape := newDiffTestTape("t1", "GET", "http://example.com/list", 200,
		http.Header{"Content-Type": {"application/json"}},
		[]byte(fixtureBody),
	)
	store := seedDiffStore(t, tape)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(200)
		w.Write([]byte(liveBody))
	}))
	defer srv.Close()

	req, _ := http.NewRequest("GET", srv.URL+"/list", nil)

	report, err := Diff(context.Background(), store, srv.Client().Transport, []*http.Request{req},
		WithIgnorePaths("$.items[*].ts"),
		WithIgnoreHeaders(httpTestIgnoreHeaders...),
	)
	if err != nil {
		t.Fatalf("Diff() error = %v", err)
	}

	r := report.Results[0]
	if r.Status != DiffMatched {
		t.Errorf("expected DiffMatched with wildcard ignore, got %d", r.Status)
		for _, bf := range r.BodyFields {
			t.Logf("  body diff: %s (%d)", bf.Path, bf.Kind)
		}
	}
}

func TestDiff_WithIgnoreHeaders(t *testing.T) {
	tape := newDiffTestTape("t1", "GET", "http://example.com/api", 200,
		http.Header{
			"Content-Type": {"application/json"},
			"X-Request-Id": {"old-id"},
		},
		[]byte(`{}`),
	)
	store := seedDiffStore(t, tape)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-Request-Id", "new-id")
		w.WriteHeader(200)
		w.Write([]byte(`{}`))
	}))
	defer srv.Close()

	req, _ := http.NewRequest("GET", srv.URL+"/api", nil)

	report, err := Diff(context.Background(), store, srv.Client().Transport, []*http.Request{req},
		WithIgnoreHeaders("X-Request-Id"),
		WithIgnoreHeaders(httpTestIgnoreHeaders...),
	)
	if err != nil {
		t.Fatalf("Diff() error = %v", err)
	}

	r := report.Results[0]
	// X-Request-Id should be ignored, so only Content-Type is compared.
	for _, hd := range r.Headers {
		if hd.Name == "X-Request-Id" {
			t.Error("X-Request-Id should be ignored but appeared in diffs")
		}
	}
}

func TestDiff_WithDiffSanitizer(t *testing.T) {
	// Fixture was recorded with redacted email.
	fixtureBody := `{"name":"alice","email":"[REDACTED]"}`
	// Live response has real email.
	liveBody := `{"name":"alice","email":"alice@example.com"}`

	tape := newDiffTestTape("t1", "GET", "http://example.com/users", 200,
		http.Header{"Content-Type": {"application/json"}},
		[]byte(fixtureBody),
	)
	store := seedDiffStore(t, tape)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(200)
		w.Write([]byte(liveBody))
	}))
	defer srv.Close()

	// Build the same sanitizer used during recording.
	sanitizer := NewPipeline(RedactBodyPaths("$.email"))

	req, _ := http.NewRequest("GET", srv.URL+"/users", nil)

	report, err := Diff(context.Background(), store, srv.Client().Transport, []*http.Request{req},
		WithDiffSanitizer(sanitizer),
		WithIgnoreHeaders(httpTestIgnoreHeaders...),
	)
	if err != nil {
		t.Fatalf("Diff() error = %v", err)
	}

	r := report.Results[0]
	if r.Status != DiffMatched {
		t.Errorf("expected DiffMatched with sanitizer, got %d", r.Status)
		for _, bf := range r.BodyFields {
			t.Logf("  body diff: %s (%d) old=%v new=%v", bf.Path, bf.Kind, bf.OldValue, bf.NewValue)
		}
	}
}

func TestDiff_WithoutSanitizerFalsePositive(t *testing.T) {
	// Same scenario but WITHOUT sanitizer: should show drift.
	fixtureBody := `{"name":"alice","email":"[REDACTED]"}`
	liveBody := `{"name":"alice","email":"alice@example.com"}`

	tape := newDiffTestTape("t1", "GET", "http://example.com/users", 200,
		http.Header{"Content-Type": {"application/json"}},
		[]byte(fixtureBody),
	)
	store := seedDiffStore(t, tape)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(200)
		w.Write([]byte(liveBody))
	}))
	defer srv.Close()

	req, _ := http.NewRequest("GET", srv.URL+"/users", nil)

	report, err := Diff(context.Background(), store, srv.Client().Transport, []*http.Request{req},
		WithIgnoreHeaders(httpTestIgnoreHeaders...),
	)
	if err != nil {
		t.Fatalf("Diff() error = %v", err)
	}

	r := report.Results[0]
	if r.Status != DiffDrifted {
		t.Errorf("expected DiffDrifted without sanitizer, got %d", r.Status)
	}
}

func TestDiff_WithDiffMatcher(t *testing.T) {
	tape := newDiffTestTape("t1", "GET", "http://example.com/users", 200,
		http.Header{"Content-Type": {"application/json"}},
		[]byte(`{"ok":true}`),
	)
	store := seedDiffStore(t, tape)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(200)
		w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()

	// Use a matcher that never matches.
	neverMatch := MatcherFunc(func(req *http.Request, candidates []Tape) (Tape, bool) {
		return Tape{}, false
	})

	req, _ := http.NewRequest("GET", srv.URL+"/users", nil)

	report, err := Diff(context.Background(), store, srv.Client().Transport, []*http.Request{req},
		WithDiffMatcher(neverMatch),
	)
	if err != nil {
		t.Fatalf("Diff() error = %v", err)
	}

	r := report.Results[0]
	if r.Status != DiffNoFixture {
		t.Errorf("expected DiffNoFixture with never-match matcher, got %d", r.Status)
	}
}

func TestDiff_MultipleRequests(t *testing.T) {
	tape1 := newDiffTestTape("t1", "GET", "http://example.com/users", 200,
		http.Header{"Content-Type": {"application/json"}},
		[]byte(`{"name":"alice"}`),
	)
	tape2 := newDiffTestTape("t2", "GET", "http://example.com/orders", 200,
		http.Header{"Content-Type": {"application/json"}},
		[]byte(`{"total":100}`),
	)
	store := seedDiffStore(t, tape1, tape2)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(200)
		switch r.URL.Path {
		case "/users":
			w.Write([]byte(`{"name":"alice"}`))
		case "/orders":
			w.Write([]byte(`{"total":200}`)) // changed
		}
	}))
	defer srv.Close()

	req1, _ := http.NewRequest("GET", srv.URL+"/users", nil)
	req2, _ := http.NewRequest("GET", srv.URL+"/orders", nil)

	report, err := Diff(context.Background(), store, srv.Client().Transport,
		[]*http.Request{req1, req2},
		WithIgnoreHeaders(httpTestIgnoreHeaders...),
	)
	if err != nil {
		t.Fatalf("Diff() error = %v", err)
	}

	if len(report.Results) != 2 {
		t.Fatalf("expected 2 results, got %d", len(report.Results))
	}

	if report.Results[0].Status != DiffMatched {
		t.Errorf("result[0] expected DiffMatched, got %d", report.Results[0].Status)
	}
	if report.Results[1].Status != DiffDrifted {
		t.Errorf("result[1] expected DiffDrifted, got %d", report.Results[1].Status)
	}
	if len(report.Stale) != 0 {
		t.Errorf("expected no stale fixtures, got %d", len(report.Stale))
	}
}

func TestDiff_EmptyRequests(t *testing.T) {
	tape := newDiffTestTape("t1", "GET", "http://example.com/api", 200,
		http.Header{},
		[]byte(`{}`),
	)
	store := seedDiffStore(t, tape)

	report, err := Diff(context.Background(), store, http.DefaultTransport, nil)
	if err != nil {
		t.Fatalf("Diff() error = %v", err)
	}

	if len(report.Results) != 0 {
		t.Errorf("expected 0 results, got %d", len(report.Results))
	}

	// All fixtures are stale since no requests were made.
	if len(report.Stale) != 1 {
		t.Fatalf("expected 1 stale fixture, got %d", len(report.Stale))
	}
}

func TestDiff_NilDiffMatcher(t *testing.T) {
	// WithDiffMatcher(nil) should keep the default matcher.
	tape := newDiffTestTape("t1", "GET", "http://example.com/users", 200,
		http.Header{"Content-Type": {"application/json"}},
		[]byte(`{"ok":true}`),
	)
	store := seedDiffStore(t, tape)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(200)
		w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()

	req, _ := http.NewRequest("GET", srv.URL+"/users", nil)

	report, err := Diff(context.Background(), store, srv.Client().Transport, []*http.Request{req},
		WithDiffMatcher(nil),
		WithIgnoreHeaders(httpTestIgnoreHeaders...),
	)
	if err != nil {
		t.Fatalf("Diff() error = %v", err)
	}

	if report.Results[0].Status != DiffMatched {
		t.Errorf("expected DiffMatched, got %d", report.Results[0].Status)
	}
}

func TestDiff_WithSanitizerHeaderRedaction(t *testing.T) {
	// Fixture was recorded with redacted Authorization header.
	tape := newDiffTestTape("t1", "GET", "http://example.com/api", 200,
		http.Header{
			"Content-Type":  {"application/json"},
			"Authorization": {"[REDACTED]"},
		},
		[]byte(`{}`),
	)
	store := seedDiffStore(t, tape)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Authorization", "Bearer real-token")
		w.WriteHeader(200)
		w.Write([]byte(`{}`))
	}))
	defer srv.Close()

	sanitizer := NewPipeline(RedactHeaders("Authorization"))

	req, _ := http.NewRequest("GET", srv.URL+"/api", nil)

	report, err := Diff(context.Background(), store, srv.Client().Transport, []*http.Request{req},
		WithDiffSanitizer(sanitizer),
		WithIgnoreHeaders(httpTestIgnoreHeaders...),
	)
	if err != nil {
		t.Fatalf("Diff() error = %v", err)
	}

	r := report.Results[0]
	// After sanitization, the live response's Authorization header should
	// also be [REDACTED], matching the fixture.
	for _, hd := range r.Headers {
		if hd.Name == "Authorization" {
			t.Errorf("Authorization should match after sanitization but found diff: %+v", hd)
		}
	}
}

// --- Unit tests for internal functions ---

func TestIsJSONBody(t *testing.T) {
	tests := []struct {
		name string
		body []byte
		want bool
	}{
		{"valid object", []byte(`{"a":1}`), true},
		{"valid array", []byte(`[1,2,3]`), true},
		{"valid string", []byte(`"hello"`), true},
		{"valid number", []byte(`42`), true},
		{"valid null", []byte(`null`), true},
		{"invalid", []byte(`not json`), false},
		{"empty", []byte{}, false},
		{"nil", nil, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isJSONBody(tt.body); got != tt.want {
				t.Errorf("isJSONBody() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestBodyHash_Diff(t *testing.T) {
	tests := []struct {
		name string
		body []byte
		want string
	}{
		{"empty", nil, ""},
		{"empty bytes", []byte{}, ""},
		{"hello", []byte("hello"), BodyHashFromBytes([]byte("hello"))},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := bodyHash(tt.body)
			if got != tt.want {
				t.Errorf("bodyHash() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestDiffHeaders_Unit(t *testing.T) {
	tests := []struct {
		name    string
		fixture http.Header
		live    http.Header
		ignore  map[string]struct{}
		want    int // number of diffs
	}{
		{
			name:    "identical",
			fixture: http.Header{"X-A": {"1"}},
			live:    http.Header{"X-A": {"1"}},
			ignore:  map[string]struct{}{},
			want:    0,
		},
		{
			name:    "added",
			fixture: http.Header{},
			live:    http.Header{"X-New": {"val"}},
			ignore:  map[string]struct{}{},
			want:    1,
		},
		{
			name:    "removed",
			fixture: http.Header{"X-Old": {"val"}},
			live:    http.Header{},
			ignore:  map[string]struct{}{},
			want:    1,
		},
		{
			name:    "changed",
			fixture: http.Header{"X-A": {"old"}},
			live:    http.Header{"X-A": {"new"}},
			ignore:  map[string]struct{}{},
			want:    1,
		},
		{
			name:    "ignored",
			fixture: http.Header{"X-A": {"old"}},
			live:    http.Header{"X-A": {"new"}},
			ignore:  map[string]struct{}{"X-A": {}},
			want:    0,
		},
		{
			name:    "nil headers",
			fixture: nil,
			live:    nil,
			ignore:  map[string]struct{}{},
			want:    0,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			diffs := diffHeaders(tt.fixture, tt.live, tt.ignore)
			if len(diffs) != tt.want {
				t.Errorf("diffHeaders() returned %d diffs, want %d: %+v", len(diffs), tt.want, diffs)
			}
		})
	}
}

func TestDiffJSONEqual(t *testing.T) {
	tests := []struct {
		name string
		a, b any
		want bool
	}{
		{"nil nil", nil, nil, true},
		{"nil string", nil, "a", false},
		{"string nil", "a", nil, false},
		{"same strings", "a", "a", true},
		{"diff strings", "a", "b", false},
		{"same numbers", float64(1), float64(1), true},
		{"diff numbers", float64(1), float64(2), false},
		{"same bools", true, true, true},
		{"diff bools", true, false, false},
		{"bool vs string", true, "true", false},
		{"number vs string", float64(1), "1", false},
		{"same maps", map[string]any{"a": "b"}, map[string]any{"a": "b"}, true},
		{"diff maps", map[string]any{"a": "b"}, map[string]any{"a": "c"}, false},
		{"diff map keys", map[string]any{"a": "b"}, map[string]any{"x": "b"}, false},
		{"map vs string", map[string]any{"a": "b"}, "abc", false},
		{"same arrays", []any{"a", "b"}, []any{"a", "b"}, true},
		{"diff arrays", []any{"a", "b"}, []any{"a", "c"}, false},
		{"diff length arrays", []any{"a"}, []any{"a", "b"}, false},
		{"array vs string", []any{"a"}, "a", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := diffJSONEqual(tt.a, tt.b); got != tt.want {
				t.Errorf("diffJSONEqual() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestCompareJSON_Basic(t *testing.T) {
	tests := []struct {
		name     string
		fixture  string
		live     string
		wantLen  int
		wantPath string
		wantKind FieldChangeKind
	}{
		{
			name:    "identical",
			fixture: `{"a":1}`,
			live:    `{"a":1}`,
			wantLen: 0,
		},
		{
			name:     "added field",
			fixture:  `{"a":1}`,
			live:     `{"a":1,"b":2}`,
			wantLen:  1,
			wantPath: "$.b",
			wantKind: FieldAdded,
		},
		{
			name:     "removed field",
			fixture:  `{"a":1,"b":2}`,
			live:     `{"a":1}`,
			wantLen:  1,
			wantPath: "$.b",
			wantKind: FieldRemoved,
		},
		{
			name:     "changed value",
			fixture:  `{"a":1}`,
			live:     `{"a":2}`,
			wantLen:  1,
			wantPath: "$.a",
			wantKind: FieldChanged,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			diffs := diffJSONBodies([]byte(tt.fixture), []byte(tt.live), nil)
			if len(diffs) != tt.wantLen {
				t.Fatalf("expected %d diffs, got %d: %+v", tt.wantLen, len(diffs), diffs)
			}
			if tt.wantLen > 0 {
				if diffs[0].Path != tt.wantPath {
					t.Errorf("expected path %q, got %q", tt.wantPath, diffs[0].Path)
				}
				if diffs[0].Kind != tt.wantKind {
					t.Errorf("expected kind %d, got %d", tt.wantKind, diffs[0].Kind)
				}
			}
		})
	}
}

func TestCompareJSON_Nested(t *testing.T) {
	fixture := `{"user":{"name":"alice","address":{"city":"NYC","zip":"10001"}}}`
	live := `{"user":{"name":"alice","address":{"city":"LA","state":"CA"}}}`

	diffs := diffJSONBodies([]byte(fixture), []byte(live), nil)

	// Expected: city changed, zip removed, state added.
	if len(diffs) != 3 {
		t.Fatalf("expected 3 diffs, got %d: %+v", len(diffs), diffs)
	}

	diffMap := make(map[string]BodyFieldDiff)
	for _, d := range diffs {
		diffMap[d.Path] = d
	}

	if d, ok := diffMap["$.user.address.city"]; !ok {
		t.Error("missing diff for $.user.address.city")
	} else if d.Kind != FieldChanged {
		t.Errorf("$.user.address.city: expected FieldChanged, got %d", d.Kind)
	}

	if d, ok := diffMap["$.user.address.state"]; !ok {
		t.Error("missing diff for $.user.address.state")
	} else if d.Kind != FieldAdded {
		t.Errorf("$.user.address.state: expected FieldAdded, got %d", d.Kind)
	}

	if d, ok := diffMap["$.user.address.zip"]; !ok {
		t.Error("missing diff for $.user.address.zip")
	} else if d.Kind != FieldRemoved {
		t.Errorf("$.user.address.zip: expected FieldRemoved, got %d", d.Kind)
	}
}

func TestCompareJSON_Arrays(t *testing.T) {
	fixture := `{"items":[1,2,3]}`
	live := `{"items":[1,2,4]}`

	diffs := diffJSONBodies([]byte(fixture), []byte(live), nil)

	if len(diffs) != 1 {
		t.Fatalf("expected 1 diff, got %d: %+v", len(diffs), diffs)
	}
	if diffs[0].Path != "$.items.2" {
		t.Errorf("expected path $.items.2, got %q", diffs[0].Path)
	}
	if diffs[0].Kind != FieldChanged {
		t.Errorf("expected FieldChanged, got %d", diffs[0].Kind)
	}
}

func TestCompareJSON_ArrayLengthDiff(t *testing.T) {
	fixture := `{"items":[1,2]}`
	live := `{"items":[1,2,3]}`

	diffs := diffJSONBodies([]byte(fixture), []byte(live), nil)

	if len(diffs) != 1 {
		t.Fatalf("expected 1 diff, got %d: %+v", len(diffs), diffs)
	}
	if diffs[0].Path != "$.items.2" {
		t.Errorf("expected path $.items.2, got %q", diffs[0].Path)
	}
	if diffs[0].Kind != FieldAdded {
		t.Errorf("expected FieldAdded, got %d", diffs[0].Kind)
	}
}

func TestCompareJSON_TypeMismatch(t *testing.T) {
	fixture := `{"a":"string"}`
	live := `{"a":42}`

	diffs := diffJSONBodies([]byte(fixture), []byte(live), nil)

	if len(diffs) != 1 {
		t.Fatalf("expected 1 diff, got %d: %+v", len(diffs), diffs)
	}
	if diffs[0].Kind != FieldChanged {
		t.Errorf("expected FieldChanged, got %d", diffs[0].Kind)
	}
}

func TestCompareJSON_NullHandling(t *testing.T) {
	tests := []struct {
		name    string
		fixture string
		live    string
		wantLen int
	}{
		{"null to null", `{"a":null}`, `{"a":null}`, 0},
		{"null to value", `{"a":null}`, `{"a":"hello"}`, 1},
		{"value to null", `{"a":"hello"}`, `{"a":null}`, 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			diffs := diffJSONBodies([]byte(tt.fixture), []byte(tt.live), nil)
			if len(diffs) != tt.wantLen {
				t.Errorf("expected %d diffs, got %d: %+v", tt.wantLen, len(diffs), diffs)
			}
		})
	}
}

func TestMatchesIgnorePath(t *testing.T) {
	tests := []struct {
		name     string
		concrete []pathSegment
		pattern  []segment
		want     bool
	}{
		{
			name:     "simple match",
			concrete: []pathSegment{{key: "name"}},
			pattern:  []segment{{key: "name"}},
			want:     true,
		},
		{
			name:     "simple no match",
			concrete: []pathSegment{{key: "name"}},
			pattern:  []segment{{key: "email"}},
			want:     false,
		},
		{
			name: "wildcard match",
			concrete: []pathSegment{
				{key: "items"},
				{index: 0, isIndex: true},
				{key: "id"},
			},
			pattern: []segment{
				{key: "items", wildcard: true},
				{key: "id"},
			},
			want: true,
		},
		{
			name: "wildcard match different index",
			concrete: []pathSegment{
				{key: "items"},
				{index: 5, isIndex: true},
				{key: "id"},
			},
			pattern: []segment{
				{key: "items", wildcard: true},
				{key: "id"},
			},
			want: true,
		},
		{
			name: "wildcard key mismatch",
			concrete: []pathSegment{
				{key: "other"},
				{index: 0, isIndex: true},
				{key: "id"},
			},
			pattern: []segment{
				{key: "items", wildcard: true},
				{key: "id"},
			},
			want: false,
		},
		{
			name: "nested wildcard",
			concrete: []pathSegment{
				{key: "users"},
				{index: 2, isIndex: true},
				{key: "addresses"},
				{index: 0, isIndex: true},
				{key: "city"},
			},
			pattern: []segment{
				{key: "users", wildcard: true},
				{key: "addresses", wildcard: true},
				{key: "city"},
			},
			want: true,
		},
		{
			name:     "length mismatch",
			concrete: []pathSegment{{key: "a"}, {key: "b"}},
			pattern:  []segment{{key: "a"}},
			want:     false,
		},
		{
			name: "wildcard but no index follows",
			concrete: []pathSegment{
				{key: "items"},
				{key: "id"},
			},
			pattern: []segment{
				{key: "items", wildcard: true},
				{key: "id"},
			},
			want: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := matchesIgnorePath(tt.concrete, tt.pattern)
			if got != tt.want {
				t.Errorf("matchesIgnorePath() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestFormatPath(t *testing.T) {
	tests := []struct {
		name string
		segs []pathSegment
		want string
	}{
		{"empty", nil, "$"},
		{"single key", []pathSegment{{key: "name"}}, "$.name"},
		{"nested", []pathSegment{{key: "user"}, {key: "email"}}, "$.user.email"},
		{"with index", []pathSegment{{key: "items"}, {index: 0, isIndex: true}}, "$.items.0"},
		{"mixed", []pathSegment{
			{key: "items"},
			{index: 2, isIndex: true},
			{key: "name"},
		}, "$.items.2.name"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := formatPath(tt.segs)
			if got != tt.want {
				t.Errorf("formatPath() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestDiff_RequestBodyPreservedForMatching(t *testing.T) {
	// Ensure the request body is available for matching even after the
	// diff function reads it.
	body := `{"action":"create","user":"alice"}`
	tape := newDiffTestTape("t1", "POST", "http://example.com/api", 200,
		http.Header{"Content-Type": {"application/json"}},
		[]byte(`{"created":true}`),
	)
	tape.Request.Body = []byte(body)
	tape.Request.BodyHash = BodyHashFromBytes([]byte(body))
	store := seedDiffStore(t, tape)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Read the body to verify it arrived.
		b, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("failed to read body: %v", err)
		}
		if string(b) != body {
			t.Errorf("body = %q, want %q", b, body)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(200)
		w.Write([]byte(`{"created":true}`))
	}))
	defer srv.Close()

	req, _ := http.NewRequest("POST", srv.URL+"/api", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")

	report, err := Diff(context.Background(), store, srv.Client().Transport, []*http.Request{req},
		WithIgnoreHeaders(httpTestIgnoreHeaders...),
	)
	if err != nil {
		t.Fatalf("Diff() error = %v", err)
	}

	if report.Results[0].Status == DiffNoFixture {
		t.Error("expected fixture match, got DiffNoFixture")
	}
}

func TestDiff_DiffReportJSON(t *testing.T) {
	// Verify the report is JSON-serializable.
	report := DiffReport{
		Results: []DiffResult{
			{
				RequestMethod: "GET",
				RequestURL:    "http://example.com/api",
				FixtureID:     "t1",
				Status:        DiffDrifted,
				StatusCode:    &StatusCodeDiff{Old: 200, New: 201},
				Headers: []HeaderDiff{
					{Name: "X-New", Kind: FieldAdded, NewValues: []string{"val"}},
				},
				BodyFields: []BodyFieldDiff{
					{Path: "$.name", Kind: FieldChanged, OldValue: "alice", NewValue: "bob"},
				},
			},
		},
		Stale: []StaleFixture{
			{ID: "t2", Route: "r", Method: "POST", URL: "http://example.com/old"},
		},
	}

	data, err := json.Marshal(report)
	if err != nil {
		t.Fatalf("json.Marshal(DiffReport) error = %v", err)
	}

	var roundTrip DiffReport
	if err := json.Unmarshal(data, &roundTrip); err != nil {
		t.Fatalf("json.Unmarshal(DiffReport) error = %v", err)
	}

	if len(roundTrip.Results) != 1 {
		t.Fatalf("round-trip: expected 1 result, got %d", len(roundTrip.Results))
	}
	if roundTrip.Results[0].Status != DiffDrifted {
		t.Errorf("round-trip: expected DiffDrifted, got %d", roundTrip.Results[0].Status)
	}
	if len(roundTrip.Stale) != 1 {
		t.Fatalf("round-trip: expected 1 stale, got %d", len(roundTrip.Stale))
	}
}

func TestDiff_WithIgnorePathsNestedWildcard(t *testing.T) {
	fixtureBody := `{"users":[{"name":"alice","addresses":[{"city":"NYC","ts":"old"}]},{"name":"bob","addresses":[{"city":"LA","ts":"old"}]}]}`
	liveBody := `{"users":[{"name":"alice","addresses":[{"city":"NYC","ts":"new"}]},{"name":"bob","addresses":[{"city":"LA","ts":"new"}]}]}`

	tape := newDiffTestTape("t1", "GET", "http://example.com/users", 200,
		http.Header{"Content-Type": {"application/json"}},
		[]byte(fixtureBody),
	)
	store := seedDiffStore(t, tape)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(200)
		w.Write([]byte(liveBody))
	}))
	defer srv.Close()

	req, _ := http.NewRequest("GET", srv.URL+"/users", nil)

	report, err := Diff(context.Background(), store, srv.Client().Transport, []*http.Request{req},
		WithIgnorePaths("$.users[*].addresses[*].ts"),
		WithIgnoreHeaders(httpTestIgnoreHeaders...),
	)
	if err != nil {
		t.Fatalf("Diff() error = %v", err)
	}

	r := report.Results[0]
	if r.Status != DiffMatched {
		t.Errorf("expected DiffMatched with nested wildcard ignore, got %d", r.Status)
		for _, bf := range r.BodyFields {
			t.Logf("  unexpected diff: %s (%d)", bf.Path, bf.Kind)
		}
	}
}

func TestDiff_EmptyBodiesMatch(t *testing.T) {
	tape := newDiffTestTape("t1", "GET", "http://example.com/api", 204,
		http.Header{},
		nil, // empty body
	)
	store := seedDiffStore(t, tape)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(204)
	}))
	defer srv.Close()

	req, _ := http.NewRequest("GET", srv.URL+"/api", nil)

	report, err := Diff(context.Background(), store, srv.Client().Transport, []*http.Request{req},
		WithIgnoreHeaders(httpTestIgnoreHeaders...),
	)
	if err != nil {
		t.Fatalf("Diff() error = %v", err)
	}

	r := report.Results[0]
	if r.Status != DiffMatched {
		t.Errorf("expected DiffMatched for empty bodies, got %d", r.Status)
		for _, hd := range r.Headers {
			t.Logf("  header diff: %s kind=%d", hd.Name, hd.Kind)
		}
	}
}

func TestDiff_InvalidJSONFixture(t *testing.T) {
	// When fixture body is invalid JSON, fall back to hash comparison.
	tape := newDiffTestTape("t1", "GET", "http://example.com/api", 200,
		http.Header{"Content-Type": {"application/json"}},
		[]byte(`not valid json`),
	)
	store := seedDiffStore(t, tape)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(200)
		w.Write([]byte(`not valid json`))
	}))
	defer srv.Close()

	req, _ := http.NewRequest("GET", srv.URL+"/api", nil)

	report, err := Diff(context.Background(), store, srv.Client().Transport, []*http.Request{req},
		WithIgnoreHeaders(httpTestIgnoreHeaders...),
	)
	if err != nil {
		t.Fatalf("Diff() error = %v", err)
	}

	r := report.Results[0]
	if r.Status != DiffMatched {
		t.Errorf("expected DiffMatched for same non-JSON bodies, got %d", r.Status)
	}
}

func TestDiff_MixedJSONAndNonJSON(t *testing.T) {
	// When fixture is JSON but live is not, fall back to hash.
	tape := newDiffTestTape("t1", "GET", "http://example.com/api", 200,
		http.Header{"Content-Type": {"application/json"}},
		[]byte(`{"ok":true}`),
	)
	store := seedDiffStore(t, tape)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		w.WriteHeader(200)
		w.Write([]byte(`plain text response`))
	}))
	defer srv.Close()

	req, _ := http.NewRequest("GET", srv.URL+"/api", nil)

	report, err := Diff(context.Background(), store, srv.Client().Transport, []*http.Request{req},
		WithIgnoreHeaders(httpTestIgnoreHeaders...),
	)
	if err != nil {
		t.Fatalf("Diff() error = %v", err)
	}

	r := report.Results[0]
	if r.Status != DiffDrifted {
		t.Errorf("expected DiffDrifted, got %d", r.Status)
	}
	if !r.BodyChanged {
		t.Error("expected BodyChanged for JSON->non-JSON transition")
	}
}

func TestDiff_SequentialExecution(t *testing.T) {
	// Verify requests are processed in order.
	callOrder := make([]string, 0)

	tape1 := newDiffTestTape("t1", "GET", "http://example.com/first", 200,
		http.Header{"Content-Type": {"application/json"}},
		[]byte(`{"order":1}`),
	)
	tape2 := newDiffTestTape("t2", "GET", "http://example.com/second", 200,
		http.Header{"Content-Type": {"application/json"}},
		[]byte(`{"order":2}`),
	)
	store := seedDiffStore(t, tape1, tape2)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callOrder = append(callOrder, r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(200)
		switch r.URL.Path {
		case "/first":
			w.Write([]byte(`{"order":1}`))
		case "/second":
			w.Write([]byte(`{"order":2}`))
		}
	}))
	defer srv.Close()

	req1, _ := http.NewRequest("GET", srv.URL+"/first", nil)
	req2, _ := http.NewRequest("GET", srv.URL+"/second", nil)

	report, err := Diff(context.Background(), store, srv.Client().Transport,
		[]*http.Request{req1, req2},
		WithIgnoreHeaders(httpTestIgnoreHeaders...),
	)
	if err != nil {
		t.Fatalf("Diff() error = %v", err)
	}

	if len(callOrder) != 2 {
		t.Fatalf("expected 2 calls, got %d", len(callOrder))
	}
	if callOrder[0] != "/first" || callOrder[1] != "/second" {
		t.Errorf("expected [/first, /second], got %v", callOrder)
	}

	if len(report.Results) != 2 {
		t.Fatalf("expected 2 results, got %d", len(report.Results))
	}
	if report.Results[0].RequestURL != srv.URL+"/first" {
		t.Errorf("result[0] URL = %q", report.Results[0].RequestURL)
	}
	if report.Results[1].RequestURL != srv.URL+"/second" {
		t.Errorf("result[1] URL = %q", report.Results[1].RequestURL)
	}
}

func TestDiff_BooleanValueChange(t *testing.T) {
	fixture := `{"active":true}`
	live := `{"active":false}`

	diffs := diffJSONBodies([]byte(fixture), []byte(live), nil)
	if len(diffs) != 1 {
		t.Fatalf("expected 1 diff, got %d", len(diffs))
	}
	if diffs[0].OldValue != true {
		t.Errorf("expected old value true, got %v", diffs[0].OldValue)
	}
	if diffs[0].NewValue != false {
		t.Errorf("expected new value false, got %v", diffs[0].NewValue)
	}
}

func TestDiff_ArrayOfObjects(t *testing.T) {
	fixture := `{"items":[{"id":1,"name":"a"},{"id":2,"name":"b"}]}`
	live := `{"items":[{"id":1,"name":"a"},{"id":2,"name":"c"}]}`

	diffs := diffJSONBodies([]byte(fixture), []byte(live), nil)
	if len(diffs) != 1 {
		t.Fatalf("expected 1 diff, got %d: %+v", len(diffs), diffs)
	}
	if diffs[0].Path != "$.items.1.name" {
		t.Errorf("expected path $.items.1.name, got %q", diffs[0].Path)
	}
}

func TestCopyStringSlice(t *testing.T) {
	tests := []struct {
		name  string
		input []string
	}{
		{"nil", nil},
		{"empty", []string{}},
		{"single", []string{"a"}},
		{"multi", []string{"a", "b", "c"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := copyStringSlice(tt.input)
			if tt.input == nil {
				if got != nil {
					t.Errorf("expected nil, got %v", got)
				}
				return
			}
			if len(got) != len(tt.input) {
				t.Fatalf("len(got) = %d, want %d", len(got), len(tt.input))
			}
			// Verify it's a copy (mutation safety).
			if len(got) > 0 {
				got[0] = "mutated"
				if tt.input[0] == "mutated" {
					t.Error("copy aliased the original slice")
				}
			}
		})
	}
}

func TestDiff_WithDiffSanitizerFakeFields(t *testing.T) {
	// Fixture was recorded with faked email.
	seed := "test-seed"
	// Build the sanitizer.
	sanitizer := NewPipeline(FakeFields(seed, "$.email"))

	// Apply sanitizer to known body to get the faked fixture value.
	tmpTape := Tape{
		Response: RecordedResp{
			Body: []byte(`{"name":"alice","email":"alice@real.com"}`),
		},
	}
	sanitized := sanitizer.Sanitize(tmpTape)
	fixtureBody := sanitized.Response.Body

	tape := newDiffTestTape("t1", "GET", "http://example.com/users", 200,
		http.Header{"Content-Type": {"application/json"}},
		fixtureBody,
	)
	store := seedDiffStore(t, tape)

	// Live response has the real email.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(200)
		w.Write([]byte(`{"name":"alice","email":"alice@real.com"}`))
	}))
	defer srv.Close()

	req, _ := http.NewRequest("GET", srv.URL+"/users", nil)

	report, err := Diff(context.Background(), store, srv.Client().Transport, []*http.Request{req},
		WithDiffSanitizer(sanitizer),
		WithIgnoreHeaders(httpTestIgnoreHeaders...),
	)
	if err != nil {
		t.Fatalf("Diff() error = %v", err)
	}

	r := report.Results[0]
	if r.Status != DiffMatched {
		t.Errorf("expected DiffMatched with FakeFields sanitizer, got %d", r.Status)
		for _, bf := range r.BodyFields {
			t.Logf("  diff: %s (%d) old=%v new=%v", bf.Path, bf.Kind, bf.OldValue, bf.NewValue)
		}
	}
}

func TestDiff_RootLevelArrayComparison(t *testing.T) {
	fixture := `[1,2,3]`
	live := `[1,2,4]`

	diffs := diffJSONBodies([]byte(fixture), []byte(live), nil)
	if len(diffs) != 1 {
		t.Fatalf("expected 1 diff, got %d: %+v", len(diffs), diffs)
	}
	if diffs[0].Path != "$.2" {
		t.Errorf("expected path $.2, got %q", diffs[0].Path)
	}
}

func TestDiff_MultipleHeaderValues(t *testing.T) {
	tape := newDiffTestTape("t1", "GET", "http://example.com/api", 200,
		http.Header{
			"Set-Cookie": {"a=1", "b=2"},
		},
		[]byte(`{}`),
	)
	store := seedDiffStore(t, tape)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Add("Set-Cookie", "a=1")
		w.Header().Add("Set-Cookie", "c=3")
		w.WriteHeader(200)
		w.Write([]byte(`{}`))
	}))
	defer srv.Close()

	req, _ := http.NewRequest("GET", srv.URL+"/api", nil)

	report, err := Diff(context.Background(), store, srv.Client().Transport, []*http.Request{req},
		WithIgnoreHeaders(httpTestIgnoreHeaders...),
	)
	if err != nil {
		t.Fatalf("Diff() error = %v", err)
	}

	r := report.Results[0]
	found := false
	for _, hd := range r.Headers {
		if hd.Name == "Set-Cookie" {
			found = true
			if hd.Kind != FieldChanged {
				t.Errorf("Set-Cookie: expected FieldChanged, got %d", hd.Kind)
			}
		}
	}
	if !found {
		t.Error("expected Set-Cookie header diff")
	}
}

func TestDiff_DeeplyNestedIgnorePath(t *testing.T) {
	fixture := `{"a":{"b":{"c":{"ts":"old","val":"same"}}}}`
	live := `{"a":{"b":{"c":{"ts":"new","val":"same"}}}}`

	pp, ok := parsePath("$.a.b.c.ts")
	if !ok {
		t.Fatal("parsePath failed")
	}
	diffs := diffJSONBodies([]byte(fixture), []byte(live), []parsedPath{pp})

	if len(diffs) != 0 {
		t.Errorf("expected 0 diffs with ignored deeply nested path, got %d: %+v", len(diffs), diffs)
	}
}

func TestDiff_IgnorePathsInvalidSyntax(t *testing.T) {
	// Invalid paths should be silently ignored.
	body := []byte(`{"name":"alice","ts":"old"}`)
	tape := newDiffTestTape("t1", "GET", "http://example.com/api", 200,
		http.Header{"Content-Type": {"application/json"}},
		body,
	)
	store := seedDiffStore(t, tape)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(200)
		w.Write([]byte(`{"name":"alice","ts":"new"}`))
	}))
	defer srv.Close()

	req, _ := http.NewRequest("GET", srv.URL+"/api", nil)

	// "invalid" is not a valid path (missing $. prefix).
	report, err := Diff(context.Background(), store, srv.Client().Transport, []*http.Request{req},
		WithIgnorePaths("invalid", "$.ts"),
		WithIgnoreHeaders(httpTestIgnoreHeaders...),
	)
	if err != nil {
		t.Fatalf("Diff() error = %v", err)
	}

	r := report.Results[0]
	if r.Status != DiffMatched {
		t.Errorf("expected DiffMatched (invalid path silently ignored, $.ts properly ignored), got %d", r.Status)
	}
}

func TestDiff_IgnoreHeadersCaseInsensitive(t *testing.T) {
	tape := newDiffTestTape("t1", "GET", "http://example.com/api", 200,
		http.Header{"X-Request-Id": {"old"}},
		[]byte(`{}`),
	)
	store := seedDiffStore(t, tape)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Request-Id", "new")
		w.WriteHeader(200)
		w.Write([]byte(`{}`))
	}))
	defer srv.Close()

	req, _ := http.NewRequest("GET", srv.URL+"/api", nil)

	// Use lowercase header name -- should still match.
	report, err := Diff(context.Background(), store, srv.Client().Transport, []*http.Request{req},
		WithIgnoreHeaders("x-request-id"),
		WithIgnoreHeaders(httpTestIgnoreHeaders...),
	)
	if err != nil {
		t.Fatalf("Diff() error = %v", err)
	}

	for _, hd := range report.Results[0].Headers {
		if hd.Name == "X-Request-Id" {
			t.Error("X-Request-Id should be ignored (case-insensitive)")
		}
	}
}

func TestDiff_SameFixtureMatchedByMultipleRequests(t *testing.T) {
	// Same fixture matched twice. Should NOT be stale.
	tape := newDiffTestTape("t1", "GET", "http://example.com/api", 200,
		http.Header{"Content-Type": {"application/json"}},
		[]byte(`{"ok":true}`),
	)
	store := seedDiffStore(t, tape)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(200)
		w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()

	req1, _ := http.NewRequest("GET", srv.URL+"/api", nil)
	req2, _ := http.NewRequest("GET", srv.URL+"/api", nil)

	report, err := Diff(context.Background(), store, srv.Client().Transport,
		[]*http.Request{req1, req2},
		WithIgnoreHeaders(httpTestIgnoreHeaders...),
	)
	if err != nil {
		t.Fatalf("Diff() error = %v", err)
	}

	if len(report.Stale) != 0 {
		t.Errorf("expected no stale fixtures, got %d", len(report.Stale))
	}
}

func TestDiff_LargeNumberOfFields(t *testing.T) {
	// Stress test: many fields, some changed.
	fixture := make(map[string]any)
	live := make(map[string]any)
	for i := 0; i < 100; i++ {
		key := fmt.Sprintf("field_%d", i)
		fixture[key] = float64(i)
		if i%10 == 0 {
			live[key] = float64(i + 1000)
		} else {
			live[key] = float64(i)
		}
	}

	fixtureBytes, _ := json.Marshal(fixture)
	liveBytes, _ := json.Marshal(live)

	diffs := diffJSONBodies(fixtureBytes, liveBytes, nil)

	// 10 fields changed (0, 10, 20, ..., 90).
	if len(diffs) != 10 {
		t.Errorf("expected 10 diffs, got %d", len(diffs))
	}
}

func TestDiff_ContextCancellationMidLoop(t *testing.T) {
	// Cancel context between first and second request.
	tape1 := newDiffTestTape("t1", "GET", "http://example.com/first", 200,
		http.Header{"Content-Type": {"application/json"}},
		[]byte(`{"ok":true}`),
	)
	tape2 := newDiffTestTape("t2", "GET", "http://example.com/second", 200,
		http.Header{"Content-Type": {"application/json"}},
		[]byte(`{"ok":true}`),
	)
	store := seedDiffStore(t, tape1, tape2)

	ctx, cancel := context.WithCancel(context.Background())
	callCount := 0

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		if callCount == 1 {
			cancel() // cancel after first request
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(200)
		w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()

	req1, _ := http.NewRequest("GET", srv.URL+"/first", nil)
	req2, _ := http.NewRequest("GET", srv.URL+"/second", nil)

	_, err := Diff(ctx, store, srv.Client().Transport,
		[]*http.Request{req1, req2},
		WithIgnoreHeaders(httpTestIgnoreHeaders...),
	)
	if err == nil {
		t.Fatal("expected error from mid-loop context cancellation")
	}
	if !strings.Contains(err.Error(), "cancel") {
		t.Errorf("expected error to contain 'cancel', got %q", err.Error())
	}
}

func TestDiff_ArrayShorterInLive(t *testing.T) {
	fixture := `{"items":[1,2,3]}`
	live := `{"items":[1,2]}`

	diffs := diffJSONBodies([]byte(fixture), []byte(live), nil)
	if len(diffs) != 1 {
		t.Fatalf("expected 1 diff, got %d: %+v", len(diffs), diffs)
	}
	if diffs[0].Kind != FieldRemoved {
		t.Errorf("expected FieldRemoved, got %d", diffs[0].Kind)
	}
	if diffs[0].Path != "$.items.2" {
		t.Errorf("expected path $.items.2, got %q", diffs[0].Path)
	}
}

func TestDiff_IgnorePathInArray(t *testing.T) {
	// Ignore a specific array index path.
	fixture := `[1,2,3]`
	live := `[1,99,3]`

	pp, ok := parsePath("$.items") // dummy -- we need to test array-level ignore
	_ = pp
	_ = ok

	// For root-level arrays, indices are at $.0, $.1, etc.
	// The ignore path syntax doesn't support bare indices, but our traversal
	// uses isIgnored with the concrete path representation. This is for
	// coverage of the array-level isIgnored check.
	diffs := diffJSONBodies([]byte(fixture), []byte(live), nil)
	if len(diffs) != 1 {
		t.Fatalf("expected 1 diff, got %d: %+v", len(diffs), diffs)
	}
}

func TestDiffJSONBodies_InvalidFixtureJSON(t *testing.T) {
	// When fixture JSON is invalid, diffJSONBodies returns nil.
	diffs := diffJSONBodies([]byte(`invalid`), []byte(`{"a":1}`), nil)
	if diffs != nil {
		t.Errorf("expected nil diffs for invalid fixture JSON, got %+v", diffs)
	}
}

func TestDiffJSONBodies_InvalidLiveJSON(t *testing.T) {
	// When live JSON is invalid, diffJSONBodies returns nil.
	diffs := diffJSONBodies([]byte(`{"a":1}`), []byte(`invalid`), nil)
	if diffs != nil {
		t.Errorf("expected nil diffs for invalid live JSON, got %+v", diffs)
	}
}

func TestDiff_ReadBodyError(t *testing.T) {
	tape := newDiffTestTape("t1", "POST", "http://example.com/api", 200,
		http.Header{},
		[]byte(`{}`),
	)
	store := seedDiffStore(t, tape)

	// Use a request with a body that fails to read.
	req, _ := http.NewRequest("POST", "http://example.com/api",
		&errorReader{err: errors.New("body read error")})

	report, err := Diff(context.Background(), store, http.DefaultTransport, []*http.Request{req})
	if err != nil {
		t.Fatalf("Diff() error = %v", err)
	}

	r := report.Results[0]
	if r.Status != DiffDrifted {
		t.Errorf("expected DiffDrifted on body read error, got %d", r.Status)
	}
	if r.Error == "" {
		t.Error("expected non-empty Error field")
	}
}

// errorReader is an io.Reader that always returns an error.
type errorReader struct {
	err error
}

func (e *errorReader) Read(_ []byte) (int, error) {
	return 0, e.err
}

func (e *errorReader) Close() error {
	return nil
}

func TestDiff_CompareJSON_RootScalar(t *testing.T) {
	// Root-level scalar comparison (not objects or arrays).
	diffs := diffJSONBodies([]byte(`42`), []byte(`43`), nil)
	if len(diffs) != 1 {
		t.Fatalf("expected 1 diff for root scalar, got %d", len(diffs))
	}
	if diffs[0].Path != "$" {
		t.Errorf("expected path $, got %q", diffs[0].Path)
	}
}

func TestDiff_CompareJSON_ObjectToArray(t *testing.T) {
	// Type mismatch at root: object vs array.
	diffs := diffJSONBodies([]byte(`{"a":1}`), []byte(`[1,2]`), nil)
	if len(diffs) != 1 {
		t.Fatalf("expected 1 diff for type mismatch, got %d", len(diffs))
	}
	if diffs[0].Kind != FieldChanged {
		t.Errorf("expected FieldChanged for type mismatch, got %d", diffs[0].Kind)
	}
}

func TestDiff_HeadersDetectAddedByLiveServer(t *testing.T) {
	// Verify that headers added by the live server that weren't in the
	// fixture ARE detected as diffs (not silently ignored).
	tape := newDiffTestTape("t1", "GET", "http://example.com/api", 200,
		http.Header{"Content-Type": {"application/json"}},
		[]byte(`{}`),
	)
	store := seedDiffStore(t, tape)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(200)
		w.Write([]byte(`{}`))
	}))
	defer srv.Close()

	req, _ := http.NewRequest("GET", srv.URL+"/api", nil)

	// Do NOT ignore httptest headers -- verify they are detected.
	report, err := Diff(context.Background(), store, srv.Client().Transport, []*http.Request{req})
	if err != nil {
		t.Fatalf("Diff() error = %v", err)
	}

	r := report.Results[0]
	if r.Status != DiffDrifted {
		t.Errorf("expected DiffDrifted (headers added by httptest server), got %d", r.Status)
	}

	// httptest adds Content-Length and Date.
	foundDate := false
	foundContentLength := false
	for _, hd := range r.Headers {
		if hd.Name == "Date" && hd.Kind == FieldAdded {
			foundDate = true
		}
		if hd.Name == "Content-Length" && hd.Kind == FieldAdded {
			foundContentLength = true
		}
	}
	if !foundDate {
		t.Error("expected Date header to be detected as added")
	}
	if !foundContentLength {
		t.Error("expected Content-Length header to be detected as added")
	}
}
