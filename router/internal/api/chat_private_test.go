package api

import (
	"context"
	"errors"
	"io"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/RyanCaudill04/ai-gateway/router/internal/config"
)

// privateConfig builds a config by hand, so Parse's check on the private route
// is bypassed: this is what a config that somehow got past validation looks
// like. groq is a remote provider; ollama and ollama2 are local. Each provider
// points at the fake upstream under its own path prefix.
func privateConfig(private []string) func(upURL string) *config.Config {
	return func(up string) *config.Config {
		model := func(id string) []config.Model { return []config.Model{{ID: id, ContextLength: 1000}} }
		return &config.Config{
			Providers: map[string]*config.Provider{
				"groq":    {Name: "groq", BaseURL: up + "/groq/v1", Enabled: true, Models: model("big")},
				"ollama":  {Name: "ollama", BaseURL: up + "/ollama/v1", Local: true, Enabled: true, Models: model("tiny")},
				"ollama2": {Name: "ollama2", BaseURL: up + "/ollama2/v1", Local: true, Enabled: true, Models: model("small")},
			},
			Routes: map[string][]string{
				config.PrivateRoute: private,
				"fast":              {"groq/big", "ollama/tiny"},
			},
		}
	}
}

func okJSON(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	io.WriteString(w, completion)
}

func okStream(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/event-stream")
	sse(w, chunk1)
	sse(w, "[DONE]")
}

func fail500(w http.ResponseWriter, r *http.Request) {
	http.Error(w, "down", http.StatusInternalServerError)
}

// called drains the fake upstream's record of requests, as provider names.
func (f *fixture) called() []string {
	var out []string
	for {
		select {
		case c := <-f.calls:
			out = append(out, c.provider)
		default:
			return out
		}
	}
}

// TestPrivateRouteNeverCallsANonLocalProvider is the request-time proof. The
// config is built by hand with a remote provider inside `private`, in every
// position, and the fake remote upstream must receive nothing in any case.
func TestPrivateRouteNeverCallsANonLocalProvider(t *testing.T) {
	tests := []struct {
		name       string
		route      []string
		each       map[string]http.HandlerFunc
		wantStatus int
		wantCode   string // error code, for errors
		wantCalls  []string
		wantFailed string // x-router-fallback, "" = header absent
	}{
		{
			name: "remote first, local answers", route: []string{"groq/big", "ollama/tiny"},
			wantStatus: 200, wantCalls: []string{"ollama"},
		},
		{
			name: "local first, remote after it", route: []string{"ollama/tiny", "groq/big"},
			wantStatus: 200, wantCalls: []string{"ollama"},
		},
		{
			name: "local fails, remote is the fallback", route: []string{"ollama/tiny", "groq/big"},
			each:       map[string]http.HandlerFunc{"ollama": fail500},
			wantStatus: 503, wantCode: "all_providers_exhausted", wantCalls: []string{"ollama"},
			wantFailed: "ollama/tiny=provider_error",
		},
		{
			name: "remote first, local fails", route: []string{"groq/big", "ollama/tiny"},
			each:       map[string]http.HandlerFunc{"ollama": fail500},
			wantStatus: 503, wantCode: "all_providers_exhausted", wantCalls: []string{"ollama"},
			wantFailed: "ollama/tiny=provider_error",
		},
		{
			name: "only a remote entry", route: []string{"groq/big"},
			wantStatus: 503, wantCode: "no_enabled_provider", wantCalls: nil,
		},
	}
	for _, tt := range tests {
		for _, stream := range []bool{false, true} {
			name := tt.name + ", non-streaming"
			extra := ""
			if stream {
				name, extra = tt.name+", streaming", `,"stream":true`
			}
			t.Run(name, func(t *testing.T) {
				each := map[string]http.HandlerFunc{"ollama": okJSON, "ollama2": okJSON, "groq": okJSON}
				if stream {
					each["ollama"], each["groq"] = okStream, okStream
				}
				for k, v := range tt.each {
					each[k] = v
				}
				f := newFixtureWith(t, fixtureOpts{build: privateConfig(tt.route), each: each})

				resp := f.post(t, context.Background(), chatBody("private", extra))
				body := readAll(t, resp.Body)
				if resp.StatusCode != tt.wantStatus {
					t.Fatalf("status = %d, want %d: %s", resp.StatusCode, tt.wantStatus, body)
				}
				if got := f.called(); !slices.Equal(got, tt.wantCalls) {
					t.Errorf("upstream calls = %v, want %v (groq must never be called)", got, tt.wantCalls)
				}
				if tt.wantCode != "" {
					e := errorBody(t, body)
					if e["code"] != tt.wantCode {
						t.Errorf("code = %v, want %s", e["code"], tt.wantCode)
					}
					// Neither the body nor the headers may name the remote
					// provider as having been tried.
					if strings.Contains(body, "groq") {
						t.Errorf("error body names the remote provider: %s", body)
					}
				}
				if got := resp.Header.Get("X-Router-Fallback"); got != tt.wantFailed {
					t.Errorf("x-router-fallback = %q, want %q", got, tt.wantFailed)
				}
				if p := resp.Header.Get("X-Router-Provider"); p == "groq" {
					t.Errorf("x-router-provider = groq")
				}
			})
		}
	}
}

