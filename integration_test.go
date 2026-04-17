package httptape

import (
	"bytes"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// --------------------------------------------------------------------
// Integration tests: full record-and-replay flow through Recorder + Server
// using both MemoryStore and FileStore.
// --------------------------------------------------------------------

// upstreamHandler returns an http.Handler that serves deterministic
// responses for a set of known routes, used as the upstream backend.
func upstreamHandler() http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("/api/users", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-Request-Id", "upstream-12345")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"users":[{"id":1,"name":"Alice","email":"alice@corp.example"},{"id":2,"name":"Bob","email":"bob@corp.example"}]}`))
	})

	mux.HandleFunc("/api/items", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		body, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		// Echo the request body in the response for verification.
		w.Write([]byte(`{"created":true,"echo":` + string(body) + `}`))
	})

	mux.HandleFunc("/api/health", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})

	mux.HandleFunc("/api/secret", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Set-Cookie", "session=abc123; Path=/; HttpOnly")
		w.Header().Set("X-Request-Id", "req-secret-999")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"token":"super-secret-token","user":{"email":"admin@corp.example","ssn":"123-45-6789"}}`))
	})

	return mux
}

// recordRequests sends a standard set of requests through the recorder,
// returning the responses received during recording for later comparison.
func recordRequests(t *testing.T, recorderClient *http.Client, upstreamURL string) []recordedResponse {
	t.Helper()

	var results []recordedResponse

	// GET /api/users
	resp, err := recorderClient.Get(upstreamURL + "/api/users")
	if err != nil {
		t.Fatalf("GET /api/users: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	results = append(results, recordedResponse{
		method:     "GET",
		path:       "/api/users",
		statusCode: resp.StatusCode,
		body:       string(body),
		headers:    resp.Header.Clone(),
	})

	// POST /api/items
	postBody := `{"name":"widget","price":9.99}`
	resp, err = recorderClient.Post(upstreamURL+"/api/items", "application/json", strings.NewReader(postBody))
	if err != nil {
		t.Fatalf("POST /api/items: %v", err)
	}
	body, _ = io.ReadAll(resp.Body)
	resp.Body.Close()
	results = append(results, recordedResponse{
		method:     "POST",
		path:       "/api/items",
		statusCode: resp.StatusCode,
		body:       string(body),
		headers:    resp.Header.Clone(),
	})

	// GET /api/health (204 no content)
	resp, err = recorderClient.Get(upstreamURL + "/api/health")
	if err != nil {
		t.Fatalf("GET /api/health: %v", err)
	}
	body, _ = io.ReadAll(resp.Body)
	resp.Body.Close()
	results = append(results, recordedResponse{
		method:     "GET",
		path:       "/api/health",
		statusCode: resp.StatusCode,
		body:       string(body),
		headers:    resp.Header.Clone(),
	})

	return results
}

// recordedResponse holds the response data captured during recording.
type recordedResponse struct {
	method     string
	path       string
	statusCode int
	body       string
	headers    http.Header
}

// replayAndVerify replays the same requests against a Server and verifies
// responses match the original recorded responses.
func replayAndVerify(t *testing.T, replayServer *httptest.Server, recorded []recordedResponse) {
	t.Helper()

	for _, rec := range recorded {
		var resp *http.Response
		var err error

		switch rec.method {
		case "GET":
			resp, err = http.Get(replayServer.URL + rec.path)
		case "POST":
			// For POST replay, we need to send a request with the same
			// path. The default matcher matches on method + path.
			resp, err = http.Post(replayServer.URL+rec.path, "application/json", strings.NewReader("{}"))
		default:
			t.Fatalf("unsupported method %s", rec.method)
		}

		if err != nil {
			t.Fatalf("replay %s %s: %v", rec.method, rec.path, err)
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()

		if resp.StatusCode != rec.statusCode {
			t.Errorf("replay %s %s: status = %d, want %d",
				rec.method, rec.path, resp.StatusCode, rec.statusCode)
		}

		if string(body) != rec.body {
			t.Errorf("replay %s %s: body = %q, want %q",
				rec.method, rec.path, string(body), rec.body)
		}

		// Verify application-level headers (skip transport headers).
		if ct := resp.Header.Get("Content-Type"); rec.headers.Get("Content-Type") != "" && ct != rec.headers.Get("Content-Type") {
			t.Errorf("replay %s %s: Content-Type = %q, want %q",
				rec.method, rec.path, ct, rec.headers.Get("Content-Type"))
		}
	}
}

// TestIntegration_RecordReplay_MemoryStore tests the full record-and-replay
// flow using MemoryStore.
func TestIntegration_RecordReplay_MemoryStore(t *testing.T) {
	// Start a real upstream server.
	upstream := httptest.NewServer(upstreamHandler())
	defer upstream.Close()

	// Create a MemoryStore and Recorder (synchronous for deterministic tests).
	store := NewMemoryStore()
	rec := NewRecorder(store, WithAsync(false))

	// Create an HTTP client that uses the Recorder as its transport.
	client := &http.Client{Transport: rec}

	// Record requests.
	recorded := recordRequests(t, client, upstream.URL)
	rec.Close()

	// Create a replay Server from the same store.
	srv := NewServer(store)
	replayTS := httptest.NewServer(srv)
	defer replayTS.Close()

	// Replay and verify.
	replayAndVerify(t, replayTS, recorded)
}

// TestIntegration_RecordReplay_FileStore tests the full record-and-replay
// flow using FileStore.
func TestIntegration_RecordReplay_FileStore(t *testing.T) {
	// Create a temp directory for the FileStore.
	dir := t.TempDir()

	// Start a real upstream server.
	upstream := httptest.NewServer(upstreamHandler())
	defer upstream.Close()

	// Create a FileStore and Recorder (synchronous).
	store, err := NewFileStore(WithDirectory(dir))
	if err != nil {
		t.Fatalf("NewFileStore: %v", err)
	}
	rec := NewRecorder(store, WithAsync(false))

	// Create an HTTP client that uses the Recorder as its transport.
	client := &http.Client{Transport: rec}

	// Record requests.
	recorded := recordRequests(t, client, upstream.URL)
	rec.Close()

	// Create a NEW FileStore from the same directory to prove persistence.
	store2, err := NewFileStore(WithDirectory(dir))
	if err != nil {
		t.Fatalf("NewFileStore (reload): %v", err)
	}

	// Create a replay Server from the new store.
	srv := NewServer(store2)
	replayTS := httptest.NewServer(srv)
	defer replayTS.Close()

	// Replay and verify.
	replayAndVerify(t, replayTS, recorded)
}

// TestIntegration_RecordReplay_AsyncRecorder tests that async recording
// also produces correct replays after Close.
func TestIntegration_RecordReplay_AsyncRecorder(t *testing.T) {
	upstream := httptest.NewServer(upstreamHandler())
	defer upstream.Close()

	store := NewMemoryStore()
	rec := NewRecorder(store, WithAsync(true), WithBufferSize(64))

	client := &http.Client{Transport: rec}

	recorded := recordRequests(t, client, upstream.URL)
	// Close flushes the async buffer.
	rec.Close()

	srv := NewServer(store)
	replayTS := httptest.NewServer(srv)
	defer replayTS.Close()

	replayAndVerify(t, replayTS, recorded)
}

// TestIntegration_RecordReplay_WithSanitization tests that tapes recorded
// with sanitization are replayed with sanitized values.
func TestIntegration_RecordReplay_WithSanitization(t *testing.T) {
	upstream := httptest.NewServer(upstreamHandler())
	defer upstream.Close()

	store := NewMemoryStore()

	// Create a sanitizer pipeline that redacts headers and body fields.
	sanitizer := NewPipeline(
		RedactHeaders("Set-Cookie", "X-Request-Id"),
		RedactBodyPaths("$.token", "$.user.ssn"),
	)

	rec := NewRecorder(store, WithAsync(false), WithSanitizer(sanitizer))
	client := &http.Client{Transport: rec}

	// Hit the /api/secret endpoint which has sensitive data.
	resp, err := client.Get(upstream.URL + "/api/secret")
	if err != nil {
		t.Fatalf("GET /api/secret: %v", err)
	}
	// The original response should still have the real data (recorder
	// does not alter the response returned to the caller).
	origBody, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if !strings.Contains(string(origBody), "super-secret-token") {
		t.Error("original response should contain real token")
	}

	rec.Close()

	// Replay from the store -- the replayed response should be sanitized.
	srv := NewServer(store)
	replayTS := httptest.NewServer(srv)
	defer replayTS.Close()

	replayResp, err := http.Get(replayTS.URL + "/api/secret")
	if err != nil {
		t.Fatalf("replay GET /api/secret: %v", err)
	}
	replayBody, _ := io.ReadAll(replayResp.Body)
	replayResp.Body.Close()

	replayStr := string(replayBody)

	// Token should be redacted.
	if strings.Contains(replayStr, "super-secret-token") {
		t.Error("replayed response should NOT contain real token after redaction")
	}
	if !strings.Contains(replayStr, Redacted) {
		t.Errorf("replayed response should contain %q, got: %s", Redacted, replayStr)
	}

	// SSN should be redacted.
	if strings.Contains(replayStr, "123-45-6789") {
		t.Error("replayed response should NOT contain real SSN after redaction")
	}

	// Set-Cookie header should be redacted.
	setCookie := replayResp.Header.Get("Set-Cookie")
	if setCookie != Redacted {
		t.Errorf("Set-Cookie = %q, want %q", setCookie, Redacted)
	}

	// X-Request-Id should be redacted.
	xReqID := replayResp.Header.Get("X-Request-Id")
	if xReqID != Redacted {
		t.Errorf("X-Request-Id = %q, want %q", xReqID, Redacted)
	}
}

// TestIntegration_RecordReplay_WithFakeFields tests that deterministic
// faking via FakeFields produces consistent replayed values through the
// full record-replay flow including a real HTTP client.
func TestIntegration_RecordReplay_WithFakeFields(t *testing.T) {
	upstream := httptest.NewServer(upstreamHandler())
	defer upstream.Close()

	store := NewMemoryStore()

	sanitizer := NewPipeline(
		FakeFields("test-seed", "$.users[*].email", "$.users[*].name"),
	)

	rec := NewRecorder(store, WithAsync(false), WithSanitizer(sanitizer))
	client := &http.Client{Transport: rec}

	resp, err := client.Get(upstream.URL + "/api/users")
	if err != nil {
		t.Fatalf("GET /api/users: %v", err)
	}
	io.ReadAll(resp.Body)
	resp.Body.Close()
	rec.Close()

	// Verify the stored tape has faked values.
	tapes, listErr := store.List(t.Context(), Filter{})
	if listErr != nil {
		t.Fatalf("List: %v", listErr)
	}
	if len(tapes) != 1 {
		t.Fatalf("expected 1 tape, got %d", len(tapes))
	}

	storedBody := string(tapes[0].Response.Body)

	// Original emails should NOT appear in the stored tape.
	if strings.Contains(storedBody, "alice@corp.example") {
		t.Error("stored tape should NOT contain original email alice@corp.example")
	}
	if strings.Contains(storedBody, "bob@corp.example") {
		t.Error("stored tape should NOT contain original email bob@corp.example")
	}

	// Faked emails should use the deterministic pattern.
	if !strings.Contains(storedBody, "@example.com") {
		t.Error("stored tape should contain faked emails with @example.com domain")
	}

	// Original names should NOT appear.
	if strings.Contains(storedBody, `"Alice"`) {
		t.Error("stored tape should NOT contain original name Alice")
	}
	if strings.Contains(storedBody, `"Bob"`) {
		t.Error("stored tape should NOT contain original name Bob")
	}

	// Replay via a real HTTP client — the Content-Length fix in ServeHTTP
	// ensures the faked body (different length) is served correctly.
	srv := NewServer(store)
	replayTS := httptest.NewServer(srv)
	defer replayTS.Close()

	replayResp, err := http.Get(replayTS.URL + "/api/users")
	if err != nil {
		t.Fatalf("replay GET /api/users: %v", err)
	}
	replayBody, _ := io.ReadAll(replayResp.Body)
	replayResp.Body.Close()

	if replayResp.StatusCode != http.StatusOK {
		t.Errorf("replay status = %d, want %d", replayResp.StatusCode, http.StatusOK)
	}

	replayStr := string(replayBody)

	if strings.Contains(replayStr, "alice@corp.example") {
		t.Error("replayed response should NOT contain original email alice@corp.example")
	}
	if !strings.Contains(replayStr, "@example.com") {
		t.Error("replayed response should contain faked emails with @example.com domain")
	}

	// Verify determinism: same seed + same input = same output.
	store2 := NewMemoryStore()
	rec2 := NewRecorder(store2, WithAsync(false), WithSanitizer(
		NewPipeline(FakeFields("test-seed", "$.users[*].email", "$.users[*].name")),
	))
	client2 := &http.Client{Transport: rec2}
	resp2, err := client2.Get(upstream.URL + "/api/users")
	if err != nil {
		t.Fatalf("GET /api/users (2nd): %v", err)
	}
	io.ReadAll(resp2.Body)
	resp2.Body.Close()
	rec2.Close()

	tapes2, _ := store2.List(t.Context(), Filter{})
	if len(tapes2) != 1 {
		t.Fatalf("expected 1 tape in store2, got %d", len(tapes2))
	}

	if string(tapes[0].Response.Body) != string(tapes2[0].Response.Body) {
		t.Errorf("deterministic faking produced different results:\n  run1: %s\n  run2: %s",
			string(tapes[0].Response.Body), string(tapes2[0].Response.Body))
	}
}

// TestIntegration_BothStores_IdenticalReplay verifies that MemoryStore and
// FileStore produce identical replay behavior for the same recorded traffic.
func TestIntegration_BothStores_IdenticalReplay(t *testing.T) {
	upstream := httptest.NewServer(upstreamHandler())
	defer upstream.Close()

	// --- Record with MemoryStore ---
	memStore := NewMemoryStore()
	memRec := NewRecorder(memStore, WithAsync(false))
	memClient := &http.Client{Transport: memRec}

	memResults := recordRequests(t, memClient, upstream.URL)
	memRec.Close()

	// --- Record with FileStore ---
	dir := t.TempDir()

	fileStore, err := NewFileStore(WithDirectory(dir))
	if err != nil {
		t.Fatalf("NewFileStore: %v", err)
	}
	fileRec := NewRecorder(fileStore, WithAsync(false))
	fileClient := &http.Client{Transport: fileRec}

	fileResults := recordRequests(t, fileClient, upstream.URL)
	fileRec.Close()

	// Verify both recording sessions captured the same responses.
	if len(memResults) != len(fileResults) {
		t.Fatalf("recorded count: memory=%d, file=%d", len(memResults), len(fileResults))
	}
	for i := range memResults {
		if memResults[i].statusCode != fileResults[i].statusCode {
			t.Errorf("request %d status: memory=%d, file=%d",
				i, memResults[i].statusCode, fileResults[i].statusCode)
		}
		if memResults[i].body != fileResults[i].body {
			t.Errorf("request %d body: memory=%q, file=%q",
				i, memResults[i].body, fileResults[i].body)
		}
	}

	// --- Replay from both stores ---
	memSrv := NewServer(memStore)
	memReplayTS := httptest.NewServer(memSrv)
	defer memReplayTS.Close()

	fileSrv := NewServer(fileStore)
	fileReplayTS := httptest.NewServer(fileSrv)
	defer fileReplayTS.Close()

	// Replay the same requests to both and compare.
	for _, rec := range memResults {
		var memResp, fileResp *http.Response

		switch rec.method {
		case "GET":
			memResp, err = http.Get(memReplayTS.URL + rec.path)
			if err != nil {
				t.Fatalf("replay memory GET %s: %v", rec.path, err)
			}
			fileResp, err = http.Get(fileReplayTS.URL + rec.path)
			if err != nil {
				t.Fatalf("replay file GET %s: %v", rec.path, err)
			}
		case "POST":
			memResp, err = http.Post(memReplayTS.URL+rec.path, "application/json", strings.NewReader("{}"))
			if err != nil {
				t.Fatalf("replay memory POST %s: %v", rec.path, err)
			}
			fileResp, err = http.Post(fileReplayTS.URL+rec.path, "application/json", strings.NewReader("{}"))
			if err != nil {
				t.Fatalf("replay file POST %s: %v", rec.path, err)
			}
		}

		memBody, _ := io.ReadAll(memResp.Body)
		memResp.Body.Close()
		fileBody, _ := io.ReadAll(fileResp.Body)
		fileResp.Body.Close()

		if memResp.StatusCode != fileResp.StatusCode {
			t.Errorf("replay %s %s status: memory=%d, file=%d",
				rec.method, rec.path, memResp.StatusCode, fileResp.StatusCode)
		}

		if !bytes.Equal(memBody, fileBody) {
			t.Errorf("replay %s %s body mismatch:\n  memory=%q\n  file=%q",
				rec.method, rec.path, string(memBody), string(fileBody))
		}

		// Compare Content-Type header.
		memCT := memResp.Header.Get("Content-Type")
		fileCT := fileResp.Header.Get("Content-Type")
		if memCT != fileCT {
			t.Errorf("replay %s %s Content-Type: memory=%q, file=%q",
				rec.method, rec.path, memCT, fileCT)
		}
	}
}

// TestIntegration_RecordReplay_FileStore_Sanitized tests the full
// record-sanitize-replay flow using FileStore to verify sanitized data
// survives JSON serialization round-trips.
func TestIntegration_RecordReplay_FileStore_Sanitized(t *testing.T) {
	dir := t.TempDir()

	upstream := httptest.NewServer(upstreamHandler())
	defer upstream.Close()

	store, err := NewFileStore(WithDirectory(dir))
	if err != nil {
		t.Fatalf("NewFileStore: %v", err)
	}

	sanitizer := NewPipeline(
		RedactHeaders("Set-Cookie"),
		RedactBodyPaths("$.token", "$.user.ssn"),
	)

	rec := NewRecorder(store, WithAsync(false), WithSanitizer(sanitizer))
	client := &http.Client{Transport: rec}

	resp, err := client.Get(upstream.URL + "/api/secret")
	if err != nil {
		t.Fatalf("GET /api/secret: %v", err)
	}
	io.ReadAll(resp.Body)
	resp.Body.Close()
	rec.Close()

	// Reload from disk with a new FileStore instance.
	store2, err := NewFileStore(WithDirectory(dir))
	if err != nil {
		t.Fatalf("NewFileStore (reload): %v", err)
	}

	srv := NewServer(store2)
	replayTS := httptest.NewServer(srv)
	defer replayTS.Close()

	replayResp, err := http.Get(replayTS.URL + "/api/secret")
	if err != nil {
		t.Fatalf("replay GET /api/secret: %v", err)
	}
	replayBody, _ := io.ReadAll(replayResp.Body)
	replayResp.Body.Close()

	replayStr := string(replayBody)

	if strings.Contains(replayStr, "super-secret-token") {
		t.Error("replayed response from FileStore should NOT contain real token")
	}
	if !strings.Contains(replayStr, Redacted) {
		t.Errorf("replayed response should contain %q, got: %s", Redacted, replayStr)
	}
	if strings.Contains(replayStr, "123-45-6789") {
		t.Error("replayed response from FileStore should NOT contain real SSN")
	}

	setCookie := replayResp.Header.Get("Set-Cookie")
	if setCookie != Redacted {
		t.Errorf("Set-Cookie = %q, want %q", setCookie, Redacted)
	}
}

// TestIntegration_RecordReplay_PostWithBody verifies that POST requests
// with bodies are recorded and replayed correctly, including request body
// matching via the default matcher (method + path).
func TestIntegration_RecordReplay_PostWithBody(t *testing.T) {
	upstream := httptest.NewServer(upstreamHandler())
	defer upstream.Close()

	store := NewMemoryStore()
	rec := NewRecorder(store, WithAsync(false))
	client := &http.Client{Transport: rec}

	// Send a POST with a specific body.
	postBody := `{"name":"gadget","price":42.00}`
	resp, err := client.Post(upstream.URL+"/api/items", "application/json", strings.NewReader(postBody))
	if err != nil {
		t.Fatalf("POST /api/items: %v", err)
	}
	origBody, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	rec.Close()

	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("upstream status = %d, want %d", resp.StatusCode, http.StatusCreated)
	}

	// Replay.
	srv := NewServer(store)
	replayTS := httptest.NewServer(srv)
	defer replayTS.Close()

	replayResp, err := http.Post(replayTS.URL+"/api/items", "application/json", strings.NewReader("irrelevant"))
	if err != nil {
		t.Fatalf("replay POST /api/items: %v", err)
	}
	replayBody, _ := io.ReadAll(replayResp.Body)
	replayResp.Body.Close()

	if replayResp.StatusCode != http.StatusCreated {
		t.Errorf("replay status = %d, want %d", replayResp.StatusCode, http.StatusCreated)
	}
	if string(replayBody) != string(origBody) {
		t.Errorf("replay body = %q, want %q", string(replayBody), string(origBody))
	}
}

// TestIntegration_SSE_RecordReplay_E2E tests the full SSE record-and-replay
// flow using MemoryStore: an upstream SSE server is recorded through the
// Recorder, then replayed by the Server with instant timing.
func TestIntegration_SSE_RecordReplay_E2E(t *testing.T) {
	// Start an upstream that serves SSE.
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		flusher, ok := w.(http.Flusher)
		if !ok {
			http.Error(w, "no flusher", 500)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(200)
		flusher.Flush()

		for i := 0; i < 5; i++ {
			body := strings.NewReader("")
			_ = body
			w.Write([]byte("data: {\"n\":" + strings.TrimRight(strings.Repeat("0", i+1), "0") + "}\n\n"))
			flusher.Flush()
		}
	}))
	defer upstream.Close()

	store := NewMemoryStore()
	rec := NewRecorder(store, WithAsync(false))
	client := &http.Client{Transport: rec}

	// Record.
	resp, err := client.Get(upstream.URL + "/events")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	origBody, _ := io.ReadAll(resp.Body)
	resp.Body.Close()

	// The caller should have received the raw SSE bytes.
	if !strings.Contains(string(origBody), "data:") {
		t.Error("caller should see raw SSE data")
	}

	// Replay.
	srv := NewServer(store, WithSSETiming(SSETimingInstant()))
	replayTS := httptest.NewServer(srv)
	defer replayTS.Close()

	replayResp, err := http.Get(replayTS.URL + "/events")
	if err != nil {
		t.Fatalf("replay GET: %v", err)
	}
	replayBody, _ := io.ReadAll(replayResp.Body)
	replayResp.Body.Close()

	if replayResp.StatusCode != 200 {
		t.Errorf("replay status = %d, want 200", replayResp.StatusCode)
	}

	// Check Content-Type.
	ct := replayResp.Header.Get("Content-Type")
	if ct != "text/event-stream" {
		t.Errorf("Content-Type = %q, want text/event-stream", ct)
	}

	// The replayed body should contain SSE data lines.
	if !strings.Contains(string(replayBody), "data:") {
		t.Error("replayed response should contain SSE data")
	}
}

// TestIntegration_SSE_Proxy_E2E tests the SSE proxy passthrough and L2
// fallback flow end-to-end.
func TestIntegration_SSE_Proxy_E2E(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		flusher, ok := w.(http.Flusher)
		if !ok {
			http.Error(w, "no flusher", 500)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(200)
		flusher.Flush()

		w.Write([]byte("data: proxy-event-1\n\n"))
		flusher.Flush()
		w.Write([]byte("data: proxy-event-2\n\n"))
		flusher.Flush()
	}))
	defer upstream.Close()

	l1 := NewMemoryStore()
	l2 := NewMemoryStore()

	proxy := NewProxy(l1, l2, WithProxyTransport(upstream.Client().Transport))

	// Passthrough: forward to upstream.
	req, _ := http.NewRequest("GET", upstream.URL+"/sse", nil)
	resp, err := proxy.RoundTrip(req)
	if err != nil {
		t.Fatalf("RoundTrip: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()

	if !strings.Contains(string(body), "proxy-event-1") {
		t.Error("proxy should deliver live SSE events")
	}

	// Wait for onDone save.
	deadline := time.After(2 * time.Second)
	for {
		l2Tapes, _ := l2.List(t.Context(), Filter{})
		if len(l2Tapes) > 0 {
			break
		}
		select {
		case <-deadline:
			t.Fatal("L2 tape not saved within timeout")
		default:
			time.Sleep(10 * time.Millisecond)
		}
	}

	// L2 fallback: upstream is now "down".
	proxy2 := NewProxy(l1, l2,
		WithProxyTransport(roundTripperFunc(func(_ *http.Request) (*http.Response, error) {
			return nil, errors.New("down")
		})),
		WithProxySSETiming(SSETimingInstant()),
	)

	req2, _ := http.NewRequest("GET", upstream.URL+"/sse", nil)
	resp2, err := proxy2.RoundTrip(req2)
	if err != nil {
		t.Fatalf("RoundTrip fallback: %v", err)
	}
	defer resp2.Body.Close()

	src := resp2.Header.Get("X-Httptape-Source")
	if src != "l1-cache" && src != "l2-cache" {
		t.Errorf("X-Httptape-Source = %q, want l1-cache or l2-cache", src)
	}

	// Read and verify the fallback SSE body.
	fallbackBody, _ := io.ReadAll(resp2.Body)
	if !strings.Contains(string(fallbackBody), "proxy-event-1") {
		t.Errorf("fallback should contain cached events, got: %q", string(fallbackBody))
	}
}
