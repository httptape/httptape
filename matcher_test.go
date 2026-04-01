package httptape

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// --- Individual criterion tests ---

func TestMatchMethod(t *testing.T) {
	criterion := MatchMethod()

	tests := []struct {
		name       string
		reqMethod  string
		tapeMethod string
		wantScore  int
	}{
		{"GET matches GET", "GET", "GET", 1},
		{"POST matches POST", "POST", "POST", 1},
		{"GET does not match POST", "GET", "POST", 0},
		{"case sensitive", "get", "GET", 0},
		{"DELETE matches DELETE", "DELETE", "DELETE", 1},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(tt.reqMethod, "/test", nil)
			tape := Tape{Request: RecordedReq{Method: tt.tapeMethod}}
			got := criterion(req, tape)
			if got != tt.wantScore {
				t.Errorf("MatchMethod() = %d, want %d", got, tt.wantScore)
			}
		})
	}
}

func TestMatchPath(t *testing.T) {
	criterion := MatchPath()

	tests := []struct {
		name      string
		reqPath   string
		tapeURL   string
		wantScore int
	}{
		{"exact path match", "/users", "https://api.example.com/users", 2},
		{"path with query string", "/users", "https://api.example.com/users?page=1", 2},
		{"different paths", "/users", "https://api.example.com/accounts", 0},
		{"root path", "/", "https://api.example.com/", 2},
		{"nested path", "/api/v1/users", "https://api.example.com/api/v1/users", 2},
		{"path-only tape URL", "/users", "/users", 2},
		{"path-only with query", "/users", "/users?page=1", 2},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest("GET", tt.reqPath, nil)
			tape := Tape{Request: RecordedReq{URL: tt.tapeURL}}
			got := criterion(req, tape)
			if got != tt.wantScore {
				t.Errorf("MatchPath() = %d, want %d", got, tt.wantScore)
			}
		})
	}
}

func TestMatchPath_UnparsableURL(t *testing.T) {
	criterion := MatchPath()
	req := httptest.NewRequest("GET", "/users", nil)
	tape := Tape{Request: RecordedReq{URL: "://not-a-url"}}
	got := criterion(req, tape)
	if got != 0 {
		t.Errorf("MatchPath() with unparsable URL = %d, want 0", got)
	}
}

func TestMatchRoute(t *testing.T) {
	tests := []struct {
		name      string
		route     string
		tapeRoute string
		wantScore int
	}{
		{"matching route", "users-api", "users-api", 1},
		{"different route", "users-api", "auth-api", 0},
		{"empty tape route", "users-api", "", 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			criterion := MatchRoute(tt.route)
			req := httptest.NewRequest("GET", "/test", nil)
			tape := Tape{Route: tt.tapeRoute}
			got := criterion(req, tape)
			if got != tt.wantScore {
				t.Errorf("MatchRoute(%q) = %d, want %d", tt.route, got, tt.wantScore)
			}
		})
	}
}

func TestMatchRoute_EmptyFilter(t *testing.T) {
	criterion := MatchRoute("")
	req := httptest.NewRequest("GET", "/test", nil)

	tests := []struct {
		name      string
		tapeRoute string
	}{
		{"any route", "users-api"},
		{"empty route", ""},
		{"another route", "auth-api"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tape := Tape{Route: tt.tapeRoute}
			got := criterion(req, tape)
			if got != 1 {
				t.Errorf("MatchRoute(\"\") with tape route %q = %d, want 1", tt.tapeRoute, got)
			}
		})
	}
}

func TestMatchQueryParams(t *testing.T) {
	criterion := MatchQueryParams()

	tests := []struct {
		name      string
		reqURL    string
		tapeURL   string
		wantScore int
	}{
		{
			"all params match",
			"/users?page=1&limit=10",
			"https://api.example.com/users?page=1&limit=10",
			4,
		},
		{
			"subset match - extra tape params ok",
			"/users?page=1",
			"https://api.example.com/users?page=1&limit=10",
			4,
		},
		{
			"param value mismatch",
			"/users?page=2",
			"https://api.example.com/users?page=1",
			0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest("GET", tt.reqURL, nil)
			tape := Tape{Request: RecordedReq{URL: tt.tapeURL}}
			got := criterion(req, tape)
			if got != tt.wantScore {
				t.Errorf("MatchQueryParams() = %d, want %d", got, tt.wantScore)
			}
		})
	}
}

