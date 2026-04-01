package httptape

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
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

// --- MatchHeaders tests ---

func TestMatchHeaders_SingleHeader(t *testing.T) {
	criterion := MatchHeaders("Content-Type", "application/json")

	req := httptest.NewRequest("GET", "/test", nil)
	req.Header.Set("Content-Type", "application/json")
	tape := Tape{Request: RecordedReq{
		Method:  "GET",
		URL:     "/test",
		Headers: http.Header{"Content-Type": {"application/json"}},
	}}

	got := criterion(req, tape)
	if got != 3 {
		t.Errorf("MatchHeaders() = %d, want 3", got)
	}
}

func TestMatchHeaders_CaseInsensitiveName(t *testing.T) {
	tests := []struct {
		name string
		key  string
	}{
		{"lowercase", "content-type"},
		{"uppercase", "CONTENT-TYPE"},
		{"canonical", "Content-Type"},
		{"mixed", "cOnTeNt-TyPe"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			criterion := MatchHeaders(tt.key, "application/json")

			req := httptest.NewRequest("GET", "/test", nil)
			req.Header.Set("Content-Type", "application/json")
			tape := Tape{Request: RecordedReq{
				Headers: http.Header{"Content-Type": {"application/json"}},
			}}

			got := criterion(req, tape)
			if got != 3 {
				t.Errorf("MatchHeaders(%q, ...) = %d, want 3", tt.key, got)
			}
		})
	}
}

func TestMatchHeaders_CaseSensitiveValue(t *testing.T) {
	criterion := MatchHeaders("Accept", "Application/JSON")

	req := httptest.NewRequest("GET", "/test", nil)
	req.Header.Set("Accept", "application/json")
	tape := Tape{Request: RecordedReq{
		Headers: http.Header{"Accept": {"application/json"}},
	}}

	got := criterion(req, tape)
	if got != 0 {
		t.Errorf("MatchHeaders() with case mismatch value = %d, want 0", got)
	}
}

func TestMatchHeaders_HeaderNotInRequest(t *testing.T) {
	criterion := MatchHeaders("X-Custom", "value")

	req := httptest.NewRequest("GET", "/test", nil)
	// No X-Custom header set on request.
	tape := Tape{Request: RecordedReq{
		Headers: http.Header{"X-Custom": {"value"}},
	}}

	got := criterion(req, tape)
	if got != 0 {
		t.Errorf("MatchHeaders() header not in request = %d, want 0", got)
	}
}

func TestMatchHeaders_HeaderNotInTape(t *testing.T) {
	criterion := MatchHeaders("X-Custom", "value")

	req := httptest.NewRequest("GET", "/test", nil)
	req.Header.Set("X-Custom", "value")
	tape := Tape{Request: RecordedReq{
		Headers: http.Header{},
	}}

	got := criterion(req, tape)
	if got != 0 {
		t.Errorf("MatchHeaders() header not in tape = %d, want 0", got)
	}
}

func TestMatchHeaders_WrongValueInRequest(t *testing.T) {
	criterion := MatchHeaders("Accept", "application/xml")

	req := httptest.NewRequest("GET", "/test", nil)
	req.Header.Set("Accept", "application/json")
	tape := Tape{Request: RecordedReq{
		Headers: http.Header{"Accept": {"application/xml"}},
	}}

	got := criterion(req, tape)
	if got != 0 {
		t.Errorf("MatchHeaders() wrong value in request = %d, want 0", got)
	}
}

func TestMatchHeaders_WrongValueInTape(t *testing.T) {
	criterion := MatchHeaders("Accept", "application/xml")

	req := httptest.NewRequest("GET", "/test", nil)
	req.Header.Set("Accept", "application/xml")
	tape := Tape{Request: RecordedReq{
		Headers: http.Header{"Accept": {"application/json"}},
	}}

	got := criterion(req, tape)
	if got != 0 {
		t.Errorf("MatchHeaders() wrong value in tape = %d, want 0", got)
	}
}

