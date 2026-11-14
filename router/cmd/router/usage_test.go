package main

import (
	"context"
	"encoding/json"
	"net/http"
	"path/filepath"
	"testing"
	"time"

	"github.com/RyanCaudill04/ai-gateway/router/internal/api"
	"github.com/RyanCaudill04/ai-gateway/router/internal/apikeys"
	"github.com/RyanCaudill04/ai-gateway/router/internal/auth"
	"github.com/RyanCaudill04/ai-gateway/router/internal/poller"
	"github.com/RyanCaudill04/ai-gateway/router/internal/ratelimit"
	"github.com/RyanCaudill04/ai-gateway/router/internal/store"
	"github.com/RyanCaudill04/ai-gateway/router/internal/upstream"
)

// pollerHealth is a health source with fixed answers, standing in for the
// poller (which would need a running Ollama to have any).
type pollerHealth map[string]poller.Health

func (p pollerHealth) Health(name string) (poller.Health, bool) { h, ok := p[name]; return h, ok }

// cappedKey makes a key store on a fresh database with one key, "ui", that
// may make cap requests a day, and returns the store and the key.
func cappedKey(t *testing.T, cap int64) (*apikeys.Store, string) {
	t.Helper()
	db, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "gateway.db"))
	if err != nil {
		t.Fatal(err)
	}
	s, err := apikeys.Open(context.Background(), db)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		s.Close(context.Background())
		db.Close()
	})
	key, prefix, hash, err := apikeys.Generate("ui")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Create(context.Background(), apikeys.NewKey{Name: "ui", Prefix: prefix, Hash: hash, DailyCap: cap}); err != nil {
		t.Fatal(err)
	}
	return s, key
}

// The usage endpoint is joined to its sources in newHandler: the real
// tracker, the real key store and a health source all feed one response.
func TestUsageEndpointIsFedByTheRealSources(t *testing.T) {
	cfg := testConfig(t)
	limits, err := ratelimit.Open(context.Background(), nil, cfg)
	if err != nil {
		t.Fatal(err)
	}
	// A real 429 from groq, recorded the way the chat handler records it.
	limits.ObserveError(&upstream.Error{
		Kind: upstream.KindRateLimited, Provider: "groq", Model: "llama",
		Status: 429, RetryAfter: time.Hour,
	}, time.Now())

	keys, key := cappedKey(t, 50)
	checked := time.Now().Add(-time.Minute)
	h := newHandler(handlerDeps{
		authn: denyAll{}, cfg: cfg, enabled: allEnabled, apiKeys: keys, limits: limits,
		health: pollerHealth{"ollama": {Online: true, CheckedAt: checked}},
	})

	w := do(h, "GET", "/v1/usage", "Authorization", "Bearer "+key)
	if w.Code != http.StatusOK {
		t.Fatalf("status %d: %s", w.Code, w.Body)
	}
	if cc := w.Header().Get("Cache-Control"); cc != "no-store" {
		t.Errorf("Cache-Control = %q", cc)
	}
	var got struct {
		Key *struct {
			Name      string
			DailyCap  *int64 `json:"daily_cap"`
			UsedToday int64  `json:"used_today"`
		}
		Routes []struct {
			ID     string
			Status string
			Next   *struct{ ID string }
		}
		Providers []struct {
			Provider string
			Status   string
			Health   *struct{ Online *bool }
			Models   []struct {
				ID        string
				Exhausted bool
				Windows   []struct{ Kind, Window string }
			}
		}
	}
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("not JSON: %v: %s", err, w.Body)
	}

	// The key store: its name and cap, and this very read not counted.
	if got.Key == nil || got.Key.Name != "ui" || got.Key.DailyCap == nil || *got.Key.DailyCap != 50 || got.Key.UsedToday != 0 {
		t.Errorf("key = %+v", got.Key)
	}
	// The tracker: groq/llama is exhausted and carries the window the 429 made.
	var groqSeen bool
	for _, p := range got.Providers {
		switch p.Provider {
		case "groq":
			groqSeen = true
			m := p.Models[0]
			if m.ID != "groq/llama" || !m.Exhausted || len(m.Windows) != 1 || m.Windows[0].Kind != "requests" {
				t.Errorf("groq/llama = %+v", m)
			}
			if p.Status != "known" {
				t.Errorf("groq status = %q", p.Status)
			}
		case "ollama":
			// The health source.
			if p.Status != "unlimited" || p.Health == nil || p.Health.Online == nil || !*p.Health.Online {
				t.Errorf("ollama = %+v", p)
			}
		}
	}
	if !groqSeen {
		t.Error("groq missing")
	}
	// The route check runs against the same tracker: private is local, so
	// it is still available.
	if len(got.Routes) != 1 || got.Routes[0].ID != "private" || got.Routes[0].Next == nil || got.Routes[0].Next.ID != "ollama/tiny" {
		t.Errorf("routes = %+v", got.Routes)
	}
}