func TestMatchQueryParams_NoRequestParams(t *testing.T) {
	criterion := MatchQueryParams()
	req := httptest.NewRequest("GET", "/users", nil)
	tape := Tape{Request: RecordedReq{URL: "https://api.example.com/users?page=1"}}
	got := criterion(req, tape)
	if got != 4 {
		t.Errorf("MatchQueryParams() with no request params = %d, want 4", got)
	}
}

func TestMatchQueryParams_Missing(t *testing.T) {
	criterion := MatchQueryParams()
	req := httptest.NewRequest("GET", "/users?page=1&sort=asc", nil)
	tape := Tape{Request: RecordedReq{URL: "https://api.example.com/users?page=1"}}
	got := criterion(req, tape)
	if got != 0 {
		t.Errorf("MatchQueryParams() with missing tape param = %d, want 0", got)
	}
}

func TestMatchQueryParams_UnparsableURL(t *testing.T) {
	criterion := MatchQueryParams()
	req := httptest.NewRequest("GET", "/users?page=1", nil)
	tape := Tape{Request: RecordedReq{URL: "://not-a-url"}}
	got := criterion(req, tape)
	if got != 0 {
		t.Errorf("MatchQueryParams() with unparsable URL = %d, want 0", got)
	}
}

func TestMatchBodyHash_Match(t *testing.T) {
	criterion := MatchBodyHash()
	body := []byte("hello world")
	hash := BodyHashFromBytes(body)

	req := httptest.NewRequest("POST", "/test", bytes.NewReader(body))
	tape := Tape{Request: RecordedReq{BodyHash: hash}}
	got := criterion(req, tape)
	if got != 8 {
		t.Errorf("MatchBodyHash() = %d, want 8", got)
	}
}

func TestMatchBodyHash_Mismatch(t *testing.T) {
	criterion := MatchBodyHash()
	reqBody := []byte("hello world")
	tapeHash := BodyHashFromBytes([]byte("different body"))

	req := httptest.NewRequest("POST", "/test", bytes.NewReader(reqBody))
	tape := Tape{Request: RecordedReq{BodyHash: tapeHash}}
	got := criterion(req, tape)
	if got != 0 {
		t.Errorf("MatchBodyHash() = %d, want 0", got)
	}
}

func TestMatchBodyHash_BothEmpty(t *testing.T) {
	criterion := MatchBodyHash()
	req := httptest.NewRequest("GET", "/test", nil)
	tape := Tape{Request: RecordedReq{BodyHash: ""}}
	got := criterion(req, tape)
	if got != 8 {
		t.Errorf("MatchBodyHash() both empty = %d, want 8", got)
	}
}

func TestMatchBodyHash_RequestEmpty_TapeNot(t *testing.T) {
	criterion := MatchBodyHash()
	req := httptest.NewRequest("GET", "/test", nil)
	tape := Tape{Request: RecordedReq{BodyHash: "abc123"}}
	got := criterion(req, tape)
	if got != 0 {
		t.Errorf("MatchBodyHash() request empty, tape not = %d, want 0", got)
	}
}

func TestMatchBodyHash_RequestNotEmpty_TapeEmpty(t *testing.T) {
	criterion := MatchBodyHash()
	req := httptest.NewRequest("POST", "/test", strings.NewReader("some body"))
	tape := Tape{Request: RecordedReq{BodyHash: ""}}
	got := criterion(req, tape)
	if got != 0 {
		t.Errorf("MatchBodyHash() request not empty, tape empty = %d, want 0", got)
	}
}

func TestMatchBodyHash_BodyRestored(t *testing.T) {
	criterion := MatchBodyHash()
	body := []byte("hello world")
	hash := BodyHashFromBytes(body)

	req := httptest.NewRequest("POST", "/test", bytes.NewReader(body))
	tape := Tape{Request: RecordedReq{BodyHash: hash}}

	// First call should match.
	got := criterion(req, tape)
	if got != 8 {
		t.Fatalf("MatchBodyHash() first call = %d, want 8", got)
	}

	// Body should still be readable after criterion runs.
	restored, err := io.ReadAll(req.Body)
	if err != nil {
		t.Fatalf("reading restored body: %v", err)
	}
	if !bytes.Equal(restored, body) {
		t.Errorf("restored body = %q, want %q", restored, body)
	}
}