func TestMatchHeaders_MultiValuedHeader_AnyOf(t *testing.T) {
	criterion := MatchHeaders("Accept", "application/json")

	req := httptest.NewRequest("GET", "/test", nil)
	req.Header.Add("Accept", "text/html")
	req.Header.Add("Accept", "application/json")
	tape := Tape{Request: RecordedReq{
		Headers: http.Header{"Accept": {"text/html", "application/json"}},
	}}

	got := criterion(req, tape)
	if got != 3 {
		t.Errorf("MatchHeaders() multi-valued any-of = %d, want 3", got)
	}
}

func TestMatchHeaders_MultiValuedHeader_NotPresent(t *testing.T) {
	criterion := MatchHeaders("Accept", "application/xml")

	req := httptest.NewRequest("GET", "/test", nil)
	req.Header.Add("Accept", "text/html")
	req.Header.Add("Accept", "application/json")
	tape := Tape{Request: RecordedReq{
		Headers: http.Header{"Accept": {"text/html", "application/json"}},
	}}

	got := criterion(req, tape)
	if got != 0 {
		t.Errorf("MatchHeaders() multi-valued not present = %d, want 0", got)
	}
}

func TestMatchHeaders_NilTapeHeaders(t *testing.T) {
	criterion := MatchHeaders("X-Custom", "value")

	req := httptest.NewRequest("GET", "/test", nil)
	req.Header.Set("X-Custom", "value")
	tape := Tape{Request: RecordedReq{}}

	got := criterion(req, tape)
	if got != 0 {
		t.Errorf("MatchHeaders() nil tape headers = %d, want 0", got)
	}
}

func TestMatchHeaders_MultipleCriteria_AND(t *testing.T) {
	m := NewCompositeMatcher(
		MatchMethod(),
		MatchPath(),
		MatchHeaders("Accept", "application/json"),
		MatchHeaders("X-Api-Version", "v2"),
	)

	candidates := []Tape{
		{
			ID: "v1-json",
			Request: RecordedReq{
				Method:  "GET",
				URL:     "/api/data",
				Headers: http.Header{"Accept": {"application/json"}, "X-Api-Version": {"v1"}},
			},
		},
		{
			ID: "v2-json",
			Request: RecordedReq{
				Method:  "GET",
				URL:     "/api/data",
				Headers: http.Header{"Accept": {"application/json"}, "X-Api-Version": {"v2"}},
			},
		},
		{
			ID: "v2-xml",
			Request: RecordedReq{
				Method:  "GET",
				URL:     "/api/data",
				Headers: http.Header{"Accept": {"application/xml"}, "X-Api-Version": {"v2"}},
			},
		},
	}

	req := httptest.NewRequest("GET", "/api/data", nil)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("X-Api-Version", "v2")

	tape, ok := m.Match(req, candidates)
	if !ok {
		t.Fatal("expected a match")
	}
	if tape.ID != "v2-json" {
		t.Errorf("got tape ID=%s, want v2-json", tape.ID)
	}
}

func TestMatchHeaders_ScoreStacking(t *testing.T) {
	// Two header criteria should contribute 6 total (3 + 3).
	c1 := MatchHeaders("Accept", "application/json")
	c2 := MatchHeaders("X-Api-Version", "v2")

	req := httptest.NewRequest("GET", "/test", nil)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("X-Api-Version", "v2")
	tape := Tape{Request: RecordedReq{
		Headers: http.Header{
			"Accept":        {"application/json"},
			"X-Api-Version": {"v2"},
		},
	}}

	s1 := c1(req, tape)
	s2 := c2(req, tape)
	if s1+s2 != 6 {
		t.Errorf("stacked header scores = %d, want 6", s1+s2)
	}
}

