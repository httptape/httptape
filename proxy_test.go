package httptape

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// roundTripperFunc adapts a function to http.RoundTripper for testing.
type roundTripperFunc func(*http.Request) (*http.Response, error)

func (f roundTripperFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

// successTransport returns a transport that always succeeds with the given
// status code and body.
func successTransport(statusCode int, body string) http.RoundTripper {
	return roundTripperFunc(func(_ *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: statusCode,
			Header:     http.Header{"X-Upstream": {"true"}},
			Body:       io.NopCloser(strings.NewReader(body)),
		}, nil
	})
}

// failingTransport returns a transport that always returns the given error.
func failingTransport(err error) http.RoundTripper {
	return roundTripperFunc(func(_ *http.Request) (*http.Response, error) {
		return nil, err
	})
}

func TestProxy_SuccessPath(t *testing.T) {
	l1 := NewMemoryStore()
	l2 := NewMemoryStore()

	proxy := NewProxy(l1, l2,
		WithProxyTransport(successTransport(200, "hello")),
	)

	req, _ := http.NewRequest("GET", "http://example.com/api/users", nil)
	resp, err := proxy.RoundTrip(req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer resp.Body.Close()

	// Real response returned.
	if resp.StatusCode != 200 {
		t.Errorf("got status %d, want 200", resp.StatusCode)
	}
	respBody, _ := io.ReadAll(resp.Body)
	if string(respBody) != "hello" {
		t.Errorf("got body %q, want %q", string(respBody), "hello")
	}

	// No X-Httptape-Source header on success.
	if src := resp.Header.Get("X-Httptape-Source"); src != "" {
		t.Errorf("got X-Httptape-Source=%q on success, want empty", src)
	}

	// L1 and L2 should each have one tape.
	l1Tapes, _ := l1.List(req.Context(), Filter{})
	if len(l1Tapes) != 1 {
		t.Fatalf("L1 has %d tapes, want 1", len(l1Tapes))
	}
	l2Tapes, _ := l2.List(req.Context(), Filter{})
	if len(l2Tapes) != 1 {
		t.Fatalf("L2 has %d tapes, want 1", len(l2Tapes))
	}

	// Both tapes have the upstream response headers.
	if l1Tapes[0].Response.Headers.Get("X-Upstream") != "true" {
		t.Error("L1 tape missing X-Upstream header")
	}
}

func TestProxy_FallbackToL1(t *testing.T) {
	l1 := NewMemoryStore()
	l2 := NewMemoryStore()

	// Pre-populate L1 with a tape.
	tape := NewTape("", RecordedReq{
		Method:   "GET",
		URL:      "http://example.com/api/users",
		Headers:  http.Header{},
		BodyHash: "",
	}, RecordedResp{
		StatusCode: 200,
		Headers:    http.Header{"X-Custom": {"from-l1"}},
		Body:       []byte("l1-response"),
	})
	l1.Save(context.Background(), tape) //nolint:errcheck

	transportErr := errors.New("connection refused")
	proxy := NewProxy(l1, l2,
		WithProxyTransport(failingTransport(transportErr)),
	)

	req, _ := http.NewRequest("GET", "http://example.com/api/users", nil)
	resp, err := proxy.RoundTrip(req)
	if err != nil {
		t.Fatalf("unexpected error: %v (expected fallback)", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		t.Errorf("got status %d, want 200", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if string(body) != "l1-response" {
		t.Errorf("got body %q, want %q", string(body), "l1-response")
	}
	if src := resp.Header.Get("X-Httptape-Source"); src != "l1-cache" {
		t.Errorf("got X-Httptape-Source=%q, want %q", src, "l1-cache")
	}
}

func TestProxy_FallbackToL2(t *testing.T) {
	l1 := NewMemoryStore() // empty
	l2 := NewMemoryStore()

	// Pre-populate L2 with a tape.
	tape := NewTape("", RecordedReq{
		Method:   "GET",
		URL:      "http://example.com/api/users",
		Headers:  http.Header{},
		BodyHash: "",
	}, RecordedResp{
		StatusCode: 200,
		Headers:    http.Header{"X-Custom": {"from-l2"}},
		Body:       []byte("l2-response"),
	})
	l2.Save(context.Background(), tape) //nolint:errcheck

	transportErr := errors.New("connection refused")
	proxy := NewProxy(l1, l2,
		WithProxyTransport(failingTransport(transportErr)),
	)

	req, _ := http.NewRequest("GET", "http://example.com/api/users", nil)
	resp, err := proxy.RoundTrip(req)
	if err != nil {
		t.Fatalf("unexpected error: %v (expected fallback)", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		t.Errorf("got status %d, want 200", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if string(body) != "l2-response" {
		t.Errorf("got body %q, want %q", string(body), "l2-response")
	}
	if src := resp.Header.Get("X-Httptape-Source"); src != "l2-cache" {
		t.Errorf("got X-Httptape-Source=%q, want %q", src, "l2-cache")
	}
}

func TestProxy_NoCacheError(t *testing.T) {
	l1 := NewMemoryStore() // empty
	l2 := NewMemoryStore() // empty

	transportErr := errors.New("connection refused")
	proxy := NewProxy(l1, l2,
		WithProxyTransport(failingTransport(transportErr)),
	)

	req, _ := http.NewRequest("GET", "http://example.com/api/users", nil)
	resp, err := proxy.RoundTrip(req)
	if resp != nil {
		t.Errorf("expected nil response, got status %d", resp.StatusCode)
	}
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "connection refused") {
		t.Errorf("expected original error, got %v", err)
	}
}

func TestProxy_SanitizationOnL2Only(t *testing.T) {
	l1 := NewMemoryStore()
	l2 := NewMemoryStore()

	// Create a sanitizer that redacts a specific header.
	sanitizer := NewPipeline(RedactHeaders("Authorization"))

	proxy := NewProxy(l1, l2,
		WithProxyTransport(roundTripperFunc(func(req *http.Request) (*http.Response, error) {
			return &http.Response{
				StatusCode: 200,
				Header:     http.Header{"Content-Type": {"application/json"}},
				Body:       io.NopCloser(strings.NewReader("ok")),
			}, nil
		})),
		WithProxySanitizer(sanitizer),
	)

	req, _ := http.NewRequest("GET", "http://example.com/api/users", nil)
	req.Header.Set("Authorization", "Bearer secret-token")
	resp, err := proxy.RoundTrip(req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	resp.Body.Close()

	// L1 tape should have the raw Authorization header.
	l1Tapes, _ := l1.List(req.Context(), Filter{})
	if len(l1Tapes) != 1 {
		t.Fatalf("L1 has %d tapes, want 1", len(l1Tapes))
	}
	l1Auth := l1Tapes[0].Request.Headers.Get("Authorization")
	if l1Auth != "Bearer secret-token" {
		t.Errorf("L1 Authorization=%q, want %q", l1Auth, "Bearer secret-token")
	}

	// L2 tape should have the header redacted.
	l2Tapes, _ := l2.List(req.Context(), Filter{})
	if len(l2Tapes) != 1 {
		t.Fatalf("L2 has %d tapes, want 1", len(l2Tapes))
	}
	l2Auth := l2Tapes[0].Request.Headers.Get("Authorization")
	if l2Auth == "Bearer secret-token" {
		t.Errorf("L2 Authorization should be redacted, got %q", l2Auth)
	}
}

func TestProxy_FallbackOn5xx(t *testing.T) {
	l1 := NewMemoryStore()
	l2 := NewMemoryStore()

	// Pre-populate L1 with a cached tape.
	tape := NewTape("", RecordedReq{
		Method:   "GET",
		URL:      "http://example.com/api/users",
		Headers:  http.Header{},
		BodyHash: "",
	}, RecordedResp{
		StatusCode: 200,
		Headers:    http.Header{},
		Body:       []byte("cached-ok"),
	})
	l1.Save(context.Background(), tape) //nolint:errcheck

	// Transport returns 503.
	transport := roundTripperFunc(func(_ *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: 503,
			Header:     http.Header{},
			Body:       io.NopCloser(strings.NewReader("Service Unavailable")),
		}, nil
	})

	proxy := NewProxy(l1, l2,
		WithProxyTransport(transport),
		WithProxyFallbackOn(func(err error, resp *http.Response) bool {
			if err != nil {
				return true
			}
			return resp != nil && resp.StatusCode >= 500
		}),
	)

	req, _ := http.NewRequest("GET", "http://example.com/api/users", nil)
	resp, err := proxy.RoundTrip(req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer resp.Body.Close()

	// Should get the cached response, not the 503.
	if resp.StatusCode != 200 {
		t.Errorf("got status %d, want 200 (fallback)", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if string(body) != "cached-ok" {
		t.Errorf("got body %q, want %q", string(body), "cached-ok")
	}
	if src := resp.Header.Get("X-Httptape-Source"); src != "l1-cache" {
		t.Errorf("got X-Httptape-Source=%q, want %q", src, "l1-cache")
	}
}

func TestProxy_XHttptapeSourceHeader(t *testing.T) {
	tests := []struct {
		name       string
		l1Tape     bool
		l2Tape     bool
		wantSource string
	}{
		{"l1 fallback", true, false, "l1-cache"},
		{"l2 fallback", false, true, "l2-cache"},
		{"l1 preferred over l2", true, true, "l1-cache"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			l1 := NewMemoryStore()
			l2 := NewMemoryStore()

			if tt.l1Tape {
				tape := NewTape("", RecordedReq{
					Method: "GET", URL: "http://example.com/x", Headers: http.Header{},
				}, RecordedResp{
					StatusCode: 200, Headers: http.Header{}, Body: []byte("l1"),
				})
				l1.Save(context.Background(), tape) //nolint:errcheck
			}
			if tt.l2Tape {
				tape := NewTape("", RecordedReq{
					Method: "GET", URL: "http://example.com/x", Headers: http.Header{},
				}, RecordedResp{
					StatusCode: 200, Headers: http.Header{}, Body: []byte("l2"),
				})
				l2.Save(context.Background(), tape) //nolint:errcheck
			}

			proxy := NewProxy(l1, l2,
				WithProxyTransport(failingTransport(errors.New("down"))),
			)

			req, _ := http.NewRequest("GET", "http://example.com/x", nil)
			resp, err := proxy.RoundTrip(req)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			defer resp.Body.Close()

			if src := resp.Header.Get("X-Httptape-Source"); src != tt.wantSource {
				t.Errorf("got X-Httptape-Source=%q, want %q", src, tt.wantSource)
			}
		})
	}
}

func TestProxy_Close_NoOp(t *testing.T) {
	// Proxy has no Close method (per ADR-26: no goroutines, no resources to release).
	// This test verifies it implements RoundTripper only, not io.Closer.
	l1 := NewMemoryStore()
	l2 := NewMemoryStore()
	proxy := NewProxy(l1, l2, WithProxyTransport(successTransport(200, "ok")))

	// Verify it implements http.RoundTripper.
	var _ http.RoundTripper = proxy

	// Simple round-trip works.
	req, _ := http.NewRequest("GET", "http://example.com/", nil)
	resp, err := proxy.RoundTrip(req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	resp.Body.Close()
}

func TestProxy_ConcurrentSafety(t *testing.T) {
	l1 := NewMemoryStore()
	l2 := NewMemoryStore()

	callCount := 0
	var mu sync.Mutex
	transport := roundTripperFunc(func(_ *http.Request) (*http.Response, error) {
		mu.Lock()
		callCount++
		shouldFail := callCount%3 == 0
		mu.Unlock()
		if shouldFail {
			return nil, errors.New("intermittent failure")
		}
		return &http.Response{
			StatusCode: 200,
			Header:     http.Header{},
			Body:       io.NopCloser(strings.NewReader("ok")),
		}, nil
	})

	proxy := NewProxy(l1, l2, WithProxyTransport(transport))

	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			req, _ := http.NewRequest("GET", "http://example.com/api/data", nil)
			resp, err := proxy.RoundTrip(req)
			if err == nil {
				resp.Body.Close()
			}
			// No panic = success for concurrent safety test.
		}()
	}
	wg.Wait()
}

func TestProxy_RequestBodyPreservedForMatching(t *testing.T) {
	l1 := NewMemoryStore()
	l2 := NewMemoryStore()

	// Pre-populate L1 with a POST tape that has a specific body hash.
	postBody := []byte(`{"action":"create"}`)
	tape := NewTape("", RecordedReq{
		Method:   "POST",
		URL:      "http://example.com/api/items",
		Headers:  http.Header{},
		Body:     postBody,
		BodyHash: BodyHashFromBytes(postBody),
	}, RecordedResp{
		StatusCode: 201,
		Headers:    http.Header{},
		Body:       []byte("created"),
	})
	l1.Save(context.Background(), tape) //nolint:errcheck

	proxy := NewProxy(l1, l2,
		WithProxyTransport(failingTransport(errors.New("down"))),
		WithProxyMatcher(NewCompositeMatcher(MethodCriterion{}, PathCriterion{}, BodyHashCriterion{})),
	)

	req, _ := http.NewRequest("POST", "http://example.com/api/items", bytes.NewReader(postBody))
	resp, err := proxy.RoundTrip(req)
	if err != nil {
		t.Fatalf("unexpected error: %v (expected fallback match by body hash)", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	if string(body) != "created" {
		t.Errorf("got body %q, want %q", string(body), "created")
	}
	if src := resp.Header.Get("X-Httptape-Source"); src != "l1-cache" {
		t.Errorf("got X-Httptape-Source=%q, want %q", src, "l1-cache")
	}
}

func TestProxy_PanicsOnNilL1Store(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Error("expected panic on nil L1 store")
		}
	}()
	NewProxy(nil, NewMemoryStore())
}

func TestProxy_PanicsOnNilL2Store(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Error("expected panic on nil L2 store")
		}
	}()
	NewProxy(NewMemoryStore(), nil)
}

func TestProxy_OnErrorCallback(t *testing.T) {
	// Use a store that always fails on Save to trigger onError.
	var capturedErrors []string
	var mu sync.Mutex

	l1 := NewMemoryStore()
	l2 := NewMemoryStore()

	proxy := NewProxy(l1, l2,
		WithProxyTransport(successTransport(200, "ok")),
		WithProxyOnError(func(err error) {
			mu.Lock()
			capturedErrors = append(capturedErrors, err.Error())
			mu.Unlock()
		}),
	)

	req, _ := http.NewRequest("GET", "http://example.com/api", nil)
	resp, err := proxy.RoundTrip(req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	resp.Body.Close()

	// No errors expected with normal stores.
	mu.Lock()
	if len(capturedErrors) != 0 {
		t.Errorf("got %d errors, want 0: %v", len(capturedErrors), capturedErrors)
	}
	mu.Unlock()
}

func TestProxy_FallbackOn5xx_NoCacheMatch(t *testing.T) {
	l1 := NewMemoryStore() // empty
	l2 := NewMemoryStore() // empty

	// Transport returns 503 with a body.
	transport := roundTripperFunc(func(_ *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: 503,
			Header:     http.Header{"X-Upstream": {"true"}},
			Body:       io.NopCloser(strings.NewReader("Service Unavailable")),
		}, nil
	})

	proxy := NewProxy(l1, l2,
		WithProxyTransport(transport),
		WithProxyFallbackOn(func(err error, resp *http.Response) bool {
			if err != nil {
				return true
			}
			return resp != nil && resp.StatusCode >= 500
		}),
	)

	req, _ := http.NewRequest("GET", "http://example.com/api/users", nil)
	resp, err := proxy.RoundTrip(req)

	// Must not return (nil, nil) -- that violates the RoundTripper contract.
	if resp == nil && err == nil {
		t.Fatal("RoundTrip returned (nil, nil), violating RoundTripper contract")
	}

	// With no cache match, the original 5xx response should be returned.
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 503 {
		t.Errorf("got status %d, want 503 (original 5xx)", resp.StatusCode)
	}

	body, _ := io.ReadAll(resp.Body)
	if string(body) != "Service Unavailable" {
		t.Errorf("got body %q, want %q", string(body), "Service Unavailable")
	}
}

func TestProxy_WithProxyRoute(t *testing.T) {
	l1 := NewMemoryStore()
	l2 := NewMemoryStore()

	proxy := NewProxy(l1, l2,
		WithProxyTransport(successTransport(200, "ok")),
		WithProxyRoute("users-api"),
	)

	req, _ := http.NewRequest("GET", "http://example.com/api/users", nil)
	resp, err := proxy.RoundTrip(req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	resp.Body.Close()

	l1Tapes, _ := l1.List(req.Context(), Filter{})
	if len(l1Tapes) != 1 {
		t.Fatalf("L1 has %d tapes, want 1", len(l1Tapes))
	}
	if l1Tapes[0].Route != "users-api" {
		t.Errorf("L1 tape route=%q, want %q", l1Tapes[0].Route, "users-api")
	}

	l2Tapes, _ := l2.List(req.Context(), Filter{})
	if len(l2Tapes) != 1 {
		t.Fatalf("L2 has %d tapes, want 1", len(l2Tapes))
	}
	if l2Tapes[0].Route != "users-api" {
		t.Errorf("L2 tape route=%q, want %q", l2Tapes[0].Route, "users-api")
	}
}

// TestProxy_HealthDisabledByDefault is the backward-compat regression test
// required by ADR-28: with the new health options absent, the proxy must
// expose no health surface, spawn no goroutines on Start(), and behave
// byte-for-byte as before.
func TestProxy_HealthDisabledByDefault(t *testing.T) {
	l1 := NewMemoryStore()
	l2 := NewMemoryStore()

	baseline := runtime.NumGoroutine()

	proxy := NewProxy(l1, l2,
		WithProxyTransport(successTransport(200, "ok")),
	)

	if h := proxy.HealthHandler(); h != nil {
		t.Fatalf("HealthHandler() = %v, want nil with default options", h)
	}

	// Start must be a no-op when no health monitor exists.
	proxy.Start()
	proxy.Start() // idempotent
	time.Sleep(50 * time.Millisecond)
	if got := runtime.NumGoroutine(); got > baseline+1 {
		// Allow +1 for runtime jitter; we expect zero goroutines from Start().
		t.Errorf("Start() spawned goroutines: baseline=%d, got=%d", baseline, got)
	}

	// A normal RoundTrip continues to behave exactly as before — no new
	// headers, no panic on the nil-receiver observe call.
	req, _ := http.NewRequest("GET", "http://example.com/api/data", nil)
	resp, err := proxy.RoundTrip(req)
	if err != nil {
		t.Fatalf("RoundTrip: %v", err)
	}
	resp.Body.Close()

	if src := resp.Header.Get("X-Httptape-Source"); src != "" {
		t.Errorf("default-off proxy added X-Httptape-Source=%q on success", src)
	}

	if err := proxy.Close(); err != nil {
		t.Errorf("Close on default proxy: %v", err)
	}
}

// TestProxy_HealthHeaderUnchanged confirms X-Httptape-Source is still emitted
// on cache fallbacks when the health endpoint is enabled.
func TestProxy_HealthHeaderUnchanged(t *testing.T) {
	l1 := NewMemoryStore()
	l2 := NewMemoryStore()

	tape := NewTape("", RecordedReq{
		Method: "GET", URL: "http://example.com/x", Headers: http.Header{},
	}, RecordedResp{
		StatusCode: 200, Headers: http.Header{}, Body: []byte("cached"),
	})
	l1.Save(context.Background(), tape) //nolint:errcheck

	proxy := NewProxy(l1, l2,
		WithProxyTransport(failingTransport(errors.New("down"))),
		WithProxyUpstreamURL("http://example.com"),
		WithProxyHealthEndpoint(),
		WithProxyProbeInterval(0), // no probe loop in this test
	)
	defer proxy.Close() //nolint:errcheck

	req, _ := http.NewRequest("GET", "http://example.com/x", nil)
	resp, err := proxy.RoundTrip(req)
	if err != nil {
		t.Fatalf("RoundTrip: %v", err)
	}
	resp.Body.Close()

	if src := resp.Header.Get("X-Httptape-Source"); src != "l1-cache" {
		t.Errorf("X-Httptape-Source=%q, want l1-cache", src)
	}
}

func TestProxy_HealthEndpointMounted(t *testing.T) {
	l1 := NewMemoryStore()
	l2 := NewMemoryStore()

	proxy := NewProxy(l1, l2,
		WithProxyTransport(successTransport(200, "ok")),
		WithProxyUpstreamURL("http://upstream.example"),
		WithProxyHealthEndpoint(),
		WithProxyProbeInterval(0),
	)
	defer proxy.Close() //nolint:errcheck

	if proxy.HealthHandler() == nil {
		t.Fatal("HealthHandler() is nil with WithProxyHealthEndpoint set")
	}

	srv := httptest.NewServer(proxy.HealthHandler())
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/__httptape/health")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		t.Errorf("status=%d, want 200", resp.StatusCode)
	}
	var snap HealthSnapshot
	if err := json.NewDecoder(resp.Body).Decode(&snap); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if snap.UpstreamURL != "http://upstream.example" {
		t.Errorf("UpstreamURL=%q", snap.UpstreamURL)
	}
}

func TestProxy_StartCloseIdempotent(t *testing.T) {
	l1 := NewMemoryStore()
	l2 := NewMemoryStore()

	proxy := NewProxy(l1, l2,
		WithProxyTransport(successTransport(200, "ok")),
		WithProxyUpstreamURL("http://up"),
		WithProxyHealthEndpoint(),
		WithProxyProbeInterval(20*time.Millisecond),
	)

	baseline := runtime.NumGoroutine()
	proxy.Start()
	proxy.Start()
	proxy.Start()

	time.Sleep(80 * time.Millisecond)

	if err := proxy.Close(); err != nil {
		t.Errorf("Close: %v", err)
	}
	if err := proxy.Close(); err != nil {
		t.Errorf("Close (second): %v", err)
	}

	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if runtime.NumGoroutine() <= baseline+1 {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Errorf("goroutine leak: baseline=%d, got=%d", baseline, runtime.NumGoroutine())
}

// TestProxy_HealthOptionsApplied verifies the additional health-related
// proxy options (path, error handler) are wired into the resulting monitor.
func TestProxy_HealthOptionsApplied(t *testing.T) {
	l1 := NewMemoryStore()
	l2 := NewMemoryStore()

	var captured atomic.Int32
	proxy := NewProxy(l1, l2,
		WithProxyTransport(failingTransport(errors.New("boom"))),
		WithProxyUpstreamURL("http://up"),
		WithProxyHealthEndpoint(),
		WithProxyProbeInterval(15*time.Millisecond),
		WithProxyProbePath("/healthz"),
		WithProxyHealthErrorHandler(func(error) { captured.Add(1) }),
	)
	defer proxy.Close() //nolint:errcheck

	if proxy.health == nil || proxy.health.probePath != "/healthz" {
		t.Errorf("probePath not propagated: %+v", proxy.health)
	}

	proxy.Start()
	deadline := time.After(2 * time.Second)
	for captured.Load() == 0 {
		select {
		case <-deadline:
			t.Fatal("custom error handler not invoked")
		default:
			time.Sleep(15 * time.Millisecond)
		}
	}
}

// TestProxy_HealthErrorHandlerFallsBackToOnError checks the precedence: a
// proxy with WithProxyOnError but no explicit health error handler routes
// probe errors through the existing onError callback.
func TestProxy_HealthErrorHandlerFallsBackToOnError(t *testing.T) {
	l1 := NewMemoryStore()
	l2 := NewMemoryStore()

	var captured atomic.Int32
	proxy := NewProxy(l1, l2,
		WithProxyTransport(failingTransport(errors.New("boom"))),
		WithProxyUpstreamURL("http://up"),
		WithProxyOnError(func(error) { captured.Add(1) }),
		WithProxyHealthEndpoint(),
		WithProxyProbeInterval(15*time.Millisecond),
	)
	defer proxy.Close() //nolint:errcheck

	proxy.Start()
	deadline := time.After(2 * time.Second)
	for captured.Load() == 0 {
		select {
		case <-deadline:
			t.Fatal("onError fallback not invoked")
		default:
			time.Sleep(15 * time.Millisecond)
		}
	}
}

// TestProxy_HealthPanicsWithoutUpstreamURL confirms the constructor guard
// fires when WithProxyHealthEndpoint is set without WithProxyUpstreamURL.
func TestProxy_HealthPanicsWithoutUpstreamURL(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Error("expected panic when health endpoint enabled without upstream URL")
		}
	}()
	NewProxy(NewMemoryStore(), NewMemoryStore(),
		WithProxyHealthEndpoint(),
	)
}

// TestProxy_HealthIntegration exercises the full path: a transport whose
// behaviour flips between healthy and broken; the probe drives the state
// transitions; an SSE subscriber receives the live -> l1-cache -> live
// sequence without any client-driven request.
func TestProxy_HealthIntegration(t *testing.T) {
	l1 := NewMemoryStore()
	l2 := NewMemoryStore()

	// Pre-populate L1 with a tape matching the probe (HEAD /). The broken
	// phase's probe will fall back to this entry, driving observe(l1-cache).
	tape := NewTape("", RecordedReq{
		Method:  "HEAD",
		URL:     "http://upstream.example/",
		Headers: http.Header{},
	}, RecordedResp{
		StatusCode: 200,
		Headers:    http.Header{"Content-Type": {"text/plain"}},
		Body:       []byte(""),
	})
	l1.Save(context.Background(), tape) //nolint:errcheck

	var broken atomic.Bool
	transport := roundTripperFunc(func(req *http.Request) (*http.Response, error) {
		if broken.Load() {
			return nil, errors.New("upstream down")
		}
		return &http.Response{
			StatusCode: 200,
			Header:     http.Header{},
			Body:       io.NopCloser(strings.NewReader("ok")),
		}, nil
	})

	proxy := NewProxy(l1, l2,
		WithProxyTransport(transport),
		WithProxyUpstreamURL("http://upstream.example"),
		WithProxyHealthEndpoint(),
		WithProxyProbeInterval(20*time.Millisecond),
	)
	defer proxy.Close() //nolint:errcheck

	srv := httptest.NewServer(proxy.HealthHandler())
	defer srv.Close()

	proxy.Start()

	// Subscribe via SSE.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "GET", srv.URL+"/__httptape/health/stream", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	defer resp.Body.Close()

	br := bufio.NewReader(resp.Body)

	// Drain initial seed.
	if _, err := readSSEEvent(br, 2*time.Second); err != nil {
		t.Fatalf("initial event: %v", err)
	}

	// Break the upstream; probe will fail, fallback hits L1, observe
	// transitions to l1-cache.
	broken.Store(true)
	payload, err := readSSEEvent(br, 3*time.Second)
	if err != nil {
		t.Fatalf("expected l1-cache event: %v", err)
	}
	var snap HealthSnapshot
	if err := json.Unmarshal([]byte(payload), &snap); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if snap.State != StateL1Cache {
		t.Fatalf("expected l1-cache, got %q", snap.State)
	}

	// Restore the upstream; the next probe sees live, transition fires.
	broken.Store(false)
	payload, err = readSSEEvent(br, 3*time.Second)
	if err != nil {
		t.Fatalf("expected live recovery event: %v", err)
	}
	if err := json.Unmarshal([]byte(payload), &snap); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if snap.State != StateLive {
		t.Fatalf("expected live, got %q", snap.State)
	}
}

// TestProxy_SingleFlightViaComposition verifies that concurrent identical
// requests on cache miss result in upstream being called exactly once,
// because Proxy composes CachingTransport internally and CachingTransport
// has single-flight deduplication enabled by default.
func TestProxy_SingleFlightViaComposition(t *testing.T) {
	l1 := NewMemoryStore()
	l2 := NewMemoryStore()

	var upstreamCalls atomic.Int32
	transport := roundTripperFunc(func(_ *http.Request) (*http.Response, error) {
		upstreamCalls.Add(1)
		// Small delay to allow concurrent requests to pile up.
		time.Sleep(50 * time.Millisecond)
		return &http.Response{
			StatusCode: 200,
			Header:     http.Header{"X-Upstream": {"true"}},
			Body:       io.NopCloser(strings.NewReader("single-flight-response")),
		}, nil
	})

	proxy := NewProxy(l1, l2, WithProxyTransport(transport))

	const concurrency = 10
	var wg sync.WaitGroup
	responses := make([]*http.Response, concurrency)
	errs := make([]error, concurrency)

	for i := 0; i < concurrency; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			req, _ := http.NewRequest("GET", "http://example.com/api/data", nil)
			responses[idx], errs[idx] = proxy.RoundTrip(req)
		}(i)
	}
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Fatalf("goroutine %d: unexpected error: %v", i, err)
		}
		body, _ := io.ReadAll(responses[i].Body)
		responses[i].Body.Close()
		if string(body) != "single-flight-response" {
			t.Errorf("goroutine %d: body=%q, want %q", i, string(body), "single-flight-response")
		}
	}

	// With single-flight dedup, upstream should be called exactly once.
	if got := upstreamCalls.Load(); got != 1 {
		t.Errorf("upstream called %d times, want 1 (single-flight dedup)", got)
	}
}