// --- CompositeMatcher tests ---

func TestCompositeMatcher_DefaultMatcher(t *testing.T) {
	m := DefaultMatcher()

	candidates := []Tape{
		{Request: RecordedReq{Method: "POST", URL: "/users"}},
		{Request: RecordedReq{Method: "GET", URL: "/users"}},
		{Request: RecordedReq{Method: "GET", URL: "/accounts"}},
	}

	req := httptest.NewRequest("GET", "/users", nil)
	tape, ok := m.Match(req, candidates)
	if !ok {
		t.Fatal("expected a match")
	}
	if tape.Request.Method != "GET" || tape.Request.URL != "/users" {
		t.Errorf("got method=%s url=%s, want GET /users", tape.Request.Method, tape.Request.URL)
	}
}

func TestCompositeMatcher_NoCandidates(t *testing.T) {
	m := DefaultMatcher()
	req := httptest.NewRequest("GET", "/users", nil)
	_, ok := m.Match(req, []Tape{})
	if ok {
		t.Error("expected no match with empty candidates")
	}
}

func TestCompositeMatcher_NoCriteria(t *testing.T) {
	m := NewCompositeMatcher()
	req := httptest.NewRequest("GET", "/users", nil)
	candidates := []Tape{
		{Request: RecordedReq{Method: "GET", URL: "/users"}},
	}
	_, ok := m.Match(req, candidates)
	if ok {
		t.Error("expected no match with no criteria")
	}
}

func TestCompositeMatcher_NoMatch(t *testing.T) {
	m := DefaultMatcher()
	candidates := []Tape{
		{Request: RecordedReq{Method: "POST", URL: "/accounts"}},
		{Request: RecordedReq{Method: "DELETE", URL: "/users"}},
	}
	req := httptest.NewRequest("GET", "/users", nil)
	_, ok := m.Match(req, candidates)
	if ok {
		t.Error("expected no match when all candidates eliminated")
	}
}

func TestCompositeMatcher_Priority(t *testing.T) {
	body := []byte("request body")
	hash := BodyHashFromBytes(body)

	m := NewCompositeMatcher(MatchMethod(), MatchPath(), MatchBodyHash())

	candidates := []Tape{
		{
			ID:      "no-body",
			Request: RecordedReq{Method: "POST", URL: "/users", BodyHash: ""},
		},
		{
			ID:      "with-body",
			Request: RecordedReq{Method: "POST", URL: "/users", BodyHash: hash},
		},
	}

	req := httptest.NewRequest("POST", "/users", bytes.NewReader(body))
	tape, ok := m.Match(req, candidates)
	if !ok {
		t.Fatal("expected a match")
	}
	// The tape with body hash should win (score 1+2+8=11 vs eliminated for no-body)
	if tape.ID != "with-body" {
		t.Errorf("got tape ID=%s, want with-body", tape.ID)
	}
}

func TestCompositeMatcher_StableOrdering(t *testing.T) {
	m := DefaultMatcher()

	candidates := []Tape{
		{ID: "first", Request: RecordedReq{Method: "GET", URL: "/users"}},
		{ID: "second", Request: RecordedReq{Method: "GET", URL: "/users"}},
	}

	req := httptest.NewRequest("GET", "/users", nil)
	tape, ok := m.Match(req, candidates)
	if !ok {
		t.Fatal("expected a match")
	}
	if tape.ID != "first" {
		t.Errorf("got tape ID=%s, want first (stable ordering)", tape.ID)
	}
}

func TestCompositeMatcher_ShortCircuit(t *testing.T) {
	called := false
	trackingCriterion := MatchCriterion(func(_ *http.Request, _ Tape) int {
		called = true
		return 1
	})

	// Put a criterion that always returns 0 first, then a tracking one.
	alwaysFail := MatchCriterion(func(_ *http.Request, _ Tape) int { return 0 })
	m := NewCompositeMatcher(alwaysFail, trackingCriterion)

	req := httptest.NewRequest("GET", "/users", nil)
	candidates := []Tape{{Request: RecordedReq{Method: "GET", URL: "/users"}}}
	m.Match(req, candidates)

	if called {
		t.Error("tracking criterion should not have been called after short-circuit")
	}
}

