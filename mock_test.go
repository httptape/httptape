package httptape

import (
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestMock_SingleStub(t *testing.T) {
	srv := Mock(
		When(GET("/api/health")).
			Respond(204).
			Build(),
	)
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/api/health")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 204 {
		t.Errorf("status = %d, want 204", resp.StatusCode)
	}
}

func TestMock_JSONBody(t *testing.T) {
	srv := Mock(
		When(GET("/api/users")).
			Respond(200, JSON(`{"users": [{"id": 1}]}`)).
			Build(),
	)
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/api/users")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		t.Errorf("status = %d, want 200", resp.StatusCode)
	}

	body, _ := io.ReadAll(resp.Body)
	want := `{"users": [{"id": 1}]}`
	if string(body) != want {
		t.Errorf("body = %q, want %q", string(body), want)
	}

	ct := resp.Header.Get("Content-Type")
	if ct != "application/json" {
		t.Errorf("Content-Type = %q, want %q", ct, "application/json")
	}
}

func TestMock_TextBody(t *testing.T) {
	srv := Mock(
		When(GET("/hello")).
			Respond(200, Text("hello world")).
			Build(),
	)
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/hello")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	if string(body) != "hello world" {
		t.Errorf("body = %q, want %q", string(body), "hello world")
	}

	ct := resp.Header.Get("Content-Type")
	if ct != "text/plain; charset=utf-8" {
		t.Errorf("Content-Type = %q, want %q", ct, "text/plain; charset=utf-8")
	}
}

func TestMock_BinaryBody(t *testing.T) {
	bin := []byte{0x00, 0x01, 0x02, 0xFF}
	srv := Mock(
		When(GET("/bin")).
			Respond(200, Binary(bin)).
			WithHeader("Content-Type", "application/octet-stream").
			Build(),
	)
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/bin")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	if len(body) != len(bin) {
		t.Fatalf("body length = %d, want %d", len(body), len(bin))
	}
	for i := range bin {
		if body[i] != bin[i] {
			t.Errorf("body[%d] = %x, want %x", i, body[i], bin[i])
		}
	}
}

func TestMock_MultipleStubs(t *testing.T) {
	srv := Mock(
		When(GET("/api/users")).
			Respond(200, JSON(`{"users": []}`)).
			Build(),
		When(POST("/api/users")).
			Respond(201, JSON(`{"id": 2}`)).
			Build(),
		When(GET("/api/health")).
			Respond(204).
			Build(),
	)
	defer srv.Close()

	// Test GET /api/users
	resp, err := http.Get(srv.URL + "/api/users")
	if err != nil {
		t.Fatalf("GET /api/users error: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Errorf("GET /api/users status = %d, want 200", resp.StatusCode)
	}

	// Test POST /api/users
	resp, err = http.Post(srv.URL+"/api/users", "application/json", strings.NewReader(`{"name":"test"}`))
	if err != nil {
		t.Fatalf("POST /api/users error: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != 201 {
		t.Errorf("POST /api/users status = %d, want 201", resp.StatusCode)
	}

	// Test GET /api/health
	resp, err = http.Get(srv.URL + "/api/health")
	if err != nil {
		t.Fatalf("GET /api/health error: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != 204 {
		t.Errorf("GET /api/health status = %d, want 204", resp.StatusCode)
	}
}

func TestMock_CustomHeader(t *testing.T) {
	srv := Mock(
		When(GET("/api/data")).
			Respond(200, JSON(`{}`)).
			WithHeader("X-Request-Id", "abc123").
			Build(),
	)
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/api/data")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer resp.Body.Close()

	if got := resp.Header.Get("X-Request-Id"); got != "abc123" {
		t.Errorf("X-Request-Id = %q, want %q", got, "abc123")
	}
}

func TestMock_ContentTypeOverride(t *testing.T) {
	// WithHeader called before Respond — explicit Content-Type should win.
	srv := Mock(
		When(GET("/api/data")).
			WithHeader("Content-Type", "application/vnd.api+json").
			Respond(200, JSON(`{}`)).
			Build(),
	)
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/api/data")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer resp.Body.Close()

	ct := resp.Header.Get("Content-Type")
	if ct != "application/vnd.api+json" {
		t.Errorf("Content-Type = %q, want %q", ct, "application/vnd.api+json")
	}
}

func TestMock_NoMatch(t *testing.T) {
	srv := Mock(
		When(GET("/api/users")).
			Respond(200).
			Build(),
	)
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/api/nonexistent")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 404 {
		t.Errorf("status = %d, want 404", resp.StatusCode)
	}
}

func TestMock_MethodHelpers(t *testing.T) {
	tests := []struct {
		name   string
		method Method
		want   string
	}{
		{"GET", GET("/p"), http.MethodGet},
		{"POST", POST("/p"), http.MethodPost},
		{"PUT", PUT("/p"), http.MethodPut},
		{"DELETE", DELETE("/p"), http.MethodDelete},
		{"PATCH", PATCH("/p"), http.MethodPatch},
		{"HEAD", HEAD("/p"), http.MethodHead},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.method.method != tt.want {
				t.Errorf("method = %q, want %q", tt.method.method, tt.want)
			}
			if tt.method.path != "/p" {
				t.Errorf("path = %q, want %q", tt.method.path, "/p")
			}
		})
	}
}

func TestMock_EmptyStubs(t *testing.T) {
	// No stubs — every request should 404.
	srv := Mock()
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/anything")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 404 {
		t.Errorf("status = %d, want 404", resp.StatusCode)
	}
}

func TestMock_PUTRequest(t *testing.T) {
	srv := Mock(
		When(PUT("/api/users/1")).
			Respond(200, JSON(`{"id": 1, "updated": true}`)).
			Build(),
	)
	defer srv.Close()

	req, err := http.NewRequest(http.MethodPut, srv.URL+"/api/users/1", strings.NewReader(`{"name":"updated"}`))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		t.Errorf("status = %d, want 200", resp.StatusCode)
	}
}

func TestMock_DELETERequest(t *testing.T) {
	srv := Mock(
		When(DELETE("/api/users/1")).
			Respond(204).
			Build(),
	)
	defer srv.Close()

	req, err := http.NewRequest(http.MethodDelete, srv.URL+"/api/users/1", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 204 {
		t.Errorf("status = %d, want 204", resp.StatusCode)
	}
}

func TestMock_PATCHRequest(t *testing.T) {
	srv := Mock(
		When(PATCH("/api/users/1")).
			Respond(200, JSON(`{"patched": true}`)).
			Build(),
	)
	defer srv.Close()

	req, err := http.NewRequest(http.MethodPatch, srv.URL+"/api/users/1", strings.NewReader(`{"name":"patched"}`))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		t.Errorf("status = %d, want 200", resp.StatusCode)
	}
}