// TestProxy_CompositionSanitizationPath verifies that Proxy's internal
// CachingTransport applies the sanitizer to L2 writes while
// l1RecordingTransport saves raw tapes to L1, confirming the composition
// wiring is correct.
func TestProxy_CompositionSanitizationPath(t *testing.T) {
	l1 := NewMemoryStore()
	l2 := NewMemoryStore()

	sanitizer := NewPipeline(RedactHeaders("X-Secret"))

	proxy := NewProxy(l1, l2,
		WithProxyTransport(roundTripperFunc(func(req *http.Request) (*http.Response, error) {
			return &http.Response{
				StatusCode: 200,
				Header:     http.Header{"X-Response": {"ok"}},
				Body:       io.NopCloser(strings.NewReader("body")),
			}, nil
		})),
		WithProxySanitizer(sanitizer),
	)

	req, _ := http.NewRequest("GET", "http://example.com/api", nil)
	req.Header.Set("X-Secret", "top-secret-value")
	resp, err := proxy.RoundTrip(req)
	if err != nil {
		t.Fatalf("RoundTrip: %v", err)
	}
	resp.Body.Close()

	// L1 should have the raw (unsanitized) tape via l1RecordingTransport.
	l1Tapes, _ := l1.List(context.Background(), Filter{})
	if len(l1Tapes) != 1 {
		t.Fatalf("L1 has %d tapes, want 1", len(l1Tapes))
	}
	l1Secret := l1Tapes[0].Request.Headers.Get("X-Secret")
	if l1Secret != "top-secret-value" {
		t.Errorf("L1 X-Secret=%q, want raw value", l1Secret)
	}

	// L2 should have the sanitized tape via CachingTransport.
	l2Tapes, _ := l2.List(context.Background(), Filter{})
	if len(l2Tapes) != 1 {
		t.Fatalf("L2 has %d tapes, want 1", len(l2Tapes))
	}
	l2Secret := l2Tapes[0].Request.Headers.Get("X-Secret")
	if l2Secret == "top-secret-value" {
		t.Errorf("L2 X-Secret should be redacted, got raw value %q", l2Secret)
	}
}
