package main

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/RyanCaudill04/ai-gateway/router/internal/auth"
)

// denyAll is an Authenticator with no valid callers.
type denyAll struct{}

func (denyAll) Authenticate(*http.Request) (auth.Identity, error) {
	return auth.Identity{}, auth.ErrUnauthenticated
}

func do(h http.Handler, method, path string, headers ...string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, path, nil)
	for i := 0; i+1 < len(headers); i += 2 {
		r.Header.Set(headers[i], headers[i+1])
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func TestOnlyHealthChecksArePublic(t *testing.T) {
	h := newHandler(denyAll{})

	for _, path := range []string{"/healthz", "/api/health"} {
		if w := do(h, "GET", path); w.Code != http.StatusOK {
			t.Errorf("GET %s = %d, want 200 without credentials", path, w.Code)
		}
	}

	// Includes paths with no handler: an anonymous caller gets 401, not 404,
	// so the route table can't be mapped from outside.
	for _, tc := range [][2]string{
		{"GET", "/api/me"},
		{"GET", "/api/does-not-exist"},
		{"POST", "/api/health"},
		{"GET", "/api/health/"},
		{"POST", "/v1/chat/completions"},
		{"GET", "/v1/models"},
		{"GET", "/"},
	} {
		if w := do(h, tc[0], tc[1]); w.Code != http.StatusUnauthorized {
			t.Errorf("%s %s = %d, want 401", tc[0], tc[1], w.Code)
		}
	}
}

func TestLockedWhenAuthIsNotConfigured(t *testing.T) {
	h := newHandler(auth.Locked(errors.New("CF_ACCESS_AUD is empty")))

	if w := do(h, "GET", "/api/health"); w.Code != http.StatusOK {
		t.Errorf("GET /api/health = %d, want 200", w.Code)
	}
	w := do(h, "GET", "/api/me")
	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("GET /api/me = %d, want 503", w.Code)
	}
	if strings.Contains(w.Body.String(), "CF_ACCESS_AUD") {
		t.Errorf("response leaks the configuration problem: %s", w.Body)
	}
}

func TestAuthenticatedRequests(t *testing.T) {
	h := newHandler(auth.Disabled())

	w := do(h, "GET", "/api/me")
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"email"`) {
		t.Errorf("GET /api/me = %d %s", w.Code, w.Body)
	}
	if w := do(h, "GET", "/api/does-not-exist"); w.Code != http.StatusNotFound {
		t.Errorf("unknown path = %d, want 404 once authenticated", w.Code)
	}
	// A state-changing request another site made the browser send.
	if w := do(h, "POST", "/api/me", "Sec-Fetch-Site", "cross-site"); w.Code != http.StatusForbidden {
		t.Errorf("cross-site POST = %d, want 403", w.Code)
	}
}
