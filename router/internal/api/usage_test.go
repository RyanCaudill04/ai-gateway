package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"
	"time"

	"github.com/RyanCaudill04/ai-gateway/router/internal/apikeys"
	"github.com/RyanCaudill04/ai-gateway/router/internal/poller"
	"github.com/RyanCaudill04/ai-gateway/router/internal/ratelimit"
	"github.com/RyanCaudill04/ai-gateway/router/internal/upstream"
)

var usageNow = time.Date(2026, 11, 14, 12, 0, 0, 0, time.UTC)

// A provider 429 that asked for a 30 second wait.
var upstreamRateLimit = upstream.Error{Kind: upstream.KindRateLimited, Provider: "groq", Model: "big", Status: 429, RetryAfter: 30 * time.Second}

// fakeHealth is a poller's health checks, set by the test.
type fakeHealth map[string]poller.Health

func (f fakeHealth) Health(p string) (poller.Health, bool) { h, ok := f[p]; return h, ok }

// fakeKeys is a key store with one entry.
type fakeKeys map[string]apikeys.Usage

func (f fakeKeys) Usage(name string) (apikeys.Usage, bool) { u, ok := f[name]; return u, ok }

// usageEnv is a usage endpoint over a real in-memory tracker, so the tests
// exercise the same exhausted/reset rules the router itself uses.
type usageEnv struct {
	t       *testing.T
	tracker *ratelimit.Tracker
	deps    UsageDeps
}

func newUsageEnv(t *testing.T, enabled ...string) *usageEnv {
	t.Helper()
	cfg := testConfig(t)
	tr, err := ratelimit.Open(context.Background(), nil, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if len(enabled) == 0 {
		enabled = []string{"groq", "gemini", "ollama"}
	}
	return &usageEnv{t: t, tracker: tr, deps: UsageDeps{
		Config: cfg, Enabled: enabledSet(enabled...), Limits: tr,
		Now: func() time.Time { return usageNow },
	}}
}

// observe records one window as read five minutes ago.
func (e *usageEnv) observe(provider, model string, kind ratelimit.Kind, window ratelimit.Window, remaining, max int64, resetsIn time.Duration) {
	l := ratelimit.Limit{Kind: kind, Window: window, Remaining: remaining, Max: max}
	if resetsIn != 0 {
		l.ResetsAt = usageNow.Add(resetsIn)
	}
	e.tracker.ObserveWindow(provider, model, l, usageNow.Add(-5*time.Minute))
}

// get calls the endpoint as the service "mc-bot" and decodes the answer.
func (e *usageEnv) get() usageResponse {
	e.t.Helper()
	w := e.do("GET")
	if w.Code != http.StatusOK {
		e.t.Fatalf("status %d: %s", w.Code, w.Body)
	}
	var resp usageResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		e.t.Fatalf("not JSON: %v: %s", err, w.Body)
	}
	return resp
}

func (e *usageEnv) do(method string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, "/v1/usage", nil)
	r = r.WithContext(apikeys.ContextWithName(r.Context(), "mc-bot"))
	w := httptest.NewRecorder()
	UsageHandler(e.deps).ServeHTTP(w, r)
	return w
}

func (r usageResponse) model(t *testing.T, id string) modelUsage {
	t.Helper()
	for _, p := range r.Providers {
		for _, m := range p.Models {
			if m.ID == id {
				return m
			}
		}
	}
	t.Fatalf("no model %q in the response", id)
	return modelUsage{}
}

func (r usageResponse) provider(t *testing.T, name string) providerUsage {
	t.Helper()
	for _, p := range r.Providers {
		if p.Provider == name {
			return p
		}
	}
	t.Fatalf("no provider %q in the response", name)
	return providerUsage{}
}

func (r usageResponse) route(t *testing.T, id string) routeUsage {
	t.Helper()
	for _, ru := range r.Routes {
		if ru.ID == id {
			return ru
		}
	}
	t.Fatalf("no route %q in the response", id)
	return routeUsage{}
}

func val(p *int64) int64 {
	if p == nil {
		return -999
	}
	return *p
}