// TestPrivateRouteFallsBackAmongLocalProviders: private may use several local
// providers, and moving on from one to the next is fine.
func TestPrivateRouteFallsBackAmongLocalProviders(t *testing.T) {
	f := newFixtureWith(t, fixtureOpts{
		build: privateConfig([]string{"ollama/tiny", "ollama2/small"}),
		each:  map[string]http.HandlerFunc{"ollama": fail500, "ollama2": okJSON, "groq": okJSON},
	})
	resp := f.post(t, context.Background(), chatBody("private", ""))
	if body := readAll(t, resp.Body); resp.StatusCode != 200 {
		t.Fatalf("status = %d: %s", resp.StatusCode, body)
	}
	wantRouterHeaders(t, resp, "ollama2", "small")
	if got := resp.Header.Get("X-Router-Fallback"); got != "ollama/tiny=provider_error" {
		t.Errorf("x-router-fallback = %q", got)
	}
	if got := f.called(); !slices.Equal(got, []string{"ollama", "ollama2"}) {
		t.Errorf("upstream calls = %v", got)
	}
}

// TestPrivateRouteLocalProvidersDown is what a client sees when Ollama is
// offline, with a valid config: a clear error, and nothing else tried. The
// remote provider is in `fast` but not in `private`.
func TestPrivateRouteLocalProvidersDown(t *testing.T) {
	f := newFixtureWith(t, fixtureOpts{
		build: privateConfig([]string{"ollama/tiny", "ollama2/small"}),
		each:  map[string]http.HandlerFunc{"ollama": fail500, "ollama2": fail500, "groq": okJSON},
	})
	resp := f.post(t, context.Background(), chatBody("private", ""))
	body := readAll(t, resp.Body)
	if resp.StatusCode != 503 {
		t.Fatalf("status = %d: %s", resp.StatusCode, body)
	}
	e := errorBody(t, body)
	if e["type"] != "all_providers_exhausted" || e["code"] != "all_providers_exhausted" {
		t.Errorf("error = %v", e)
	}
	msg, _ := e["message"].(string)
	if !strings.Contains(msg, "private route") || !strings.Contains(msg, "Ollama") || !strings.Contains(msg, "Nothing was sent to any other provider") {
		t.Errorf("message does not say the local model is offline: %q", msg)
	}
	attempts, _ := e["attempts"].([]any)
	if len(attempts) != 2 {
		t.Errorf("attempts = %v, want the two local entries", e["attempts"])
	}
	if got := f.called(); !slices.Equal(got, []string{"ollama", "ollama2"}) {
		t.Errorf("upstream calls = %v, want only the local providers", got)
	}
	t.Logf("client sees: %d %s", resp.StatusCode, body)
}

// TestPrivateRouteLastGate checks the final gate by itself, with targets made
// by hand, so it holds even if resolve and the fallback loop were changed:
// a private target of a remote provider builds no request, and the route
// stops there instead of moving on to the next entry.
func TestPrivateRouteLastGate(t *testing.T) {
	cfg := privateConfig(nil)("http://unused.invalid")
	h := &chatHandler{ChatDeps{
		Config: cfg, Limits: noLimits{}, Now: time.Now,
		Key: func(string) (string, bool) { return "k", true },
	}}
	remote := target{provider: cfg.Providers["groq"], model: cfg.Providers["groq"].Models[0], localOnly: true}
	local := target{provider: cfg.Providers["ollama"], model: cfg.Providers["ollama"].Models[0], localOnly: true}

	if _, ue := h.upstreamRequest(remote, []byte(`{}`)); ue == nil || !errors.Is(ue, errLocalOnly) {
		t.Fatalf("remote private target was not refused: %v", ue)
	}
	if req, ue := h.upstreamRequest(local, []byte(`{}`)); ue != nil || req.APIKey != "" {
		t.Errorf("local private target: err %v, key %q", ue, req.APIKey)
	}
	// Not private: a remote target is fine.
	remote.localOnly = false
	if req, ue := h.upstreamRequest(remote, []byte(`{}`)); ue != nil || req.APIKey != "k" {
		t.Errorf("ordinary remote target: err %v", ue)
	}

	// Through the loop: the gate is hit on the first entry, the second (local)
	// entry must not be tried, and the refused entry is not listed as tried.
	remote.localOnly = true
	f := newFixtureWith(t, fixtureOpts{build: privateConfig(nil), each: map[string]http.HandlerFunc{"ollama": okJSON, "groq": okJSON}})
	hh := f.handler.(*chatHandler)
	local.provider = hh.Config.Providers["ollama"]
	remote.provider = hh.Config.Providers["groq"]
	out := &outcome{}
	hh.route(context.Background(), []target{remote, local}, []byte(chatBody("private", "")), false, out)
	if !out.refused || out.err == nil || out.err.Code != "private_route_violation" {
		t.Fatalf("outcome = %+v, err %v", out, out.err)
	}
	if out.calls != 0 || out.last != nil || len(out.failed) != 0 {
		t.Errorf("calls %d, last %v, failed %v: nothing should have been tried or listed", out.calls, out.last, out.failed)
	}
	if got := f.called(); len(got) != 0 {
		t.Errorf("upstream calls = %v, want none", got)
	}
}

