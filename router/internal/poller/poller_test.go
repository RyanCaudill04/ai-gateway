package poller

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/RyanCaudill04/ai-gateway/router/internal/config"
	"github.com/RyanCaudill04/ai-gateway/router/internal/ratelimit"
	"github.com/RyanCaudill04/ai-gateway/router/internal/upstream"
)

var t0 = time.Date(2026, 11, 13, 12, 0, 0, 0, time.UTC)

const secretKey = "sk-test-SECRET-0123456789"

// fakeClock is the poller's time, moved by hand.
type fakeClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(d)
	c.mu.Unlock()
}

// syncBuffer collects log output from several goroutines.
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

// rewriteTransport sends every request to the test server, so a provider can
// have its real-looking base_url (the poller recognises OpenRouter by host).
type rewriteTransport struct{ to *url.URL }

func (r rewriteTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	req = req.Clone(req.Context())
	req.URL.Scheme, req.URL.Host = r.to.Scheme, r.to.Host
	return http.DefaultTransport.RoundTrip(req)
}

// countingServer counts requests by "METHOD path" and answers with handler.
type countingServer struct {
	*httptest.Server
	mu    sync.Mutex
	count map[string]int
	auth  []string
	body  []byte
}

func newServer(t *testing.T, h http.HandlerFunc) *countingServer {
	t.Helper()
	cs := &countingServer{count: map[string]int{}}
	cs.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		cs.mu.Lock()
		cs.count[r.Method+" "+r.URL.Path]++
		cs.auth = append(cs.auth, r.Header.Get("Authorization"))
		if len(b) > 0 {
			cs.body = b
		}
		cs.mu.Unlock()
		h(w, r)
	}))
	t.Cleanup(cs.Close)
	return cs
}

func (c *countingServer) hits(key string) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.count[key]
}

func (c *countingServer) total() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	n := 0
	for _, v := range c.count {
		n += v
	}
	return n
}

const chatPath = "POST /chat/completions"

// dayHeaders reports a day window of `remaining` out of `limit`, resetting in `reset`.
func dayHeaders(remaining, limit int, reset string) http.Header {
	h := http.Header{}
	h.Set("x-ratelimit-remaining-requests-day", fmt.Sprint(remaining))
	h.Set("x-ratelimit-limit-requests-day", fmt.Sprint(limit))
	h.Set("x-ratelimit-reset-requests-day", reset)
	return h
}