func TestCompositeMatcher_FullComposition(t *testing.T) {
	body := []byte("important data")
	hash := BodyHashFromBytes(body)

	m := NewCompositeMatcher(
		MatchMethod(),
		MatchPath(),
		MatchRoute("users-api"),
		MatchQueryParams(),
		MatchBodyHash(),
	)

	candidates := []Tape{
		{
			ID:    "method-only",
			Route: "other-api",
			Request: RecordedReq{
				Method:   "POST",
				URL:      "/api/users?page=1",
				BodyHash: hash,
			},
		},
		{
			ID:    "method-path",
			Route: "other-api",
			Request: RecordedReq{
				Method:   "POST",
				URL:      "/api/users?page=1",
				BodyHash: hash,
			},
		},
		{
			ID:    "full-match",
			Route: "users-api",
			Request: RecordedReq{
				Method:   "POST",
				URL:      "https://api.example.com/api/users?page=1",
				BodyHash: hash,
			},
		},
		{
			ID:    "wrong-method",
			Route: "users-api",
			Request: RecordedReq{
				Method:   "GET",
				URL:      "https://api.example.com/api/users?page=1",
				BodyHash: hash,
			},
		},
	}

	req := httptest.NewRequest("POST", "/api/users?page=1", bytes.NewReader(body))
	tape, ok := m.Match(req, candidates)
	if !ok {
		t.Fatal("expected a match")
	}
	if tape.ID != "full-match" {
		t.Errorf("got tape ID=%s, want full-match", tape.ID)
	}
}

// --- DefaultMatcher integration tests ---

func TestDefaultMatcher_BasicMatch(t *testing.T) {
	m := DefaultMatcher()
	candidates := []Tape{
		{ID: "t1", Request: RecordedReq{Method: "GET", URL: "https://api.example.com/users"}},
	}
	req := httptest.NewRequest("GET", "/users", nil)
	tape, ok := m.Match(req, candidates)
	if !ok {
		t.Fatal("expected a match")
	}
	if tape.ID != "t1" {
		t.Errorf("got tape ID=%s, want t1", tape.ID)
	}
}

func TestDefaultMatcher_MethodMismatch(t *testing.T) {
	m := DefaultMatcher()
	candidates := []Tape{
		{Request: RecordedReq{Method: "POST", URL: "https://api.example.com/users"}},
	}
	req := httptest.NewRequest("GET", "/users", nil)
	_, ok := m.Match(req, candidates)
	if ok {
		t.Error("expected no match when method differs")
	}
}

func TestDefaultMatcher_PathMismatch(t *testing.T) {
	m := DefaultMatcher()
	candidates := []Tape{
		{Request: RecordedReq{Method: "GET", URL: "https://api.example.com/accounts"}},
	}
	req := httptest.NewRequest("GET", "/users", nil)
	_, ok := m.Match(req, candidates)
	if ok {
		t.Error("expected no match when path differs")
	}
}

func TestDefaultMatcher_MultipleMatches(t *testing.T) {
	m := DefaultMatcher()
	candidates := []Tape{
		{ID: "first", Request: RecordedReq{Method: "GET", URL: "https://api.example.com/users?page=1"}},
		{ID: "second", Request: RecordedReq{Method: "GET", URL: "https://api.example.com/users?page=2"}},
	}
	req := httptest.NewRequest("GET", "/users", nil)
	tape, ok := m.Match(req, candidates)
	if !ok {
		t.Fatal("expected a match")
	}
	// DefaultMatcher does not check query params, so first one wins.
	if tape.ID != "first" {
		t.Errorf("got tape ID=%s, want first", tape.ID)
	}
}

// --- Helper function tests ---

func TestStringSlicesEqual(t *testing.T) {
	tests := []struct {
		name string
		a    []string
		b    []string
		want bool
	}{
		{"both empty", nil, nil, true},
		{"equal", []string{"a", "b"}, []string{"a", "b"}, true},
		{"different length", []string{"a"}, []string{"a", "b"}, false},
		{"different values", []string{"a"}, []string{"b"}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := stringSlicesEqual(tt.a, tt.b); got != tt.want {
				t.Errorf("stringSlicesEqual() = %v, want %v", got, tt.want)
			}
		})
	}
}

// --- MatchPathRegex tests ---

