package main

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

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
	h := newHandler(handlerDeps{authn: denyAll{}, cfg: testConfig(t), enabled: allEnabled})

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
	h := newHandler(handlerDeps{authn: auth.Locked(errors.New("CF_ACCESS_AUD is empty")), cfg: testConfig(t), enabled: allEnabled})

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

func TestModelsEndpointRequiresAuthAndListsModels(t *testing.T) {
	if w := do(newHandler(handlerDeps{authn: denyAll{}, cfg: testConfig(t), enabled: allEnabled}), "GET", "/v1/models"); w.Code != http.StatusUnauthorized {
		t.Errorf("unauthenticated GET /v1/models = %d, want 401", w.Code)
	}

	w := do(newHandler(handlerDeps{authn: auth.Disabled(), cfg: testConfig(t), enabled: allEnabled}), "GET", "/v1/models")
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"id":"groq/llama"`) {
		t.Errorf("GET /v1/models = %d %s", w.Code, w.Body)
	}
}

func TestProviderEnabledFollowsConfig(t *testing.T) {
	registered := map[string]bool{}
	enabled := providerEnabled(testConfig(t), func(name string) bool { return registered[name] })
	// Remote providers need a key; local ones don't. A key for a provider
	// that isn't in the config enables nothing.
	registered["nope"] = true
	for name, want := range map[string]bool{"ollama": true, "groq": false, "nope": false} {
		if got := enabled(name); got != want {
			t.Errorf("providerEnabled(%q) = %v, want %v", name, got, want)
		}
	}
	// Evaluated per call, not captured at startup.
	registered["groq"] = true
	if !enabled("groq") {
		t.Error("providerEnabled(groq) didn't follow a newly registered key")
	}
}

// TestModelsFollowKeyChanges registers a key through the admin API and
// checks GET /v1/models shows the provider on the very next request, then
// hides it again once the key is removed.
func TestModelsFollowKeyChanges(t *testing.T) {
	cfg := testConfig(t)
	keys := testKeys(t)
	enabled := providerEnabled(cfg, keys.Has)
	pub := newHandler(handlerDeps{authn: auth.Disabled(), cfg: cfg, enabled: enabled})
	adm := newAdminHandler(cfg, keys, enabled)

	hasGroq := func() bool {
		w := do(pub, "GET", "/v1/models")
		if w.Code != http.StatusOK {
			t.Fatalf("GET /v1/models = %d", w.Code)
		}
		return strings.Contains(w.Body.String(), `"id":"groq/llama"`)
	}
	if hasGroq() {
		t.Fatal("groq listed before a key was registered")
	}

	adminDo := func(method, body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, "/admin/providers/groq/key", strings.NewReader(body))
		r.Host = "127.0.0.1:8081"
		w := httptest.NewRecorder()
		adm.ServeHTTP(w, r)
		return w
	}
	if w := adminDo("PUT", `{"key":"`+cliTestKey+`"}`); w.Code != http.StatusOK {
		t.Fatalf("PUT key = %d %s", w.Code, w.Body)
	}
	if !hasGroq() {
		t.Fatal("groq not listed right after its key was registered")
	}
	if got, ok := keys.Get("groq"); !ok || got != cliTestKey {
		t.Fatalf("keys.Get(groq) = %q, %v", got, ok)
	}
	if w := adminDo("DELETE", ""); w.Code != http.StatusOK {
		t.Fatalf("DELETE key = %d %s", w.Code, w.Body)
	}
	if hasGroq() {
		t.Fatal("groq still listed after its key was removed")
	}
}

// setServerEnv points run() at temp paths with a valid master key and a
// loopback admin port picked by the OS.
func setServerEnv(t *testing.T) {
	t.Helper()
	t.Setenv("GATEWAY_SECRET_KEY", base64.StdEncoding.EncodeToString(make([]byte, 32)))
	t.Setenv("ADMIN_ADDR", "127.0.0.1:0")
	t.Setenv("DATABASE_PATH", filepath.Join(t.TempDir(), "gateway.db"))
	good := filepath.Join(t.TempDir(), "providers.yaml")
	if err := os.WriteFile(good, []byte(testProviders), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PROVIDERS_CONFIG", good)
}

func TestRunRefusesBadMasterKey(t *testing.T) {
	for name, value := range map[string]string{
		"missing":      "",
		"not base64":   "not-base64!!",
		"wrong length": base64.StdEncoding.EncodeToString(make([]byte, 16)),
	} {
		t.Run(name, func(t *testing.T) {
			setServerEnv(t)
			t.Setenv("GATEWAY_SECRET_KEY", value)
			err := run("127.0.0.1:0")
			if err == nil || !strings.Contains(err.Error(), "GATEWAY_SECRET_KEY") {
				t.Errorf("run() = %v, want a GATEWAY_SECRET_KEY error", err)
			}
		})
	}
}

func TestRunRefusesNonLoopbackAdminAddr(t *testing.T) {
	setServerEnv(t)
	t.Setenv("ADMIN_ADDR", ":0")
	err := run("127.0.0.1:0")
	if err == nil || !strings.Contains(err.Error(), "loopback") {
		t.Errorf("run() = %v, want a loopback error", err)
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
			setServerEnv(t)
			t.Setenv("PROVIDERS_CONFIG", path)
			err := run("127.0.0.1:0")
			if err == nil || !strings.Contains(err.Error(), "providers") {
				t.Errorf("run() = %v, want a providers config error", err)
			}
		})
	}
}

func TestAuthenticatedRequests(t *testing.T) {
	h := newHandler(handlerDeps{authn: auth.Disabled(), cfg: testConfig(t), enabled: allEnabled})

	w := do(h, "GET", "/api/me")
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"email"`) {
		t.Errorf("GET /api/me = %d %s", w.Code, w.Body)
	}
	if w := do(h, "GET", "/api/does-not-exist"); w.Code != http.StatusNotFound {
		t.Errorf("unknown path = %d, want 404 once authenticated", w.Code)
	} else {
		wantEnvelope(t, w, "invalid_request", "not_found")
	}
	if w := do(h, "POST", "/v1/models"); w.Code != http.StatusMethodNotAllowed {
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

// The chat endpoint is registered behind auth: once signed in, an unknown
// model reaches the handler and gets its 404 rather than the mux's.
func TestChatCompletionsIsRegistered(t *testing.T) {
	h := newHandler(handlerDeps{authn: auth.Disabled(), cfg: testConfig(t), enabled: allEnabled})
	r := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(`{"model":"nope","messages":[]}`))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404: %s", w.Code, w.Body)
	}
	wantEnvelope(t, w, "invalid_request", "model_not_found")
}

type forbidAll struct{}

func (forbidAll) Authenticate(*http.Request) (auth.Identity, error) {
	return auth.Identity{}, auth.ErrForbidden
}

func TestForbiddenCallerGetsEnvelope(t *testing.T) {
	w := do(newHandler(handlerDeps{authn: forbidAll{}, cfg: testConfig(t), enabled: allEnabled}), "GET", "/v1/models")
	if w.Code != http.StatusForbidden {
		t.Errorf("status = %d, want 403", w.Code)
	}
	wantEnvelope(t, w, "unauthorized", "forbidden")
}