func okChat(h http.Header) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		for k, v := range h {
			w.Header()[k] = v
		}
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"choices":[{"message":{"role":"assistant","content":"h"}}]}`)
	}
}

type env struct {
	p     *Poller
	tr    *ratelimit.Tracker
	clock *fakeClock
	logs  *syncBuffer
}

// prov builds a provider whose base_url is the test server (a generic host).
func prov(name, baseURL string, models ...string) *config.Provider {
	p := &config.Provider{Name: name, BaseURL: baseURL, Enabled: true}
	for _, m := range models {
		p.Models = append(p.Models, config.Model{ID: m, ContextLength: 1000})
	}
	return p
}

func routeAll(cfg *config.Config) {
	cfg.Routes = map[string][]string{}
	for name, p := range cfg.Providers {
		for _, m := range p.Models {
			cfg.Routes["r"] = append(cfg.Routes["r"], name+"/"+m.ID)
		}
	}
}

func newEnv(t *testing.T, cfg *config.Config, mod func(*Deps, *Options)) *env {
	t.Helper()
	tr, err := ratelimit.Open(context.Background(), nil, cfg)
	if err != nil {
		t.Fatal(err)
	}
	clock := &fakeClock{now: t0}
	logs := &syncBuffer{}
	deps := Deps{
		Config: cfg, Tracker: tr, Completer: upstream.New(upstream.Options{}),
		Key:     func(string) (string, bool) { return secretKey, true },
		Enabled: func(name string) bool { return cfg.Providers[name] != nil },
	}
	opts := Options{
		Interval: time.Hour, ProbeTimeout: 5 * time.Second, Now: clock.Now,
		Logger: slog.New(slog.NewTextHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug})),
	}
	if mod != nil {
		mod(&deps, &opts)
	}
	return &env{p: New(deps, opts), tr: tr, clock: clock, logs: logs}
}

func (e *env) tick() { e.p.tick(context.Background()) }

// A probe is a one-word, one-token completion; its headers land in the
// tracker stamped with the poller's clock, and it is counted.
func TestProbeRecordsHeadersWithCheckedAt(t *testing.T) {
	srv := newServer(t, okChat(dayHeaders(900, 1000, "10h")))
	cfg := &config.Config{Providers: map[string]*config.Provider{"fast": prov("fast", srv.URL, "m1")}}
	routeAll(cfg)
	e := newEnv(t, cfg, nil)

	e.tick()

	if n := srv.hits(chatPath); n != 1 {
		t.Fatalf("probes sent = %d, want 1", n)
	}
	var body map[string]any
	if err := json.Unmarshal(srv.body, &body); err != nil {
		t.Fatal(err)
	}
	if body["max_tokens"] != float64(1) || body["model"] != "m1" {
		t.Errorf("probe body = %s", srv.body)
	}
	if srv.auth[0] != "Bearer "+secretKey {
		t.Errorf("probe was not authenticated")
	}
	u, ok := e.tr.Model("fast", "m1")
	if !ok || len(u.Limits) != 1 {
		t.Fatalf("usage = %+v", u)
	}
	l := u.Limits[0]
	if l.Remaining != 900 || l.Max != 1000 || !l.CheckedAt.Equal(t0) || !u.LastObserved.Equal(t0) {
		t.Errorf("window = %+v, last observed %v", l, u.LastObserved)
	}
	if st := e.p.Stats(); st.Probes != 1 || st.Day != "2026-11-13" {
		t.Errorf("stats = %+v", st)
	}
	if !strings.Contains(e.logs.String(), `probes=1`) {
		t.Errorf("probe count not logged:\n%s", e.logs)
	}
}

// A 429 to a probe is information: the model is now known to be exhausted.
func TestProbe429MarksModelExhausted(t *testing.T) {
	srv := newServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "120")
		http.Error(w, `{"error":{"message":"slow down"}}`, http.StatusTooManyRequests)
	})
	cfg := &config.Config{Providers: map[string]*config.Provider{"fast": prov("fast", srv.URL, "m1")}}
	routeAll(cfg)
	e := newEnv(t, cfg, nil)

	e.tick()

	l, ok := e.tr.Exhausted("fast", "m1", t0)
	if !ok || !l.ResetsAt.Equal(t0.Add(2*time.Minute)) || !l.CheckedAt.Equal(t0) {
		t.Fatalf("after a probe 429: %+v ok=%v", l, ok)
	}
}

// The OpenRouter key endpoint records the daily free window for every :free
// model without sending a completion, and never touches a paid model.
func TestOpenRouterKeyEndpointRecordsWindowWithoutCompletion(t *testing.T) {
	srv := newServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/key" || r.Header.Get("Authorization") != "Bearer "+secretKey {
			http.Error(w, "no", http.StatusUnauthorized)
			return
		}
		io.WriteString(w, `{"data":{"is_free_tier":true,"usage":1.5,"rate_limit":{"requests":-1},
			"free_model_daily_requests":{"used":50,"limit":50,"remaining":0}}}`)
	})
	to, _ := url.Parse(srv.URL)
	// The provider is called "sidehustle": it is recognised by its host.
	cfg := &config.Config{Providers: map[string]*config.Provider{
		"sidehustle": prov("sidehustle", "https://openrouter.ai/api/v1", "a/one:free", "b/two:free", "c/paid"),
	}}
	routeAll(cfg)
	e := newEnv(t, cfg, func(d *Deps, o *Options) { o.HTTP = &http.Client{Transport: rewriteTransport{to}} })

	e.tick()

	if n := srv.hits("GET /api/v1/key"); n != 1 {
		t.Fatalf("key endpoint calls = %d, want 1 (one call serves every :free model)", n)
	}
	if srv.hits(chatPath) != 0 {
		t.Fatal("a completion was sent to OpenRouter")
	}
	for _, m := range []string{"a/one:free", "b/two:free"} {
		l, ok := e.tr.Exhausted("sidehustle", m, t0)
		if !ok {
			t.Fatalf("%s: not exhausted after 0 of 50 remaining", m)
		}
		if l.Window != ratelimit.Day || l.Kind != ratelimit.Requests || l.Max != 50 ||
			!l.ResetsAt.Equal(time.Date(2026, 11, 14, 0, 0, 0, 0, time.UTC)) || !l.CheckedAt.Equal(t0) {
			t.Errorf("%s: window = %+v", m, l)
		}
	}
	if _, ok := e.tr.Model("sidehustle", "c/paid"); ok {
		t.Error("the paid model got a free-model window")
	}
	if st := e.p.Stats(); st.Probes != 0 || st.KeyChecks != 1 {
		t.Errorf("stats = %+v, want no probes and one key check", st)
	}

	e.clock.Advance(time.Minute) // fresh and throttled: nothing more is sent
	e.tick()
	if n := srv.hits("GET /api/v1/key"); n != 1 {
		t.Errorf("key endpoint called again within the interval (%d calls)", n)
	}
	e.clock.Advance(time.Hour)
	e.tick()
	if n := srv.hits("GET /api/v1/key"); n != 2 {
		t.Errorf("key endpoint calls after the interval = %d, want 2", n)
	}
}

// Local providers get a reachability check and nothing else.
func TestLocalHealthOnlineOfflineOnline(t *testing.T) {
	var up atomic.Bool
	up.Store(true)
	srv := newServer(t, func(w http.ResponseWriter, r *http.Request) {
		if !up.Load() {
			http.Error(w, "down", http.StatusServiceUnavailable)
			return
		}
		io.WriteString(w, `{"data":[]}`)
	})
	pi := prov("pi", srv.URL+"/v1", "llama")
	pi.Local = true
	cfg := &config.Config{Providers: map[string]*config.Provider{"pi": pi}}
	routeAll(cfg)
	e := newEnv(t, cfg, func(d *Deps, o *Options) {
		d.Key = func(string) (string, bool) { return "", false } // keyless
	})

	if _, ok := e.p.Health("pi"); ok {
		t.Fatal("health known before any check")
	}
	for i, want := range []bool{true, false, true} {
		up.Store(want)
		e.clock.Advance(5 * time.Minute)
		e.tick()
		h, ok := e.p.Health("pi")
		if !ok || h.Online != want || !h.CheckedAt.Equal(e.clock.Now()) {
			t.Fatalf("check %d: %+v ok=%v, want online=%v at %v", i, h, ok, want, e.clock.Now())
		}
		if got := e.p.LocalHealth()["pi"]; got != h {
			t.Errorf("LocalHealth = %+v, want %+v", got, h)
		}
	}
	if n := srv.hits(chatPath); n != 0 {
		t.Errorf("a generation was sent to a local provider (%d)", n)
	}
	if n := srv.hits("GET /v1/models"); n != 3 {
		t.Errorf("model list requests = %d, want 3", n)
	}
	if st := e.p.Stats(); st.Probes != 0 || st.HealthChecks != 3 {
		t.Errorf("stats = %+v", st)
	}
}

func TestDisabledProviderIsNeverContacted(t *testing.T) {
	srv := newServer(t, okChat(nil))
	local := newServer(t, okChat(nil))
	pi := prov("pi", local.URL+"/v1", "llama")
	pi.Local = true
	cfg := &config.Config{Providers: map[string]*config.Provider{
		"nokey": prov("nokey", srv.URL, "m1"), "pi": pi,
	}}
	routeAll(cfg)
	e := newEnv(t, cfg, func(d *Deps, o *Options) { d.Enabled = func(string) bool { return false } })

	e.tick()
	e.clock.Advance(2 * time.Hour)
	e.tick()

	if srv.total() != 0 || local.total() != 0 {
		t.Fatalf("a disabled provider was contacted (%d, %d requests)", srv.total(), local.total())
	}
}

// Providers that send no headers are not probed: not by host (Gemini,
// Workers AI), and not once a probe has shown a provider sends none.
func TestHeaderlessProvidersAreNotProbed(t *testing.T) {
	fake := &countingCompleter{}
	cfg := &config.Config{Providers: map[string]*config.Provider{
		"g":  prov("g", "https://generativelanguage.googleapis.com/v1beta/openai", "gemini-x"),
		"cf": prov("cf", "https://api.cloudflare.com/client/v4/accounts/abc/ai/v1", "@cf/x/y"),
	}}
	routeAll(cfg)
	e := newEnv(t, cfg, func(d *Deps, o *Options) { d.Completer = fake })
	e.tick()
	e.clock.Advance(3 * time.Hour)
	e.tick()
	if fake.n.Load() != 0 {
		t.Fatalf("header-less providers were probed %d times", fake.n.Load())
	}

	// An unknown host is probed once; with no headers back it is never probed again.
	srv := newServer(t, okChat(nil))
	cfg = &config.Config{Providers: map[string]*config.Provider{"mystery": prov("mystery", srv.URL, "m1")}}
	routeAll(cfg)
	e = newEnv(t, cfg, nil)
	e.tick()
	first := srv.hits(chatPath)
	e.clock.Advance(3 * time.Hour)
	e.tick()
	if first != 1 || srv.hits(chatPath) != 1 {
		t.Errorf("probes: first tick %d, total %d; want a single probe in all", first, srv.hits(chatPath))
	}
}

type countingCompleter struct{ n atomic.Int32 }

func (c *countingCompleter) Complete(context.Context, upstream.Request) (*upstream.Response, error) {
	c.n.Add(1)
	return &upstream.Response{Status: 200, Header: http.Header{}}, nil
}

// The decision table, one row per skip rule.
func TestDecide(t *testing.T) {
	type setup func(tr *ratelimit.Tracker, now time.Time)
	observe := func(ago time.Duration, remaining, limit int, reset string) setup {
		return func(tr *ratelimit.Tracker, now time.Time) {
			tr.Observe("fast", "m1", dayHeaders(remaining, limit, reset), now.Add(-ago))
		}
	}
	tests := []struct {
		name     string
		provider string
		model    string
		setup    setup
		enabled  bool
		noKey    bool
		learned  bool
		want     action
	}{
		{name: "nothing known: probe", provider: "fast", model: "m1", enabled: true, want: actProbe},
		{name: "stale with room: probe", provider: "fast", model: "m1", enabled: true, setup: observe(2*time.Hour, 800, 1000, "20h"), want: actProbe},
		{name: "refreshed within the interval", provider: "fast", model: "m1", enabled: true, setup: observe(10*time.Minute, 800, 1000, "20h"), want: actSkip},
		{name: "exhausted", provider: "fast", model: "m1", enabled: true, setup: observe(2*time.Hour, 0, 1000, "20h"), want: actSkip},
		{name: "nearly exhausted: few left", provider: "fast", model: "m1", enabled: true, setup: observe(2*time.Hour, 5, 1000, "20h"), want: actSkip},
		{name: "nearly exhausted: a tenth left", provider: "fast", model: "m1", enabled: true, setup: observe(2*time.Hour, 100, 1000, "20h"), want: actSkip},
		{name: "just above nearly", provider: "fast", model: "m1", enabled: true, setup: observe(2*time.Hour, 101, 1000, "20h"), want: actProbe},
		{name: "reset passed: known to be full again", provider: "fast", model: "m1", enabled: true, setup: observe(3*time.Hour, 3, 1000, "1h"), want: actSkip},
		{name: "provider disabled", provider: "fast", model: "m1", enabled: false, want: actSkip},
		{name: "no key registered", provider: "fast", model: "m1", enabled: true, noKey: true, want: actSkip},
		{name: "gemini host sends no headers", provider: "gem", model: "g1", enabled: true, want: actSkip},
		{name: "learned to send no headers", provider: "fast", model: "m1", enabled: true, learned: true, want: actSkip},
		{name: "free openrouter model: key endpoint", provider: "or", model: "x:free", enabled: true, want: actKey},
		{name: "paid openrouter model: skipped", provider: "or", model: "x", enabled: true, want: actSkip},
		{name: "local: health check", provider: "pi", model: "llama", enabled: true, want: actHealth},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pi := prov("pi", "http://pi:11434/v1", "llama")
			pi.Local = true
			cfg := &config.Config{Providers: map[string]*config.Provider{
				"fast": prov("fast", "http://127.0.0.1:1", "m1"),
				"gem":  prov("gem", "https://generativelanguage.googleapis.com/v1beta/openai", "g1"),
				"or":   prov("or", "https://openrouter.ai/api/v1", "x:free", "x"),
				"pi":   pi,
			}}
			routeAll(cfg)
			e := newEnv(t, cfg, func(d *Deps, o *Options) {
				d.Enabled = func(string) bool { return tt.enabled }
				d.Key = func(string) (string, bool) { return secretKey, !tt.noKey }
			})
			if tt.setup != nil {
				tt.setup(e.tr, t0)
			}
			if tt.learned {
				e.p.noHeaders["fast"] = true
			}
			var got *target
			for _, c := range e.p.candidates(t0) {
				if c.name == tt.provider && c.model == tt.model {
					got = &c
				}
			}
			if got == nil {
				t.Fatal("not a candidate")
			}
			if d := e.p.decide(*got, t0); d.action != tt.want {
				t.Errorf("action = %v (%s), want %v", d.action, d.reason, tt.want)
			}
		})
	}
}

// A model in no route is only watched while real requests keep using it, and
// the poller's own probes don't count as use.
func TestOnlyRoutedOrRecentlyUsedModelsAreWatched(t *testing.T) {
	srv := newServer(t, okChat(dayHeaders(900, 1000, "30h")))
	cfg := &config.Config{
		Providers: map[string]*config.Provider{"fast": prov("fast", srv.URL, "routed", "extra", "idle")},
		Routes:    map[string][]string{"r": {"fast/routed"}},
	}
	e := newEnv(t, cfg, nil)
	names := func(now time.Time) string {
		var s []string
		for _, c := range e.p.candidates(now) {
			s = append(s, c.model)
		}
		return strings.Join(s, ",")
	}
	if got := names(t0); got != "routed" {
		t.Fatalf("candidates = %q, want only the routed model", got)
	}

	e.tr.Observe("fast", "extra", dayHeaders(900, 1000, "30h"), t0) // a real request
	if got := names(t0); got != "extra,routed" && got != "routed,extra" {
		t.Fatalf("candidates after real use = %q", got)
	}

	// Probing "extra" refreshes LastObserved, but must not renew its welcome.
	e.clock.Advance(2 * time.Hour)
	e.tick()
	if srv.hits(chatPath) != 2 {
		t.Fatalf("probes = %d, want 2 (routed and recently used)", srv.hits(chatPath))
	}
	e.clock.Advance(23 * time.Hour) // 25h after the real request, 23h after the probe
	if got := names(e.clock.Now()); got != "routed" {
		t.Errorf("candidates 25h after the last real use = %q, want only routed", got)
	}
}

// After a restart every model is stale; only a few are probed per tick, the
// stalest first, and none twice.
func TestProbesPerTickAreCapped(t *testing.T) {
	srv := newServer(t, okChat(dayHeaders(900, 1000, "30h")))
	cfg := &config.Config{Providers: map[string]*config.Provider{"fast": prov("fast", srv.URL, "a", "b", "c", "d", "e")}}
	routeAll(cfg)
	e := newEnv(t, cfg, func(d *Deps, o *Options) { o.MaxProbesPerTick = 2 })

	e.tick()
	if n := srv.hits(chatPath); n != 2 {
		t.Fatalf("first tick probes = %d, want 2", n)
	}
	e.clock.Advance(time.Minute)
	e.tick()
	e.clock.Advance(time.Minute)
	e.tick()
	if n := srv.hits(chatPath); n != 5 {
		t.Errorf("after three ticks probes = %d, want 5 (each model once)", n)
	}
	e.clock.Advance(time.Minute)
	e.tick()
	if n := srv.hits(chatPath); n != 5 {
		t.Errorf("fresh models were probed again (%d)", n)
	}
}

// One provider that never answers neither stalls the tick for the others nor
// outlasts the probe timeout.
func TestHangingUpstreamDoesNotStallOthers(t *testing.T) {
	hang := newServer(t, func(w http.ResponseWriter, r *http.Request) { <-r.Context().Done() })
	fast := newServer(t, okChat(dayHeaders(900, 1000, "30h")))
	cfg := &config.Config{Providers: map[string]*config.Provider{
		"hang": prov("hang", hang.URL, "h1"), "fast": prov("fast", fast.URL, "f1"),
	}}
	routeAll(cfg)
	e := newEnv(t, cfg, func(d *Deps, o *Options) { o.ProbeTimeout = 300 * time.Millisecond })

	start := time.Now()
	done := make(chan struct{})
	go func() { e.tick(); close(done) }()

	deadline := time.After(3 * time.Second)
	for {
		if u, _ := e.tr.Model("fast", "f1"); u.Known() {
			break // recorded while the other probe is still hanging
		}
		select {
		case <-deadline:
			t.Fatal("the fast model was never refreshed")
		case <-time.After(5 * time.Millisecond):
		}
	}
	select {
	case <-done:
	case <-deadline:
		t.Fatal("tick did not finish: the hanging probe was not timed out")
	}
	if time.Since(start) > 2*time.Second {
		t.Errorf("tick took %v with a 300ms probe timeout", time.Since(start))
	}
	if u, _ := e.tr.Model("hang", "h1"); u.Known() {
		t.Error("a probe that never answered recorded numbers")
	}
}

// Neither logs nor errors may contain the provider key, even when a provider
// echoes it back.
func TestKeyNeverAppearsInLogs(t *testing.T) {
	echo := func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "bad key "+r.Header.Get("Authorization")+" ("+secretKey+")", http.StatusInternalServerError)
	}
	srv := newServer(t, echo)
	or := newServer(t, echo)
	to, _ := url.Parse(or.URL)
	cfg := &config.Config{Providers: map[string]*config.Provider{
		"fast": prov("fast", srv.URL, "m1"),
		"or":   prov("or", "https://openrouter.ai/api/v1", "x:free"),
		// Unreachable on purpose: the transport error quotes the URL.
		"down": prov("down", "http://"+secretKey+"@127.0.0.1:1", "d1"),
	}}
	routeAll(cfg)
	e := newEnv(t, cfg, func(d *Deps, o *Options) { o.HTTP = &http.Client{Transport: rewriteTransport{to}} })

	e.tick()

	if srv.hits(chatPath) != 1 || or.hits("GET /api/v1/key") != 1 {
		t.Fatalf("test did not exercise the failure paths: %d, %d", srv.hits(chatPath), or.hits("GET /api/v1/key"))
	}
	if strings.Contains(e.logs.String(), secretKey) {
		t.Fatalf("the provider key is in the logs:\n%s", e.logs)
	}
	if !strings.Contains(e.logs.String(), "provider=fast") {
		t.Errorf("expected the failure to be logged by provider name:\n%s", e.logs)
	}
}

// The per-day count resets at UTC midnight, and the finished day is logged.
func TestProbeCounterResetsAtUTCMidnight(t *testing.T) {
	srv := newServer(t, okChat(dayHeaders(900, 1000, "40h")))
	cfg := &config.Config{Providers: map[string]*config.Provider{"fast": prov("fast", srv.URL, "m1")}}
	routeAll(cfg)
	e := newEnv(t, cfg, nil)
	e.clock.now = time.Date(2026, 11, 13, 23, 30, 0, 0, time.UTC)

	e.tick()
	if st := e.p.Stats(); st.Day != "2026-11-13" || st.Probes != 1 {
		t.Fatalf("stats = %+v", st)
	}
	e.clock.Advance(61 * time.Minute) // 00:31 the next day, and stale again
	e.tick()
	if st := e.p.Stats(); st.Day != "2026-11-14" || st.Probes != 1 {
		t.Fatalf("stats after midnight = %+v, want a fresh count of 1", st)
	}
	logs := e.logs.String()
	if !strings.Contains(logs, "day finished") || !strings.Contains(logs, "day=2026-11-13 probes=1") {
		t.Errorf("finished day not logged:\n%s", logs)
	}
}

// With a fake ticker: nothing is sent before the first tick, a tick sends
// probes, and Stop cancels one that is still in flight.
func TestLoopStartsQuietlyAndStopsPromptly(t *testing.T) {
	started := make(chan struct{}, 1)
	srv := newServer(t, func(w http.ResponseWriter, r *http.Request) {
		started <- struct{}{}
		<-r.Context().Done()
	})
	cfg := &config.Config{Providers: map[string]*config.Provider{"slow": prov("slow", srv.URL, "m1")}}
	routeAll(cfg)
	ticks := make(chan time.Time)
	e := newEnv(t, cfg, func(d *Deps, o *Options) {
		o.ProbeTimeout = time.Minute
		o.NewTicker = func(time.Duration) (<-chan time.Time, func()) { return ticks, func() {} }
	})

	e.p.Start(context.Background())
	time.Sleep(50 * time.Millisecond)
	if srv.total() != 0 {
		t.Fatal("something was sent before the first tick")
	}
	ticks <- t0
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("tick did not probe")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	begin := time.Now()
	if err := e.p.Stop(ctx); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if d := time.Since(begin); d > time.Second {
		t.Errorf("Stop took %v with a probe in flight", d)
	}
}

func TestIntervalOffStartsNothing(t *testing.T) {
	cfg := &config.Config{Providers: map[string]*config.Provider{"fast": prov("fast", "http://127.0.0.1:1", "m1")}}
	routeAll(cfg)
	var made atomic.Bool
	e := newEnv(t, cfg, func(d *Deps, o *Options) {
		o.Interval = 0
		o.NewTicker = func(time.Duration) (<-chan time.Time, func()) {
			made.Store(true)
			return nil, func() {}
		}
	})
	e.p.Start(context.Background())
	if made.Load() {
		t.Error("a ticker was created although the poller is off")
	}
	if err := e.p.Stop(context.Background()); err != nil {
		t.Errorf("Stop on a poller that never started: %v", err)
	}
}

func TestPanicInAJobDoesNotKillTheTick(t *testing.T) {
	cfg := &config.Config{Providers: map[string]*config.Provider{"fast": prov("fast", "http://127.0.0.1:1", "m1")}}
	routeAll(cfg)
	e := newEnv(t, cfg, func(d *Deps, o *Options) { d.Completer = panicCompleter{} })
	e.p.safeTick(context.Background()) // must return
}

type panicCompleter struct{}

func (panicCompleter) Complete(context.Context, upstream.Request) (*upstream.Response, error) {
	panic("boom")
}

func TestIntervalFromEnv(t *testing.T) {
	tests := []struct {
		in      string
		want    time.Duration
		wantErr bool
	}{
		{"", DefaultInterval, false},
		{"30m", 30 * time.Minute, false},
		{" 2h ", 2 * time.Hour, false},
		{"off", 0, false},
		{"OFF", 0, false},
		{"0", 0, false},
		{"10s", 0, true},
		{"soon", 0, true},
		{"-5m", 0, true},
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			got, err := IntervalFromEnv(func(string) string { return tt.in })
			if (err != nil) != tt.wantErr || got != tt.want {
				t.Errorf("got %v, %v; want %v, error=%v", got, err, tt.want, tt.wantErr)
			}
		})
	}
}