func TestMatchPathRegex_Match(t *testing.T) {
	criterion, err := MatchPathRegex(`^/users/\d+/orders$`)
	if err != nil {
		t.Fatalf("MatchPathRegex() error = %v", err)
	}

	req := httptest.NewRequest("GET", "/users/456/orders", nil)
	tape := Tape{Request: RecordedReq{URL: "https://api.example.com/users/123/orders"}}
	got := criterion(req, tape)
	if got != 1 {
		t.Errorf("MatchPathRegex() = %d, want 1", got)
	}
}

func TestMatchPathRegex_RequestNoMatch(t *testing.T) {
	criterion, err := MatchPathRegex(`^/users/\d+/orders$`)
	if err != nil {
		t.Fatalf("MatchPathRegex() error = %v", err)
	}

	req := httptest.NewRequest("GET", "/products/1", nil)
	tape := Tape{Request: RecordedReq{URL: "https://api.example.com/users/123/orders"}}
	got := criterion(req, tape)
	if got != 0 {
		t.Errorf("MatchPathRegex() = %d, want 0", got)
	}
}

func TestMatchPathRegex_TapeNoMatch(t *testing.T) {
	criterion, err := MatchPathRegex(`^/users/\d+/orders$`)
	if err != nil {
		t.Fatalf("MatchPathRegex() error = %v", err)
	}

	req := httptest.NewRequest("GET", "/users/456/orders", nil)
	tape := Tape{Request: RecordedReq{URL: "https://api.example.com/products/42"}}
	got := criterion(req, tape)
	if got != 0 {
		t.Errorf("MatchPathRegex() = %d, want 0", got)
	}
}

func TestMatchPathRegex_UnparsableTapeURL(t *testing.T) {
	criterion, err := MatchPathRegex(`^/users/\d+$`)
	if err != nil {
		t.Fatalf("MatchPathRegex() error = %v", err)
	}

	req := httptest.NewRequest("GET", "/users/123", nil)
	tape := Tape{Request: RecordedReq{URL: "://not-a-url"}}
	got := criterion(req, tape)
	if got != 0 {
		t.Errorf("MatchPathRegex() with unparsable URL = %d, want 0", got)
	}
}

func TestMatchPathRegex_InvalidPattern(t *testing.T) {
	criterion, err := MatchPathRegex("[invalid")
	if err == nil {
		t.Fatal("expected error for invalid regex pattern")
	}
	if criterion != nil {
		t.Error("expected nil criterion for invalid regex pattern")
	}
}

func TestMatchPathRegex_BroadPattern(t *testing.T) {
	criterion, err := MatchPathRegex(`.*`)
	if err != nil {
		t.Fatalf("MatchPathRegex() error = %v", err)
	}

	req := httptest.NewRequest("GET", "/anything/at/all", nil)
	tape := Tape{Request: RecordedReq{URL: "https://api.example.com/something/else"}}
	got := criterion(req, tape)
	if got != 1 {
		t.Errorf("MatchPathRegex(\".*\") = %d, want 1", got)
	}
}

func TestMatchPathRegex_AnchoredPattern(t *testing.T) {
	tests := []struct {
		name      string
		pattern   string
		reqPath   string
		tapeURL   string
		wantScore int
	}{
		{
			"unanchored matches partial",
			`/users/\d+`,
			"/users/123/extra",
			"https://api.example.com/users/456/extra",
			1,
		},
		{
			"anchored rejects partial",
			`^/users/\d+$`,
			"/users/123/extra",
			"https://api.example.com/users/456/extra",
			0,
		},
		{
			"anchored matches exact",
			`^/users/\d+$`,
			"/users/123",
			"https://api.example.com/users/456",
			1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			criterion, err := MatchPathRegex(tt.pattern)
			if err != nil {
				t.Fatalf("MatchPathRegex(%q) error = %v", tt.pattern, err)
			}
			req := httptest.NewRequest("GET", tt.reqPath, nil)
			tape := Tape{Request: RecordedReq{URL: tt.tapeURL}}
			got := criterion(req, tape)
			if got != tt.wantScore {
				t.Errorf("MatchPathRegex(%q) = %d, want %d", tt.pattern, got, tt.wantScore)
			}
		})
	}
}