func TestUsageEndpointNeedsAServiceKeyAndOnlyAllowsGET(t *testing.T) {
	keys, key := testAPIKeys(t)
	h := newHandler(handlerDeps{authn: auth.Disabled(), cfg: testConfig(t), enabled: allEnabled, apiKeys: keys})

	// Even with Access disabled, and with or without a bad key: 401.
	for _, authz := range []string{"", "Bearer nonsense"} {
		var headers []string
		if authz != "" {
			headers = []string{"Authorization", authz}
		}
		w := do(h, "GET", "/v1/usage", headers...)
		if w.Code != http.StatusUnauthorized {
			t.Errorf("GET /v1/usage with %q = %d, want 401", authz, w.Code)
		}
	}
	for _, method := range []string{"POST", "PUT", "DELETE"} {
		w := do(h, method, "/v1/usage", "Authorization", "Bearer "+key)
		if w.Code != http.StatusMethodNotAllowed || w.Header().Get("Allow") == "" {
			t.Errorf("%s /v1/usage = %d, Allow %q", method, w.Code, w.Header().Get("Allow"))
		}
		wantEnvelope(t, w, "invalid_request", "method_not_allowed")
	}
	// With nothing wired (no tracker, health or poller) it still answers.
	if w := do(h, "GET", "/v1/usage", "Authorization", "Bearer "+key); w.Code != http.StatusOK {
		t.Errorf("GET /v1/usage = %d: %s", w.Code, w.Body)
	}
}

// A dashboard polling /v1/usage must not use up the cap it is showing, and a
// key that has used up its day can still read why.
func TestUsagePollingDoesNotCountTowardTheDailyCap(t *testing.T) {
	keys, key := cappedKey(t, 2)
	h := newHandler(handlerDeps{authn: denyAll{}, cfg: testConfig(t), enabled: allEnabled, apiKeys: keys})
	bearer := []string{"Authorization", "Bearer " + key}

	readKey := func() (used int64, remaining int64) {
		t.Helper()
		w := do(h, "GET", "/v1/usage", bearer...)
		if w.Code != http.StatusOK {
			t.Fatalf("usage = %d: %s", w.Code, w.Body)
		}
		var body struct {
			Key struct {
				UsedToday int64  `json:"used_today"`
				Remaining *int64 `json:"remaining"`
			}
		}
		if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil || body.Key.Remaining == nil {
			t.Fatalf("body = %s (%v)", w.Body, err)
		}
		return body.Key.UsedToday, *body.Key.Remaining
	}

	for i := 0; i < 10; i++ {
		if used, left := readKey(); used != 0 || left != 2 {
			t.Fatalf("after %d usage reads: used %d, remaining %d, want 0 and 2", i, used, left)
		}
	}

	// Anything else still counts, and the cap still bites.
	for i, want := range []int{http.StatusOK, http.StatusOK, http.StatusTooManyRequests} {
		if w := do(h, "GET", "/v1/models", bearer...); w.Code != want {
			t.Fatalf("models call %d = %d, want %d", i, w.Code, want)
		}
	}
	// Over the cap, the key can still see that it is.
	if used, left := readKey(); used != 2 || left != 0 {
		t.Errorf("over the cap: used %d, remaining %d, want 2 and 0", used, left)
	}
	// And the key check itself is not skipped.
	if w := do(h, "GET", "/v1/usage", "Authorization", "Bearer nonsense"); w.Code != http.StatusUnauthorized {
		t.Errorf("bad key = %d, want 401", w.Code)
	}
}

// The handler wiring takes the real types, not only the test fakes.
var (
	_ api.UsageTracker   = (*ratelimit.Tracker)(nil)
	_ api.HealthSource   = (*poller.Poller)(nil)
	_ api.KeyUsageSource = (*apikeys.Store)(nil)
)