func str(p *string) string {
	if p == nil {
		return "<null>"
	}
	return *p
}

func TestUsageModelWithAllFourWindows(t *testing.T) {
	e := newUsageEnv(t)
	e.observe("groq", "big", ratelimit.Tokens, ratelimit.Day, 90000, 100000, 6*time.Hour)
	e.observe("groq", "big", ratelimit.Requests, ratelimit.Day, 800, 1000, 6*time.Hour)
	e.observe("groq", "big", ratelimit.Tokens, ratelimit.Minute, 5000, 6000, 40*time.Second)
	e.observe("groq", "big", ratelimit.Requests, ratelimit.Minute, 20, 30, 42*time.Second)

	m := e.get().model(t, "groq/big")
	if m.Model != "big" || m.ContextLength != 131072 || m.Status != "known" || m.Exhausted {
		t.Fatalf("model = %+v", m)
	}
	if got := str(m.ObservedAt); got != "2026-11-14T11:55:00Z" {
		t.Errorf("observed_at = %s", got)
	}

	// Always requests before tokens, minute before day.
	type want struct {
		kind, window        string
		remaining, limit    int64
		resetsAt, checkedAt string
	}
	checked := "2026-11-14T11:55:00Z"
	wants := []want{
		{"requests", "minute", 20, 30, "2026-11-14T12:00:42Z", checked},
		{"requests", "day", 800, 1000, "2026-11-14T18:00:00Z", checked},
		{"tokens", "minute", 5000, 6000, "2026-11-14T12:00:40Z", checked},
		{"tokens", "day", 90000, 100000, "2026-11-14T18:00:00Z", checked},
	}
	if len(m.Windows) != len(wants) {
		t.Fatalf("%d windows, want %d: %+v", len(m.Windows), len(wants), m.Windows)
	}
	for i, w := range wants {
		got := m.Windows[i]
		if got.Kind != w.kind || got.Window != w.window || val(got.Remaining) != w.remaining ||
			val(got.Limit) != w.limit || str(got.ResetsAt) != w.resetsAt || str(got.CheckedAt) != w.checkedAt ||
			got.Exhausted || got.Reset {
			t.Errorf("window %d = %+v, want %+v", i, got, w)
		}
	}
}

func TestUsageUnknownCountsAreNullNotMinusOne(t *testing.T) {
	e := newUsageEnv(t)
	// OpenRouter-style: the provider gave remaining but not the size, and
	// for another window the size but not what is left.
	e.observe("groq", "big", ratelimit.Requests, ratelimit.Day, 50, ratelimit.Unknown, time.Hour)
	e.observe("groq", "big", ratelimit.Tokens, ratelimit.Day, ratelimit.Unknown, 1000, time.Hour)

	w := e.do("GET")
	var raw struct {
		Providers []struct {
			Models []struct {
				ID      string
				Windows []map[string]any
			}
		}
	}
	if err := json.Unmarshal(w.Body.Bytes(), &raw); err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, p := range raw.Providers {
		for _, m := range p.Models {
			if m.ID != "groq/big" {
				continue
			}
			found = true
			if len(m.Windows) != 2 {
				t.Fatalf("windows = %v", m.Windows)
			}
			req, tok := m.Windows[0], m.Windows[1]
			if req["remaining"] != float64(50) || req["limit"] != nil {
				t.Errorf("requests window = %v, want remaining 50 and limit null", req)
			}
			if tok["remaining"] != nil || tok["limit"] != float64(1000) {
				t.Errorf("tokens window = %v, want remaining null and limit 1000", tok)
			}
			// The key is present with a null, not left out.
			for _, k := range []string{"remaining", "limit", "resets_at", "checked_at"} {
				if _, ok := req[k]; !ok {
					t.Errorf("window has no %q key", k)
				}
			}
		}
	}
	if !found {
		t.Fatal("groq/big missing")
	}
}