func TestMatchPathRegex_EmptyPattern(t *testing.T) {
	criterion, err := MatchPathRegex("")
	if err != nil {
		t.Fatalf("MatchPathRegex(\"\") error = %v", err)
	}

	req := httptest.NewRequest("GET", "/anything", nil)
	tape := Tape{Request: RecordedReq{URL: "https://api.example.com/whatever"}}
	got := criterion(req, tape)
	if got != 1 {
		t.Errorf("MatchPathRegex(\"\") = %d, want 1", got)
	}
}

func TestCompositeMatcher_RegexPath(t *testing.T) {
	criterion, err := MatchPathRegex(`^/users/\d+/orders$`)
	if err != nil {
		t.Fatalf("MatchPathRegex() error = %v", err)
	}

	m := NewCompositeMatcher(MatchMethod(), criterion)

	candidates := []Tape{
		{ID: "user-orders", Request: RecordedReq{Method: "GET", URL: "https://api.example.com/users/123/orders"}},
		{ID: "products", Request: RecordedReq{Method: "GET", URL: "https://api.example.com/products/42"}},
		{ID: "user-profile", Request: RecordedReq{Method: "GET", URL: "https://api.example.com/users/123"}},
	}

	req := httptest.NewRequest("GET", "/users/456/orders", nil)
	tape, ok := m.Match(req, candidates)
	if !ok {
		t.Fatal("expected a match")
	}
	if tape.ID != "user-orders" {
		t.Errorf("got tape ID=%s, want user-orders", tape.ID)
	}
}

func TestCompositeMatcher_ExactBeatsRegex(t *testing.T) {
	// Exact matcher: MatchMethod (1) + MatchPath (2) = 3
	exactMatcher := NewCompositeMatcher(MatchMethod(), MatchPath())

	// Regex matcher: MatchMethod (1) + MatchPathRegex (1) = 2
	regexCriterion, err := MatchPathRegex(`^/users/\d+$`)
	if err != nil {
		t.Fatalf("MatchPathRegex() error = %v", err)
	}
	regexMatcher := NewCompositeMatcher(MatchMethod(), regexCriterion)

	candidates := []Tape{
		{ID: "user-123", Request: RecordedReq{Method: "GET", URL: "https://api.example.com/users/123"}},
	}

	req := httptest.NewRequest("GET", "/users/123", nil)

	exactTape, exactOk := exactMatcher.Match(req, candidates)
	regexTape, regexOk := regexMatcher.Match(req, candidates)

	if !exactOk || !regexOk {
		t.Fatal("both matchers should find a match")
	}

	// Both match the same tape, but exact matcher should produce a higher score.
	// We verify this indirectly: exact matcher score = 3 (method 1 + path 2),
	// regex matcher score = 2 (method 1 + regex 1).
	// The key property is that exact > regex, which is the design intent.
	if exactTape.ID != "user-123" || regexTape.ID != "user-123" {
		t.Errorf("exact=%s regex=%s, both should be user-123", exactTape.ID, regexTape.ID)
	}

	// Verify scores directly by running criteria.
	exactPathScore := MatchPath()(req, candidates[0])
	regexPathScore := regexCriterion(req, candidates[0])
	if exactPathScore <= regexPathScore {
		t.Errorf("MatchPath score (%d) should be greater than MatchPathRegex score (%d)",
			exactPathScore, regexPathScore)
	}
}

// --- Server integration: verify DefaultMatcher is used by default ---

func TestServer_UsesDefaultMatcher(t *testing.T) {
	// This test verifies the server uses DefaultMatcher by default,
	// which parses tape URLs properly (unlike ExactMatcher which compared raw strings).
	store := NewMemoryStore()
	tape := Tape{
		ID: "test-tape",
		Request: RecordedReq{
			Method: "GET",
			URL:    "https://api.example.com/users",
		},
		Response: RecordedResp{
			StatusCode: http.StatusOK,
			Body:       []byte("ok"),
		},
	}
	ctx := t.Context()
	if err := store.Save(ctx, tape); err != nil {
		t.Fatalf("saving tape: %v", err)
	}

	srv := NewServer(store)
	w := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/users", nil)
	srv.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want %d", w.Code, http.StatusOK)
	}
	if got := w.Body.String(); got != "ok" {
		t.Errorf("body = %q, want %q", got, "ok")
	}
}