func TestHeaderContains(t *testing.T) {
	tests := []struct {
		name         string
		h            http.Header
		canonicalKey string
		value        string
		want         bool
	}{
		{"found", http.Header{"Accept": {"application/json"}}, "Accept", "application/json", true},
		{"not found", http.Header{"Accept": {"text/html"}}, "Accept", "application/json", false},
		{"multi-value found", http.Header{"Accept": {"text/html", "application/json"}}, "Accept", "application/json", true},
		{"missing key", http.Header{}, "Accept", "application/json", false},
		{"nil header", nil, "Accept", "application/json", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := headerContains(tt.h, tt.canonicalKey, tt.value)
			if got != tt.want {
				t.Errorf("headerContains() = %v, want %v", got, tt.want)
			}
		})
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

// --- MatchBodyFuzzy tests ---

func TestMatchBodyFuzzy_SingleField(t *testing.T) {
	criterion := MatchBodyFuzzy("$.action")

	req := httptest.NewRequest("POST", "/test",
		strings.NewReader(`{"action":"create","timestamp":"2026-01-01T00:00:00Z"}`))
	tape := Tape{Request: RecordedReq{
		Body: []byte(`{"action":"create","timestamp":"2025-06-15T12:00:00Z"}`),
	}}

	got := criterion(req, tape)
	if got != 6 {
		t.Errorf("MatchBodyFuzzy() = %d, want 6", got)
	}
}

func TestMatchBodyFuzzy_MultipleFields(t *testing.T) {
	criterion := MatchBodyFuzzy("$.action", "$.user")

	req := httptest.NewRequest("POST", "/test",
		strings.NewReader(`{"action":"create","user":"alice","nonce":"abc123"}`))
	tape := Tape{Request: RecordedReq{
		Body: []byte(`{"action":"create","user":"alice","nonce":"xyz789"}`),
	}}

	got := criterion(req, tape)
	if got != 6 {
		t.Errorf("MatchBodyFuzzy() = %d, want 6", got)
	}
}

func TestMatchBodyFuzzy_NestedField(t *testing.T) {
	criterion := MatchBodyFuzzy("$.user.id")

	req := httptest.NewRequest("POST", "/test",
		strings.NewReader(`{"user":{"id":42,"session":"s1"}}`))
	tape := Tape{Request: RecordedReq{
		Body: []byte(`{"user":{"id":42,"session":"s2"}}`),
	}}

	got := criterion(req, tape)
	if got != 6 {
		t.Errorf("MatchBodyFuzzy() nested = %d, want 6", got)
	}
}

func TestMatchBodyFuzzy_ArrayWildcard(t *testing.T) {
	criterion := MatchBodyFuzzy("$.items[*].sku")

	req := httptest.NewRequest("POST", "/test",
		strings.NewReader(`{"items":[{"sku":"A1","qty":5},{"sku":"B2","qty":3}]}`))
	tape := Tape{Request: RecordedReq{
		Body: []byte(`{"items":[{"sku":"A1","qty":10},{"sku":"B2","qty":7}]}`),
	}}

	got := criterion(req, tape)
	if got != 6 {
		t.Errorf("MatchBodyFuzzy() array wildcard = %d, want 6", got)
	}
}

func TestMatchBodyFuzzy_FieldValueDiffers(t *testing.T) {
	criterion := MatchBodyFuzzy("$.action")

	req := httptest.NewRequest("POST", "/test",
		strings.NewReader(`{"action":"create"}`))
	tape := Tape{Request: RecordedReq{
		Body: []byte(`{"action":"delete"}`),
	}}

	got := criterion(req, tape)
	if got != 0 {
		t.Errorf("MatchBodyFuzzy() mismatch = %d, want 0", got)
	}
}

func TestMatchBodyFuzzy_NonJSONRequestBody(t *testing.T) {
	criterion := MatchBodyFuzzy("$.action")

	req := httptest.NewRequest("POST", "/test",
		strings.NewReader("not json"))
	tape := Tape{Request: RecordedReq{
		Body: []byte(`{"action":"create"}`),
	}}

	got := criterion(req, tape)
	if got != 0 {
		t.Errorf("MatchBodyFuzzy() non-JSON request = %d, want 0", got)
	}
}

func TestMatchBodyFuzzy_NonJSONTapeBody(t *testing.T) {
	criterion := MatchBodyFuzzy("$.action")

	req := httptest.NewRequest("POST", "/test",
		strings.NewReader(`{"action":"create"}`))
	tape := Tape{Request: RecordedReq{
		Body: []byte("not json"),
	}}

	got := criterion(req, tape)
	if got != 0 {
		t.Errorf("MatchBodyFuzzy() non-JSON tape = %d, want 0", got)
	}
}

func TestMatchBodyFuzzy_EmptyPaths(t *testing.T) {
	criterion := MatchBodyFuzzy()

	req := httptest.NewRequest("POST", "/test",
		strings.NewReader(`{"action":"create"}`))
	tape := Tape{Request: RecordedReq{
		Body: []byte(`{"action":"create"}`),
	}}

	got := criterion(req, tape)
	if got != 0 {
		t.Errorf("MatchBodyFuzzy() empty paths = %d, want 0", got)
	}
}

func TestMatchBodyFuzzy_PathInRequestNotInTape(t *testing.T) {
	criterion := MatchBodyFuzzy("$.action", "$.extra")

	req := httptest.NewRequest("POST", "/test",
		strings.NewReader(`{"action":"create","extra":"value"}`))
	tape := Tape{Request: RecordedReq{
		Body: []byte(`{"action":"create"}`),
	}}

	// "extra" is skipped (not in tape), "action" matches => score 6
	got := criterion(req, tape)
	if got != 6 {
		t.Errorf("MatchBodyFuzzy() path in req not tape = %d, want 6", got)
	}
}

func TestMatchBodyFuzzy_PathInTapeNotInRequest(t *testing.T) {
	criterion := MatchBodyFuzzy("$.action", "$.extra")

	req := httptest.NewRequest("POST", "/test",
		strings.NewReader(`{"action":"create"}`))
	tape := Tape{Request: RecordedReq{
		Body: []byte(`{"action":"create","extra":"value"}`),
	}}

	// "extra" is skipped (not in request), "action" matches => score 6
	got := criterion(req, tape)
	if got != 6 {
		t.Errorf("MatchBodyFuzzy() path in tape not req = %d, want 6", got)
	}
}

func TestMatchBodyFuzzy_BothBodiesEmpty(t *testing.T) {
	criterion := MatchBodyFuzzy("$.action")

	req := httptest.NewRequest("POST", "/test", nil)
	tape := Tape{Request: RecordedReq{Body: nil}}

	got := criterion(req, tape)
	if got != 0 {
		t.Errorf("MatchBodyFuzzy() both empty = %d, want 0", got)
	}
}

func TestMatchBodyFuzzy_InvalidPaths(t *testing.T) {
	// All paths invalid => parsed list is empty => returns 0
	criterion := MatchBodyFuzzy("not-a-path", "also-bad")

	req := httptest.NewRequest("POST", "/test",
		strings.NewReader(`{"action":"create"}`))
	tape := Tape{Request: RecordedReq{
		Body: []byte(`{"action":"create"}`),
	}}

	got := criterion(req, tape)
	if got != 0 {
		t.Errorf("MatchBodyFuzzy() invalid paths = %d, want 0", got)
	}
}

func TestMatchBodyFuzzy_AllPathsMissing(t *testing.T) {
	// Valid paths but none exist in either body => matched=0 => returns 0
	criterion := MatchBodyFuzzy("$.nonexistent")

	req := httptest.NewRequest("POST", "/test",
		strings.NewReader(`{"action":"create"}`))
	tape := Tape{Request: RecordedReq{
		Body: []byte(`{"action":"create"}`),
	}}

	got := criterion(req, tape)
	if got != 0 {
		t.Errorf("MatchBodyFuzzy() all paths missing = %d, want 0", got)
	}
}

func TestMatchBodyFuzzy_BodyRestored(t *testing.T) {
	criterion := MatchBodyFuzzy("$.action")

	body := `{"action":"create"}`
	req := httptest.NewRequest("POST", "/test", strings.NewReader(body))
	tape := Tape{Request: RecordedReq{
		Body: []byte(`{"action":"create"}`),
	}}

	criterion(req, tape)

	// Body should be restored for subsequent reads.
	restored, err := io.ReadAll(req.Body)
	if err != nil {
		t.Fatalf("reading restored body: %v", err)
	}
	if string(restored) != body {
		t.Errorf("restored body = %q, want %q", string(restored), body)
	}
}

func TestMatchBodyFuzzy_DeepNestedObject(t *testing.T) {
	criterion := MatchBodyFuzzy("$.a.b.c")

	req := httptest.NewRequest("POST", "/test",
		strings.NewReader(`{"a":{"b":{"c":"deep"}}}`))
	tape := Tape{Request: RecordedReq{
		Body: []byte(`{"a":{"b":{"c":"deep"}}}`),
	}}

	got := criterion(req, tape)
	if got != 6 {
		t.Errorf("MatchBodyFuzzy() deep nested = %d, want 6", got)
	}
}

func TestMatchBodyFuzzy_NumericValue(t *testing.T) {
	criterion := MatchBodyFuzzy("$.count")

	req := httptest.NewRequest("POST", "/test",
		strings.NewReader(`{"count":42}`))
	tape := Tape{Request: RecordedReq{
		Body: []byte(`{"count":42}`),
	}}

	got := criterion(req, tape)
	if got != 6 {
		t.Errorf("MatchBodyFuzzy() numeric = %d, want 6", got)
	}
}

func TestMatchBodyFuzzy_BooleanValue(t *testing.T) {
	criterion := MatchBodyFuzzy("$.active")

	req := httptest.NewRequest("POST", "/test",
		strings.NewReader(`{"active":true}`))
	tape := Tape{Request: RecordedReq{
		Body: []byte(`{"active":true}`),
	}}

	got := criterion(req, tape)
	if got != 6 {
		t.Errorf("MatchBodyFuzzy() boolean = %d, want 6", got)
	}
}

func TestMatchBodyFuzzy_NullValue(t *testing.T) {
	criterion := MatchBodyFuzzy("$.data")

	req := httptest.NewRequest("POST", "/test",
		strings.NewReader(`{"data":null}`))
	tape := Tape{Request: RecordedReq{
		Body: []byte(`{"data":null}`),
	}}

	got := criterion(req, tape)
	if got != 6 {
		t.Errorf("MatchBodyFuzzy() null = %d, want 6", got)
	}
}

func TestMatchBodyFuzzy_ObjectValue(t *testing.T) {
	// Comparing an entire nested object
	criterion := MatchBodyFuzzy("$.config")

	req := httptest.NewRequest("POST", "/test",
		strings.NewReader(`{"config":{"retries":3,"timeout":30},"id":"abc"}`))
	tape := Tape{Request: RecordedReq{
		Body: []byte(`{"config":{"retries":3,"timeout":30},"id":"xyz"}`),
	}}

	got := criterion(req, tape)
	if got != 6 {
		t.Errorf("MatchBodyFuzzy() object value = %d, want 6", got)
	}
}

func TestMatchBodyFuzzy_ArrayWildcard_DifferentValues(t *testing.T) {
	criterion := MatchBodyFuzzy("$.items[*].sku")

	req := httptest.NewRequest("POST", "/test",
		strings.NewReader(`{"items":[{"sku":"A1"},{"sku":"B2"}]}`))
	tape := Tape{Request: RecordedReq{
		Body: []byte(`{"items":[{"sku":"A1"},{"sku":"C3"}]}`),
	}}

	got := criterion(req, tape)
	if got != 0 {
		t.Errorf("MatchBodyFuzzy() array wildcard mismatch = %d, want 0", got)
	}
}

func TestMatchBodyFuzzy_ArrayWildcard_DifferentLengths(t *testing.T) {
	criterion := MatchBodyFuzzy("$.items[*].sku")

	req := httptest.NewRequest("POST", "/test",
		strings.NewReader(`{"items":[{"sku":"A1"},{"sku":"B2"}]}`))
	tape := Tape{Request: RecordedReq{
		Body: []byte(`{"items":[{"sku":"A1"}]}`),
	}}

	// Different array lengths produce different collected slices
	got := criterion(req, tape)
	if got != 0 {
		t.Errorf("MatchBodyFuzzy() array different lengths = %d, want 0", got)
	}
}

func TestMatchBodyFuzzy_Composability(t *testing.T) {
	m := NewCompositeMatcher(
		MatchMethod(),
		MatchPath(),
		MatchBodyFuzzy("$.action"),
	)

	candidates := []Tape{
		{
			ID: "create",
			Request: RecordedReq{
				Method: "POST",
				URL:    "/api/do",
				Body:   []byte(`{"action":"create","ts":"2025-01-01"}`),
			},
		},
		{
			ID: "delete",
			Request: RecordedReq{
				Method: "POST",
				URL:    "/api/do",
				Body:   []byte(`{"action":"delete","ts":"2025-02-01"}`),
			},
		},
	}

	req := httptest.NewRequest("POST", "/api/do",
		strings.NewReader(`{"action":"delete","ts":"2026-03-30"}`))
	tape, ok := m.Match(req, candidates)
	if !ok {
		t.Fatal("expected a match")
	}
	if tape.ID != "delete" {
		t.Errorf("got tape ID=%s, want delete", tape.ID)
	}
}

func TestMatchBodyFuzzy_WildcardNotArray(t *testing.T) {
	criterion := MatchBodyFuzzy("$.items[*].sku")

	req := httptest.NewRequest("POST", "/test",
		strings.NewReader(`{"items":"not-an-array"}`))
	tape := Tape{Request: RecordedReq{
		Body: []byte(`{"items":"not-an-array"}`),
	}}

	// items is not an array, so path extraction fails => skipped => matched=0 => 0
	got := criterion(req, tape)
	if got != 0 {
		t.Errorf("MatchBodyFuzzy() wildcard not array = %d, want 0", got)
	}
}

func TestMatchBodyFuzzy_WildcardMissingFieldInElement(t *testing.T) {
	criterion := MatchBodyFuzzy("$.items[*].sku")

	req := httptest.NewRequest("POST", "/test",
		strings.NewReader(`{"items":[{"sku":"A1"},{"name":"B2"}]}`))
	tape := Tape{Request: RecordedReq{
		Body: []byte(`{"items":[{"sku":"A1"},{"sku":"B2"}]}`),
	}}

	// Second element in request doesn't have "sku" => extractAtPath returns false (all-or-nothing)
	got := criterion(req, tape)
	if got != 0 {
		t.Errorf("MatchBodyFuzzy() wildcard missing field = %d, want 0", got)
	}
}

// --- extractAtPath tests ---

func TestExtractAtPath_TopLevel(t *testing.T) {
	data := map[string]any{"name": "alice"}
	val, ok := extractAtPath(data, []segment{{key: "name"}})
	if !ok {
		t.Fatal("expected ok=true")
	}
	if val != "alice" {
		t.Errorf("got %v, want alice", val)
	}
}

func TestExtractAtPath_Nested(t *testing.T) {
	data := map[string]any{"user": map[string]any{"id": float64(42)}}
	val, ok := extractAtPath(data, []segment{{key: "user"}, {key: "id"}})
	if !ok {
		t.Fatal("expected ok=true")
	}
	if val != float64(42) {
		t.Errorf("got %v, want 42", val)
	}
}

func TestExtractAtPath_Missing(t *testing.T) {
	data := map[string]any{"name": "alice"}
	_, ok := extractAtPath(data, []segment{{key: "missing"}})
	if ok {
		t.Error("expected ok=false for missing key")
	}
}

func TestExtractAtPath_NotObject(t *testing.T) {
	data := "just a string"
	_, ok := extractAtPath(data, []segment{{key: "field"}})
	if ok {
		t.Error("expected ok=false for non-object data")
	}
}

func TestExtractAtPath_Wildcard(t *testing.T) {
	data := map[string]any{
		"items": []any{
			map[string]any{"sku": "A1"},
			map[string]any{"sku": "B2"},
		},
	}
	val, ok := extractAtPath(data, []segment{
		{key: "items", wildcard: true},
		{key: "sku"},
	})
	if !ok {
		t.Fatal("expected ok=true")
	}
	expected := []any{"A1", "B2"}
	if !reflect.DeepEqual(val, expected) {
		t.Errorf("got %v, want %v", val, expected)
	}
}

func TestExtractAtPath_WildcardAtLeaf(t *testing.T) {
	data := map[string]any{
		"tags": []any{"go", "test"},
	}
	val, ok := extractAtPath(data, []segment{
		{key: "tags", wildcard: true},
	})
	if !ok {
		t.Fatal("expected ok=true")
	}
	expected := []any{"go", "test"}
	if !reflect.DeepEqual(val, expected) {
		t.Errorf("got %v, want %v", val, expected)
	}
}

func TestExtractAtPath_EmptySegments(t *testing.T) {
	data := map[string]any{"name": "alice"}
	val, ok := extractAtPath(data, nil)
	if !ok {
		t.Fatal("expected ok=true")
	}
	if !reflect.DeepEqual(val, data) {
		t.Errorf("got %v, want %v", val, data)
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