func TestUsageWindowThatHasReset(t *testing.T) {
	e := newUsageEnv(t)
	// Read 5 minutes ago with 3 of 30 left; its reset was 2 minutes ago.
	e.observe("groq", "big", ratelimit.Requests, ratelimit.Minute, 3, 30, -2*time.Minute)
	// The same but the size was never reported.
	e.observe("groq", "big", ratelimit.Tokens, ratelimit.Minute, 10, ratelimit.Unknown, -2*time.Minute)
	// A day window still running keeps its numbers.
	e.observe("groq", "big", ratelimit.Requests, ratelimit.Day, 500, 1000, time.Hour)

	m := e.get().model(t, "groq/big")
	if m.Exhausted {
		t.Error("a model whose only low windows have reset is not exhausted")
	}
	byKey := map[string]windowUsage{}
	for _, w := range m.Windows {
		byKey[w.Kind+"/"+w.Window] = w
	}
	r := byKey["requests/minute"]
	if !r.Reset || val(r.Remaining) != 30 || val(r.Limit) != 30 || r.ResetsAt != nil || r.Exhausted {
		t.Errorf("reset window = %+v, want full, reset true, resets_at null", r)
	}
	if str(r.CheckedAt) != "2026-11-14T11:55:00Z" {
		t.Errorf("checked_at = %s, want the time of the real reading", str(r.CheckedAt))
	}
	tk := byKey["tokens/minute"]
	if !tk.Reset || tk.Remaining != nil || tk.Limit != nil {
		t.Errorf("reset window with unknown size = %+v, want remaining null", tk)
	}
	if d := byKey["requests/day"]; d.Reset || val(d.Remaining) != 500 {
		t.Errorf("running window = %+v", d)
	}
}

func TestUsageExhaustedModel(t *testing.T) {
	cases := []struct {
		name  string
		setup func(e *usageEnv)
		kind  string
		win   string
		reset string
	}{
		{"requests per minute", func(e *usageEnv) {
			e.observe("groq", "big", ratelimit.Requests, ratelimit.Minute, 0, 30, 30*time.Second)
		}, "requests", "minute", "2026-11-14T12:00:30Z"},
		{"tokens below the minimum", func(e *usageEnv) {
			e.observe("groq", "big", ratelimit.Tokens, ratelimit.Day, 10, 100000, 3*time.Hour)
		}, "tokens", "day", "2026-11-14T15:00:00Z"},
		{"the longest window wins", func(e *usageEnv) {
			e.observe("groq", "big", ratelimit.Requests, ratelimit.Minute, 0, 30, 30*time.Second)
			e.observe("groq", "big", ratelimit.Requests, ratelimit.Day, 0, 1000, 5*time.Hour)
		}, "requests", "day", "2026-11-14T17:00:00Z"},
		{"a 429 with Retry-After", func(e *usageEnv) {
			e.tracker.ObserveError(&upstreamRateLimit, usageNow)
		}, "requests", "minute", "2026-11-14T12:00:30Z"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newUsageEnv(t)
			tc.setup(e)
			resp := e.get()
			m := resp.model(t, "groq/big")
			if !m.Exhausted || m.LimitType != tc.kind || m.Window != tc.win || str(m.ResetsAt) != tc.reset {
				t.Errorf("model = exhausted %v %s/%s resets %s, want %s/%s resets %s",
					m.Exhausted, m.LimitType, m.Window, str(m.ResetsAt), tc.kind, tc.win, tc.reset)
			}
			if other := resp.model(t, "groq/small"); other.Exhausted || other.ResetsAt != nil {
				t.Errorf("a different model is flagged: %+v", other)
			}
		})
	}
}

