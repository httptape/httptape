package httptape

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
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
		WithProxyMatcher(NewCompositeMatcher(MatchMethod(), MatchPath(), MatchBodyHash())),
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

func TestProxy_PanicsOnNilStores(t *testing.T) {
	t.Run("nil l1", func(t *testing.T) {
		defer func() {
			if r := recover(); r == nil {
				t.Error("expected panic on nil L1 store")
			}
		}()
		NewProxy(nil, NewMemoryStore())
	})

	t.Run("nil l2", func(t *testing.T) {
		defer func() {
			if r := recover(); r == nil {
				t.Error("expected panic on nil L2 store")
			}
		}()
		NewProxy(NewMemoryStore(), nil)
	})
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