// TestPrivateRouteResolution: resolve drops non-local entries of `private`
// (a config that skipped Parse), leaves every other route alone, and never
// marks a pinned model.
func TestPrivateRouteResolution(t *testing.T) {
	cfg := privateConfig([]string{"groq/big", "ollama/tiny", "ollama2/small"})("http://x")
	all := func(string) bool { return true }
	names := func(r resolution) []string {
		var out []string
		for _, t := range r.targets {
			out = append(out, t.provider.Name+"/"+t.model.ID)
		}
		return out
	}

	res, e := resolve(cfg, all, "private")
	if e != nil || !res.localOnly || res.pinned || !slices.Equal(names(res), []string{"ollama/tiny", "ollama2/small"}) {
		t.Errorf("private: %v, %+v, %v", names(res), res, e)
	}
	for _, tg := range res.targets {
		if !tg.localOnly {
			t.Errorf("target %s is not marked local-only", tg.provider.Name)
		}
	}

	// A disabled local provider is dropped like any disabled entry.
	res, _ = resolve(cfg, func(p string) bool { return p != "ollama" }, "private")
	if !slices.Equal(names(res), []string{"ollama2/small"}) {
		t.Errorf("with ollama disabled: %v", names(res))
	}

	// Only remote entries: nothing usable.
	cfg.Routes[config.PrivateRoute] = []string{"groq/big"}
	if _, e := resolve(cfg, all, "private"); e == nil || e.Code != "no_enabled_provider" {
		t.Errorf("only-remote private: %v", e)
	}

	// Other routes are untouched, including one with a different name that
	// holds only local providers: no special treatment.
	cfg.Routes["offline"] = []string{"ollama/tiny"}
	for model, want := range map[string][]string{"fast": {"groq/big", "ollama/tiny"}, "offline": {"ollama/tiny"}} {
		res, _ := resolve(cfg, all, model)
		if res.localOnly || !slices.Equal(names(res), want) {
			t.Errorf("%s: %v localOnly=%v", model, names(res), res.localOnly)
		}
	}

	// Pinned models, local or not.
	for _, id := range []string{"ollama/tiny", "groq/big"} {
		res, e := resolve(cfg, all, id)
		if e != nil || !res.pinned || res.localOnly || res.targets[0].localOnly {
			t.Errorf("pinned %s: %+v %v", id, res, e)
		}
	}
}

// TestPinnedModelsAreUnaffected: a pinned remote model may leave the network
// (the guarantee is about `private` only), and a pinned local one works.
func TestPinnedModelsAreUnaffected(t *testing.T) {
	for model, provider := range map[string]string{"groq/big": "groq", "ollama/tiny": "ollama"} {
		f := newFixtureWith(t, fixtureOpts{
			build: privateConfig([]string{"ollama/tiny"}),
			each:  map[string]http.HandlerFunc{"ollama": okJSON, "groq": okJSON},
		})
		resp := f.post(t, context.Background(), chatBody(model, ""))
		if resp.StatusCode != 200 {
			t.Errorf("%s: status %d: %s", model, resp.StatusCode, readAll(t, resp.Body))
		}
		if got := f.called(); !slices.Equal(got, []string{provider}) {
			t.Errorf("%s: calls = %v", model, got)
		}
	}
}

// TestNoPrivateRoute: the route is optional. Without it, "private" is just an
// unknown model.
func TestNoPrivateRoute(t *testing.T) {
	build := func(up string) *config.Config {
		cfg := privateConfig(nil)(up)
		delete(cfg.Routes, config.PrivateRoute)
		return cfg
	}
	f := newFixtureWith(t, fixtureOpts{build: build, each: map[string]http.HandlerFunc{"ollama": okJSON, "groq": okJSON}})
	resp := f.post(t, context.Background(), chatBody("private", ""))
	body := readAll(t, resp.Body)
	if resp.StatusCode != 404 || errorBody(t, body)["code"] != "model_not_found" {
		t.Errorf("= %d %s, want 404 model_not_found", resp.StatusCode, body)
	}
	if got := f.called(); len(got) != 0 {
		t.Errorf("upstream calls = %v", got)
	}
}