func TestUsageNeverObservedModelIsUnknown(t *testing.T) {
	e := newUsageEnv(t)
	e.observe("groq", "big", ratelimit.Requests, ratelimit.Day, 5, 10, time.Hour)

	w := e.do("GET")
	var raw struct {
		Providers []struct {
			Provider string
			Status   string
			Models   []struct {
				ID         string
				Status     string
				Windows    json.RawMessage
				ObservedAt *string `json:"observed_at"`
			}
		}
	}
	if err := json.Unmarshal(w.Body.Bytes(), &raw); err != nil {
		t.Fatal(err)
	}
	status := map[string]string{}
	for _, p := range raw.Providers {
		status[p.Provider] = p.Status
		for _, m := range p.Models {
			status[m.ID] = m.Status
			if m.ID == "gemini/flash" && (string(m.Windows) != "[]" || m.ObservedAt != nil) {
				t.Errorf("gemini/flash windows = %s observed_at = %v, want [] and null", m.Windows, m.ObservedAt)
			}
		}
	}
	for k, want := range map[string]string{
		"gemini": "unknown", "gemini/flash": "unknown",
		// One known model makes its provider known; the other stays unknown.
		"groq": "known", "groq/big": "known", "groq/small": "unknown",
	} {
		if status[k] != want {
			t.Errorf("status of %s = %q, want %q", k, status[k], want)
		}
	}
}

func TestUsageLocalProvider(t *testing.T) {
	checked := usageNow.Add(-10 * time.Minute)
	cases := []struct {
		name   string
		health HealthSource
		online *bool
		at     string
	}{
		{"online", fakeHealth{"ollama": {Online: true, CheckedAt: checked}}, ptr(true), "2026-11-14T11:50:00Z"},
		{"offline", fakeHealth{"ollama": {Online: false, CheckedAt: checked}}, ptr(false), "2026-11-14T11:50:00Z"},
		{"not checked yet", fakeHealth{}, nil, "<null>"},
		{"no health source", nil, nil, "<null>"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newUsageEnv(t)
			e.deps.Health = tc.health
			resp := e.get()
			p := resp.provider(t, "ollama")
			if !p.Local || !p.Enabled || p.Status != "unlimited" {
				t.Errorf("ollama = %+v", p)
			}
			if p.Health == nil {
				t.Fatal("a local provider always has a health object")
			}
			if (p.Health.Online == nil) != (tc.online == nil) || (tc.online != nil && *p.Health.Online != *tc.online) {
				t.Errorf("online = %v, want %v", p.Health.Online, tc.online)
			}
			if got := str(p.Health.CheckedAt); got != tc.at {
				t.Errorf("checked_at = %s, want %s", got, tc.at)
			}
			if m := resp.model(t, "ollama/llama"); m.Status != "unlimited" || m.Exhausted {
				t.Errorf("model = %+v", m)
			}
			if g := resp.provider(t, "groq"); g.Health != nil {
				t.Errorf("a remote provider has no health: %+v", g.Health)
			}
		})
	}
}

func ptr[T any](v T) *T { return &v }

func TestUsageDisabledProvider(t *testing.T) {
	e := newUsageEnv(t, "ollama") // groq and gemini have no key
	// Numbers left over from before the key was removed are not reported.
	e.observe("groq", "big", ratelimit.Requests, ratelimit.Day, 0, 10, time.Hour)

	resp := e.get()
	p := resp.provider(t, "groq")
	if p.Enabled || p.Status != "disabled" || len(p.Models) != 2 {
		t.Fatalf("groq = %+v", p)
	}
	for _, m := range p.Models {
		if m.Status != "disabled" || len(m.Windows) != 0 || m.Exhausted {
			t.Errorf("model %s = %+v", m.ID, m)
		}
	}
}

