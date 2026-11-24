package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/RyanCaudill04/ai-gateway/router/internal/apikeys"
	"github.com/RyanCaudill04/ai-gateway/router/internal/auth"
	"github.com/RyanCaudill04/ai-gateway/router/internal/config"
)

const testProviders = `
providers:
  groq:
    base_url: https://groq.example/v1
    models:
      - id: llama
        context_length: 8192
  ollama:
    base_url: http://ollama:11434/v1
    local: true
    models:
      - id: tiny
        context_length: 4096
routes:
  private: [ollama/tiny]
`

func testConfig(t *testing.T) *config.Config {
	t.Helper()
	cfg, err := config.Parse([]byte(testProviders), os.Getenv, nil)
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}

func allEnabled(string) bool { return true }

// denyAll is an Authenticator with no valid callers.
type denyAll struct{}

func (denyAll) Authenticate(*http.Request) (auth.Identity, error) {
	return auth.Identity{}, auth.ErrUnauthenticated
}

// noKeys is a key store with no valid keys; goodKeys accepts "good-key".
type noKeys struct{}

func (noKeys) Verify(context.Context, string) (string, error) { return "", apikeys.ErrInvalid }

type goodKeys struct{}

func (goodKeys) Verify(_ context.Context, key string) (string, error) {
	if key == "good-key" {
		return "svc", nil
	}
	return "", apikeys.ErrInvalid
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

// wantEnvelope checks that a response is in the shared error format with the
// given type and code.
func wantEnvelope(t *testing.T, w *httptest.ResponseRecorder, typ, code string) {
	t.Helper()
	var body struct {
		Error struct{ Message, Type, Code string } `json:"error"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Errorf("body is not JSON: %v: %s", err, w.Body)
		return
	}
	if body.Error.Type != typ || body.Error.Code != code || body.Error.Message == "" {
		t.Errorf("error = %+v, want type %q code %q and a message", body.Error, typ, code)
	}
	if ct := w.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type = %q", ct)
	}
}

func TestOnlyHealthChecksArePublic(t *testing.T) {
	h := newHandler(denyAll{}, noKeys{}, nil, testConfig(t), allEnabled)

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
		} else {
			wantEnvelope(t, w, "unauthorized", "unauthorized")
		}
	}
}

func TestLockedWhenAuthIsNotConfigured(t *testing.T) {
	h := newHandler(auth.Locked(errors.New("CF_ACCESS_AUD is empty")), noKeys{}, nil, testConfig(t), allEnabled)

	if w := do(h, "GET", "/api/health"); w.Code != http.StatusOK {
		t.Errorf("GET /api/health = %d, want 200", w.Code)
	}
	w := do(h, "GET", "/api/me")
	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("GET /api/me = %d, want 503", w.Code)
	}
	wantEnvelope(t, w, "provider_error", "auth_not_configured")
	if strings.Contains(w.Body.String(), "CF_ACCESS_AUD") {
		t.Errorf("response leaks the configuration problem: %s", w.Body)
	}
}

func TestV1NeedsAServiceKeyNotASignIn(t *testing.T) {
	// A browser sign-in (or AUTH_MODE=none) does not open /v1.
	h := newHandler(auth.Disabled(), goodKeys{}, nil, testConfig(t), allEnabled)
	for name, headers := range map[string][]string{
		"no key":     nil,
		"wrong key":  {"Authorization", "Bearer nope"},
		"not bearer": {"Authorization", "Basic good-key"},
		"empty":      {"Authorization", "Bearer "},
	} {
		w := do(h, "GET", "/v1/models", headers...)
		if w.Code != http.StatusUnauthorized {
			t.Errorf("%s: GET /v1/models = %d, want 401", name, w.Code)
		}
		wantEnvelope(t, w, "unauthorized", "unauthorized")
	}
	// Deny by default: a /v1 path that doesn't exist is still behind the check.
	if w := do(h, "GET", "/v1/nothing-here"); w.Code != http.StatusUnauthorized {
		t.Errorf("unknown /v1 path without a key = %d, want 401", w.Code)
	}

	w := do(h, "GET", "/v1/models", "Authorization", "Bearer good-key")
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"id":"groq/llama"`) {
		t.Errorf("GET /v1/models with a key = %d %s", w.Code, w.Body)
	}
	// With a key, a missing route is the normal 404 in the error format.
	if w := do(h, "GET", "/v1/nothing-here", "Authorization", "Bearer good-key"); w.Code != http.StatusNotFound {
		t.Errorf("unknown /v1 path with a key = %d, want 404", w.Code)
	}
}

func TestAPIStaysBehindSignInNotAServiceKey(t *testing.T) {
	h := newHandler(denyAll{}, goodKeys{}, nil, testConfig(t), allEnabled)
	if w := do(h, "GET", "/api/me", "Authorization", "Bearer good-key"); w.Code != http.StatusUnauthorized {
		t.Errorf("a service key on /api/me = %d, want 401", w.Code)
	}
}

func TestProviderEnabledFollowsConfig(t *testing.T) {
	enabled := providerEnabled(testConfig(t))
	// Remote providers need a key (none can be registered until #40); local ones don't.
	for name, want := range map[string]bool{"ollama": true, "groq": false, "nope": false} {
		if got := enabled(name); got != want {
			t.Errorf("providerEnabled(%q) = %v, want %v", name, got, want)
		}
	}
}

func TestRunStopsOnBadProvidersConfig(t *testing.T) {
	invalid := filepath.Join(t.TempDir(), "providers.yaml")
	if err := os.WriteFile(invalid, []byte("providers: {}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for name, path := range map[string]string{
		"missing file": filepath.Join(t.TempDir(), "nope.yaml"),
		"invalid file": invalid,
	} {
		t.Run(name, func(t *testing.T) {
			t.Setenv("PROVIDERS_CONFIG", path)
			t.Setenv("DATABASE_PATH", filepath.Join(t.TempDir(), "gateway.db"))
			err := run("127.0.0.1:0")
			if err == nil || !strings.Contains(err.Error(), "providers") {
				t.Errorf("run() = %v, want a providers config error", err)
			}
		})
	}
}

func TestAuthenticatedRequests(t *testing.T) {
	h := newHandler(auth.Disabled(), goodKeys{}, nil, testConfig(t), allEnabled)

	w := do(h, "GET", "/api/me")
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"email"`) {
		t.Errorf("GET /api/me = %d %s", w.Code, w.Body)
	}
	if w := do(h, "GET", "/api/does-not-exist"); w.Code != http.StatusNotFound {
		t.Errorf("unknown path = %d, want 404 once authenticated", w.Code)
	} else {
		wantEnvelope(t, w, "invalid_request", "not_found")
	}
	if w := do(h, "POST", "/v1/models", "Authorization", "Bearer good-key"); w.Code != http.StatusMethodNotAllowed {
		t.Errorf("wrong method = %d, want 405", w.Code)
	} else {
		wantEnvelope(t, w, "invalid_request", "method_not_allowed")
		if w.Header().Get("Allow") == "" {
			t.Error("405 without an Allow header")
		}
	}
	// A state-changing request another site made the browser send.
	if w := do(h, "POST", "/api/me", "Sec-Fetch-Site", "cross-site"); w.Code != http.StatusForbidden {
		t.Errorf("cross-site POST = %d, want 403", w.Code)
	} else {
		wantEnvelope(t, w, "unauthorized", "cross_origin_forbidden")
	}
}

type forbidAll struct{}

func (forbidAll) Authenticate(*http.Request) (auth.Identity, error) {
	return auth.Identity{}, auth.ErrForbidden
}

func TestForbiddenCallerGetsEnvelope(t *testing.T) {
	w := do(newHandler(forbidAll{}, noKeys{}, nil, testConfig(t), allEnabled), "GET", "/api/me")
	if w.Code != http.StatusForbidden {
		t.Errorf("status = %d, want 403", w.Code)
	}
	wantEnvelope(t, w, "unauthorized", "forbidden")
}
