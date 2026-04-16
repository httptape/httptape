package httptape

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestHTTP2Recording verifies that the Recorder transparently captures HTTP/2
// traffic. httptape operates at the http.Request/Response level — above the
// transport — so HTTP/2 negotiation is handled by net/http and the recorder
// sees only the resolved request/response objects.
//
// This test serves as the verification gate for issue #47.
func TestHTTP2Recording(t *testing.T) {
	t.Parallel()

	const wantBody = `{"protocol":"http/2"}`

	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.ProtoMajor != 2 {
			t.Errorf("server saw %s, want HTTP/2", r.Proto)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(wantBody))
	}))
	srv.EnableHTTP2 = true
	srv.StartTLS()
	defer srv.Close()

	store := NewMemoryStore()
	rec := NewRecorder(store,
		WithTransport(srv.Client().Transport),
		WithRoute("http2-test"),
		WithAsync(false),
	)
	defer rec.Close()

	client := &http.Client{Transport: rec}
	resp, err := client.Get(srv.URL + "/h2")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()

	if resp.ProtoMajor != 2 {
		t.Fatalf("client saw %s, want HTTP/2", resp.Proto)
	}
	gotBody, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	if string(gotBody) != wantBody {
		t.Errorf("response body: got %q want %q", gotBody, wantBody)
	}

	tapes, err := store.List(context.Background(), Filter{Route: "http2-test"})
	if err != nil {
		t.Fatalf("list tapes: %v", err)
	}
	if len(tapes) != 1 {
		t.Fatalf("recorded %d tapes, want 1", len(tapes))
	}
	tape := tapes[0]
	if tape.Request.Method != http.MethodGet {
		t.Errorf("recorded method: got %s want GET", tape.Request.Method)
	}
	if tape.Response.StatusCode != http.StatusOK {
		t.Errorf("recorded status: got %d want 200", tape.Response.StatusCode)
	}
	if string(tape.Response.Body) != wantBody {
		t.Errorf("recorded body: got %q want %q", tape.Response.Body, wantBody)
	}
}

// TestHTTP2Replay verifies that the mock Server can serve recorded fixtures
// over an HTTP/2 connection. The Server is an http.Handler, so HTTP/2 is
// provided by whatever http.Server hosts it — here, an HTTP/2-enabled
// httptest.Server.
func TestHTTP2Replay(t *testing.T) {
	t.Parallel()

	const wantBody = `{"replayed":true}`

	store := NewMemoryStore()
	tape := NewTape("http2-replay",
		RecordedReq{
			Method:  http.MethodGet,
			URL:     "/replay",
			Headers: http.Header{},
		},
		RecordedResp{
			StatusCode: http.StatusOK,
			Headers:    http.Header{"Content-Type": []string{"application/json"}},
			Body:       []byte(wantBody),
		},
	)
	if err := store.Save(context.Background(), tape); err != nil {
		t.Fatalf("save tape: %v", err)
	}

	srv := httptest.NewUnstartedServer(NewServer(store))
	srv.EnableHTTP2 = true
	srv.StartTLS()
	defer srv.Close()

	resp, err := srv.Client().Get(srv.URL + "/replay")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()

	if resp.ProtoMajor != 2 {
		t.Fatalf("client saw %s, want HTTP/2", resp.Proto)
	}
	if resp.StatusCode != http.StatusOK {
		t.Errorf("status: got %d want 200", resp.StatusCode)
	}
	gotBody, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	if string(gotBody) != wantBody {
		t.Errorf("body: got %q want %q", gotBody, wantBody)
	}
}