func TestUsageRoutes(t *testing.T) {
	cases := []struct {
		name    string
		enabled []string
		setup   func(e *usageEnv)
		route   string
		status  string
		next    string
		resets  string
	}{
		{"first entry", nil, nil, "fast", "available", "groq/small", "<null>"},
		{"second route's first entry", nil, nil, "long-context", "available", "gemini/flash", "<null>"},
		{"falls to the next entry", nil, func(e *usageEnv) {
			e.observe("groq", "small", ratelimit.Requests, ratelimit.Day, 0, 100, time.Hour)
		}, "fast", "available", "ollama/llama", "<null>"},
		{"skips a disabled provider", []string{"groq", "ollama"}, nil, "long-context", "available", "groq/big", "<null>"},
		{"all exhausted: earliest reset", nil, func(e *usageEnv) {
			e.observe("gemini", "flash", ratelimit.Requests, ratelimit.Day, 0, 100, 10*time.Minute)
			e.observe("groq", "big", ratelimit.Requests, ratelimit.Minute, 0, 30, 5*time.Minute)
		}, "long-context", "exhausted", "", "2026-11-14T12:05:00Z"},
		{"exhausted and disabled entries", []string{"groq"}, func(e *usageEnv) {
			e.observe("groq", "big", ratelimit.Requests, ratelimit.Day, 0, 100, 2*time.Hour)
		}, "long-context", "exhausted", "", "2026-11-14T14:00:00Z"},
		{"nothing enabled", []string{"ollama"}, nil, "long-context", "unavailable", "", "<null>"},
		{"private, local and exhausted", nil, func(e *usageEnv) {
			e.observe("ollama", "llama", ratelimit.Requests, ratelimit.Minute, 0, 10, 20*time.Second)
		}, "private", "exhausted", "", "2026-11-14T12:00:20Z"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newUsageEnv(t, tc.enabled...)
			if tc.setup != nil {
				tc.setup(e)
			}
			r := e.get().route(t, tc.route)
			if r.Status != tc.status || str(r.ResetsAt) != tc.resets {
				t.Errorf("route = %s resets %s, want %s resets %s", r.Status, str(r.ResetsAt), tc.status, tc.resets)
			}
			if tc.next == "" {
				if r.Next != nil {
					t.Errorf("next = %+v, want null", r.Next)
				}
				return
			}
			if r.Next == nil || r.Next.ID != tc.next {
				t.Fatalf("next = %+v, want %s", r.Next, tc.next)
			}
		})
	}

	// The provider and model fields split the same id /v1/models uses.
	r := newUsageEnv(t).get().route(t, "fast")
	if r.Next.Provider != "groq" || r.Next.Model != "small" {
		t.Errorf("next = %+v", r.Next)
	}
}

// What the route reports must be what the chat handler would pick.
func TestUsageRouteAgreesWithNextInRoute(t *testing.T) {
	e := newUsageEnv(t)
	e.observe("groq", "small", ratelimit.Requests, ratelimit.Minute, 0, 30, time.Minute)
	resp := e.get()
	for _, name := range []string{"fast", "long-context", "private"} {
		p, m, ok := NextInRoute(e.deps.Config, e.deps.Enabled, e.tracker, name, usageNow)
		r := resp.route(t, name)
		if ok != (r.Next != nil) || (ok && r.Next.ID != p+"/"+m) {
			t.Errorf("%s: endpoint says %+v, NextInRoute says %s/%s %v", name, r.Next, p, m, ok)
		}
	}
}

func TestUsageCallingKey(t *testing.T) {
	reset := time.Date(2026, 11, 15, 0, 0, 0, 0, time.UTC)
	cases := []struct {
		name string
		keys KeyUsageSource
		want *keyUsage
	}{
		{"with a cap", fakeKeys{"mc-bot": {DailyCap: 100, Used: 37, ResetsAt: reset}},
			&keyUsage{Name: "mc-bot", DailyCap: ptr(int64(100)), UsedToday: 37, Remaining: ptr(int64(63)), ResetsAt: "2026-11-15T00:00:00Z"}},
		{"at the cap", fakeKeys{"mc-bot": {DailyCap: 100, Used: 100, ResetsAt: reset}},
			&keyUsage{Name: "mc-bot", DailyCap: ptr(int64(100)), UsedToday: 100, Remaining: ptr(int64(0)), ResetsAt: "2026-11-15T00:00:00Z"}},
		{"without a cap", fakeKeys{"mc-bot": {Used: 12, ResetsAt: reset}},
			&keyUsage{Name: "mc-bot", UsedToday: 12, ResetsAt: "2026-11-15T00:00:00Z"}},
		{"unknown to the store", fakeKeys{}, nil},
		{"no store", nil, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newUsageEnv(t)
			e.deps.Keys = tc.keys
			got := e.get().Key
			if tc.want == nil {
				if got != nil {
					t.Errorf("key = %+v, want null", got)
				}
				return
			}
			if got == nil || got.Name != tc.want.Name || val(got.DailyCap) != val(tc.want.DailyCap) ||
				got.UsedToday != tc.want.UsedToday || val(got.Remaining) != val(tc.want.Remaining) || got.ResetsAt != tc.want.ResetsAt {
				t.Errorf("key = %+v, want %+v", got, tc.want)
			}
		})
	}

	// No key in the context at all (the handler used outside Require).
	e := newUsageEnv(t)
	e.deps.Keys = fakeKeys{"mc-bot": {DailyCap: 1}}
	w := httptest.NewRecorder()
	UsageHandler(e.deps).ServeHTTP(w, httptest.NewRequest("GET", "/v1/usage", nil))
	var resp usageResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil || resp.Key != nil {
		t.Errorf("without a name in the context: key = %+v, err %v", resp.Key, err)
	}
}

func TestUsageOrderIsStable(t *testing.T) {
	e := newUsageEnv(t)
	first := e.do("GET").Body.String()
	for i := 0; i < 20; i++ { // map iteration is random: repeat to catch a leak
		if got := e.do("GET").Body.String(); got != first {
			t.Fatalf("response changed between calls:\n%s\n%s", first, got)
		}
	}
	resp := e.get()
	var providers, routes, groq []string
	for _, p := range resp.Providers {
		providers = append(providers, p.Provider)
		if p.Provider == "groq" {
			for _, m := range p.Models {
				groq = append(groq, m.Model)
			}
		}
	}
	for _, r := range resp.Routes {
		routes = append(routes, r.ID)
	}
	if !slices.Equal(providers, []string{"gemini", "groq", "ollama"}) {
		t.Errorf("providers = %v, want sorted by name", providers)
	}
	if !slices.Equal(routes, []string{"fast", "long-context", "private"}) {
		t.Errorf("routes = %v, want sorted by name", routes)
	}
	if !slices.Equal(groq, []string{"big", "small"}) {
		t.Errorf("groq models = %v, want config order", groq)
	}
}

func TestUsageResponseShape(t *testing.T) {
	e := newUsageEnv(t)
	e.deps.Keys = fakeKeys{"mc-bot": {DailyCap: 10, Used: 1, ResetsAt: usageNow.Add(12 * time.Hour)}}
	w := e.do("GET")
	if ct := w.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type = %q", ct)
	}
	if cc := w.Header().Get("Cache-Control"); cc != "no-store" {
		t.Errorf("Cache-Control = %q, want no-store", cc)
	}
	var top map[string]json.RawMessage
	if err := json.Unmarshal(w.Body.Bytes(), &top); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"object", "now", "key", "routes", "providers"} {
		if _, ok := top[k]; !ok {
			t.Errorf("response has no %q", k)
		}
	}
	var resp usageResponse
	json.Unmarshal(w.Body.Bytes(), &resp)
	if resp.Object != "usage" || resp.Now != "2026-11-14T12:00:00Z" {
		t.Errorf("object %q now %q", resp.Object, resp.Now)
	}
}

func TestUsageWithoutAnyTracker(t *testing.T) {
	e := newUsageEnv(t)
	e.deps.Limits = nil
	resp := e.get()
	if m := resp.model(t, "groq/big"); m.Status != "unknown" || len(m.Windows) != 0 {
		t.Errorf("model = %+v", m)
	}
	if r := resp.route(t, "fast"); r.Status != "available" || r.Next.ID != "groq/small" {
		t.Errorf("route = %+v", r)
	}
}

func TestUsageOnlyGET(t *testing.T) {
	e := newUsageEnv(t)
	for _, method := range []string{"POST", "PUT", "DELETE", "PATCH"} {
		w := e.do(method)
		if w.Code != http.StatusMethodNotAllowed || w.Header().Get("Allow") != "GET" {
			t.Errorf("%s = %d, Allow %q", method, w.Code, w.Header().Get("Allow"))
		}
		var body struct{ Error struct{ Type, Code string } }
		if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil || body.Error.Code != "method_not_allowed" {
			t.Errorf("%s body = %s", method, w.Body)
		}
	}
}
