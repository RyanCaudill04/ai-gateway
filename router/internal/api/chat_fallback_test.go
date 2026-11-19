package api

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/RyanCaudill04/ai-gateway/router/internal/apierror"
	"github.com/RyanCaudill04/ai-gateway/router/internal/config"
	"github.com/RyanCaudill04/ai-gateway/router/internal/ratelimit"
	"github.com/RyanCaudill04/ai-gateway/router/internal/upstream"
)

// fallbackYAML has routes for every fallback case. Model IDs contain "/",
// ":" and "@" as real ones do, and the context windows differ so the
// "only bigger models" rule can be seen.
const fallbackYAML = `
providers:
  groq:
    base_url: ${UP}/groq/v1
    models:
      - id: openai/gpt-oss-20b
        context_length: 1000
  gemini:
    base_url: ${UP}/gemini/v1
    models:
      - id: flash
        context_length: 8000
      - id: flash-lite
        context_length: 8000
  openrouter:
    base_url: ${UP}/openrouter/v1
    models:
      - id: google/gemma:free
        context_length: 4000
  workers:
    base_url: ${UP}/workers/v1
    models:
      - id: "@cf/meta/llama"
        context_length: 500
  ollama:
    base_url: ${UP}/ollama/v1
    local: true
    models:
      - id: llama3.2:3b
        context_length: 1000
routes:
  fast: [groq/openai/gpt-oss-20b, gemini/flash, openrouter/google/gemma:free, ollama/llama3.2:3b]
  duo: [groq/openai/gpt-oss-20b, ollama/llama3.2:3b]
  solo: [ollama/llama3.2:3b]
  sizes: [workers/@cf/meta/llama, groq/openai/gpt-oss-20b, ollama/llama3.2:3b, gemini/flash, openrouter/google/gemma:free]
  twins: [gemini/flash, gemini/flash-lite, openrouter/google/gemma:free, ollama/llama3.2:3b]
`

const (
	groqModel   = "openai/gpt-oss-20b"
	ollamaModel = "llama3.2:3b"
)

// ---- fake provider behaviours ----

func answers(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	io.WriteString(w, completion)
}

// fails answers with an error status and an OpenAI-style error body. headers
// are name, value pairs.
func fails(status int, message string, headers ...string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		for i := 0; i+1 < len(headers); i += 2 {
			w.Header().Set(headers[i], headers[i+1])
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		json.NewEncoder(w).Encode(map[string]any{"error": map[string]string{"message": message}})
	}
}

// limited is a 429. retryAfter is in seconds; "" sends none.
func limited(retryAfter string) http.HandlerFunc {
	if retryAfter == "" {
		return fails(429, "Rate limit reached")
	}
	return fails(429, "Rate limit reached", "Retry-After", retryAfter)
}

var tooLong = fails(400, "Please reduce the length of the messages or completion.")

func streams(events ...string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		for _, e := range events {
			sse(w, e)
		}
	}
}

// ---- running a request ----

type result struct {
	status int
	header http.Header
	body   string
	calls  []string // providers the fake upstream saw, in order
	sum    RequestSummary
}

// do sends one request and gathers everything a test checks.
func (f *fixture) do(t *testing.T, model string, stream bool) result {
	t.Helper()
	extra := ""
	if stream {
		extra = `,"stream":true`
	}
	resp := f.post(t, context.Background(), chatBody(model, extra))
	res := result{status: resp.StatusCode, header: resp.Header, body: readAll(t, resp.Body)}
	res.sum = f.summary(t)
	res.calls = f.drainCalls()
	return res
}

// drainCalls returns the providers called so far. The fake records a call
// before it answers, so once the router has responded they are all here.
func (f *fixture) drainCalls() []string {
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

func (r result) fallback() string { return r.header.Get("X-Router-Fallback") }

func (r result) wantCalls(t *testing.T, providers ...string) {
	t.Helper()
	if !slices.Equal(r.calls, providers) {
		t.Errorf("upstream calls = %v, want %v", r.calls, providers)
	}
}

func (r result) wantAnswer(t *testing.T, provider, model, fallback string) {
	t.Helper()
	if r.status != 200 || r.body != completion {
		t.Fatalf("= %d %s, want the provider's answer", r.status, r.body)
	}
	r.wantHeaders(t, provider, model, fallback)
}

func (r result) wantHeaders(t *testing.T, provider, model, fallback string) {
	t.Helper()
	if p, m := r.header.Get("X-Router-Provider"), r.header.Get("X-Router-Model"); p != provider || m != model {
		t.Errorf("x-router-provider/model = %q/%q, want %q/%q", p, m, provider, model)
	}
	if got := r.fallback(); got != fallback {
		t.Errorf("x-router-fallback = %q, want %q", got, fallback)
	}
	if r.sum.Provider != provider || r.sum.Model != model {
		t.Errorf("summary names %q/%q, want %q/%q", r.sum.Provider, r.sum.Model, provider, model)
	}
	if got := FallbackHeader(r.sum.Fallback); got != fallback {
		t.Errorf("summary fallback = %q, want %q", got, fallback)
	}
}

type wireAttempt struct {
	Provider  string `json:"provider"`
	Model     string `json:"model"`
	Type      string `json:"type"`
	Message   string `json:"message"`
	LimitType string `json:"limit_type"`
	Window    string `json:"window"`
	ResetsAt  string `json:"resets_at"`
}

type wireError struct {
	Message   string        `json:"message"`
	Type      string        `json:"type"`
	Code      string        `json:"code"`
	Provider  string        `json:"provider"`
	Model     string        `json:"model"`
	LimitType string        `json:"limit_type"`
	Window    string        `json:"window"`
	ResetsAt  string        `json:"resets_at"`
	Attempts  []wireAttempt `json:"attempts"`
}

func decodeError(t *testing.T, body string) wireError {
	t.Helper()
	var env struct {
		Error *wireError `json:"error"`
	}
	if err := json.Unmarshal([]byte(body), &env); err != nil || env.Error == nil {
		t.Fatalf("not an error envelope: %v: %s", err, body)
	}
	return *env.Error
}

// wantReset checks an RFC 3339 reset time is about d after the fixture's now.
func (f *fixture) wantReset(t *testing.T, what, got string, d time.Duration) {
	t.Helper()
	at, err := time.Parse(time.RFC3339, got)
	if err != nil {
		t.Errorf("%s resets_at = %q: %v", what, got, err)
		return
	}
	if diff := at.Sub(f.clock.now().Add(d)); diff < -5*time.Second || diff > 5*time.Second {
		t.Errorf("%s resets_at = %s, want about %s from now", what, got, d)
	}
}

// ---- the issue's four cases ----

func TestRouteFirstEntryAnswers(t *testing.T) {
	f := newFixtureWith(t, fixtureOpts{yaml: fallbackYAML, all: answers})
	res := f.do(t, "fast", false)
	res.wantAnswer(t, "groq", groqModel, "")
	res.wantCalls(t, "groq")
	if _, present := res.header["X-Router-Fallback"]; present {
		t.Error("x-router-fallback set although nothing fell back")
	}
	if res.sum.Attempts != 1 || len(res.sum.Fallback) != 0 || res.sum.ErrorType != "" {
		t.Errorf("summary = %+v", res.sum)
	}
}

// First entry 429 → the second answers. The tracker then knows the first is
// limited, so the next request skips it without calling it; once its window
// has reset, it is tried again.
func TestRouteFallsBackThenSkipsUntilTheReset(t *testing.T) {
	f := newFixtureWith(t, fixtureOpts{yaml: fallbackYAML, each: map[string]http.HandlerFunc{
		"groq": limited("30"), "gemini": answers,
	}})
	const trail = "groq/openai/gpt-oss-20b=rate_limited_minute"

	res := f.do(t, "fast", false)
	res.wantAnswer(t, "gemini", "flash", trail)
	res.wantCalls(t, "groq", "gemini")
	want := []FallbackAttempt{{Provider: "groq", Model: groqModel, Reason: ReasonRateLimitedMinute, Called: true}}
	if res.sum.Attempts != 2 || !slices.Equal(res.sum.Fallback, want) || res.sum.Status != 200 || res.sum.ErrorType != "" {
		t.Errorf("summary = %+v", res.sum)
	}

	// Second request: groq is not called at all, but still listed.
	res = f.do(t, "fast", false)
	res.wantAnswer(t, "gemini", "flash", trail)
	res.wantCalls(t, "gemini")
	want[0].Called = false
	if res.sum.Attempts != 1 || !slices.Equal(res.sum.Fallback, want) {
		t.Errorf("summary after a skip = %+v", res.sum)
	}

	// Just before the reset it is still skipped; after it, tried again.
	f.clock.advance(25 * time.Second)
	f.do(t, "fast", false).wantCalls(t, "gemini")
	f.clock.advance(6 * time.Second)
	f.up.set("groq", answers)
	res = f.do(t, "fast", false)
	res.wantAnswer(t, "groq", groqModel, "")
	res.wantCalls(t, "groq")
}

func TestRouteAllEntriesFail(t *testing.T) {
	f := newFixtureWith(t, fixtureOpts{yaml: fallbackYAML, each: map[string]http.HandlerFunc{
		"groq":       limited("3600"),
		"gemini":     fails(500, "upstream exploded"),
		"openrouter": limited(""),
		"ollama":     fails(503, "model is loading"),
	}})
	const trail = "groq/openai/gpt-oss-20b=rate_limited_day, gemini/flash=provider_error, " +
		"openrouter/google/gemma:free=rate_limited_minute, ollama/llama3.2:3b=provider_error"

	res := f.do(t, "fast", false)
	res.wantCalls(t, "groq", "gemini", "openrouter", "ollama")
	// Not every attempt was a rate limit, so 503 rather than 429.
	if res.status != 503 {
		t.Fatalf("status = %d, want 503: %s", res.status, res.body)
	}
	res.wantHeaders(t, "ollama", ollamaModel, trail) // the last one called
	e := decodeError(t, res.body)
	if e.Type != "all_providers_exhausted" || e.Code != "all_providers_exhausted" {
		t.Errorf("error = %+v", e)
	}
	want := []wireAttempt{
		{Provider: "groq", Model: groqModel, Type: "rate_limited", Message: "Rate limit reached", LimitType: "requests", Window: "day"},
		{Provider: "gemini", Model: "flash", Type: "provider_error", Message: "upstream exploded"},
		{Provider: "openrouter", Model: "google/gemma:free", Type: "rate_limited", Message: "Rate limit reached", LimitType: "requests", Window: "minute"},
		{Provider: "ollama", Model: ollamaModel, Type: "provider_error", Message: "model is loading"},
	}
	if len(e.Attempts) != len(want) {
		t.Fatalf("attempts = %+v, want %d of them", e.Attempts, len(want))
	}
	f.wantReset(t, "groq", e.Attempts[0].ResetsAt, time.Hour)
	// No Retry-After: the reset is the router's own one-minute back-off,
	// which is when a route will next try that entry.
	f.wantReset(t, "openrouter", e.Attempts[2].ResetsAt, ratelimit.DefaultBackoff)
	for i := range want {
		got := e.Attempts[i]
		got.ResetsAt = ""
		if got != want[i] {
			t.Errorf("attempts[%d] = %+v, want %+v", i, got, want[i])
		}
	}
	// The earliest reset is the first moment a retry could work.
	f.wantReset(t, "top-level", e.ResetsAt, ratelimit.DefaultBackoff)
	if ra, _ := strconv.Atoi(res.header.Get("Retry-After")); ra < 50 || ra > 60 {
		t.Errorf("Retry-After = %q, want about 60", res.header.Get("Retry-After"))
	}
	if res.sum.Attempts != 4 || res.sum.Status != 503 || res.sum.ErrorType != apierror.TypeAllProvidersExhausted || len(res.sum.Fallback) != 4 {
		t.Errorf("summary = %+v", res.sum)
	}

	// Asked again at once: the two limited entries are skipped without a
	// call, and say so; the other two are tried again.
	res = f.do(t, "fast", false)
	res.wantCalls(t, "gemini", "ollama")
	res.wantHeaders(t, "ollama", ollamaModel, trail)
	e = decodeError(t, res.body)
	if len(e.Attempts) != 4 {
		t.Fatalf("attempts = %+v", e.Attempts)
	}
	for _, i := range []int{0, 2} {
		a := e.Attempts[i]
		if a.Type != "rate_limited" || !strings.HasPrefix(a.Message, "Not tried") || a.LimitType != "requests" || a.ResetsAt == "" {
			t.Errorf("skipped attempts[%d] = %+v", i, a)
		}
	}
	if e.Attempts[0].Window != "day" || e.Attempts[2].Window != "minute" {
		t.Errorf("skipped windows = %q, %q", e.Attempts[0].Window, e.Attempts[2].Window)
	}
	called := []bool{false, true, false, true}
	for i, a := range res.sum.Fallback {
		if a.Called != called[i] {
			t.Errorf("summary fallback[%d].Called = %v, want %v", i, a.Called, called[i])
		}
	}
	if res.sum.Attempts != 2 {
		t.Errorf("summary attempts = %d, want 2 (skips are not calls)", res.sum.Attempts)
	}
}

// When every attempt was a rate limit the answer is 429, and with every
// entry already known to be exhausted nobody is called at all.
func TestRouteAllRateLimitedIs429(t *testing.T) {
	f := newFixtureWith(t, fixtureOpts{yaml: fallbackYAML, all: limited("20")})
	res := f.do(t, "duo", false)
	res.wantCalls(t, "groq", "ollama")
	if e := decodeError(t, res.body); res.status != 429 || e.Type != "all_providers_exhausted" || len(e.Attempts) != 2 {
		t.Fatalf("= %d %+v", res.status, e)
	}
	if ra, _ := strconv.Atoi(res.header.Get("Retry-After")); ra < 15 || ra > 20 {
		t.Errorf("Retry-After = %q, want about 20", res.header.Get("Retry-After"))
	}

	res = f.do(t, "duo", false)
	res.wantCalls(t)
	if res.status != 429 || decodeError(t, res.body).Type != "all_providers_exhausted" {
		t.Fatalf("= %d %s", res.status, res.body)
	}
	// Nobody was called, so no provider is named; the trail still is.
	res.wantHeaders(t, "", "", "groq/openai/gpt-oss-20b=rate_limited_minute, ollama/llama3.2:3b=rate_limited_minute")
	if res.sum.Attempts != 0 || res.sum.Status != 429 {
		t.Errorf("summary = %+v", res.sum)
	}
}

func TestPinnedNeverFallsBack(t *testing.T) {
	f := newFixtureWith(t, fixtureOpts{yaml: fallbackYAML, all: answers, each: map[string]http.HandlerFunc{
		"groq": limited("30"),
	}})
	res := f.do(t, "groq/"+groqModel, false)
	res.wantCalls(t, "groq") // and nobody else
	if res.status != 429 {
		t.Fatalf("status = %d: %s", res.status, res.body)
	}
	res.wantHeaders(t, "groq", groqModel, "")
	e := decodeError(t, res.body)
	if e.Type != "rate_limited" || e.Provider != "groq" || e.Model != groqModel ||
		e.LimitType != "requests" || e.Window != "minute" || len(e.Attempts) != 0 {
		t.Errorf("error = %+v", e)
	}
	f.wantReset(t, "error", e.ResetsAt, 30*time.Second)
	if !strings.Contains(e.Message, "per-minute request limit") {
		t.Errorf("message = %q", e.Message)
	}
	if res.sum.Attempts != 1 || len(res.sum.Fallback) != 0 || res.sum.ErrorType != apierror.TypeRateLimited {
		t.Errorf("summary = %+v", res.sum)
	}
}

// ---- pinned models and the tracker ----

// A pinned model the tracker believes is exhausted is still sent to the
// provider: stale or guessed tracker data must never lock a model out.
func TestPinnedExhaustedModelIsStillCalled(t *testing.T) {
	f := newFixtureWith(t, fixtureOpts{yaml: fallbackYAML, all: answers})
	f.obs.Tracker.ObserveError(&upstream.Error{Kind: upstream.KindRateLimited, Provider: "groq", Model: groqModel,
		RetryAfter: time.Hour}, f.clock.now())
	if _, out := f.obs.Exhausted("groq", groqModel, f.clock.now()); !out {
		t.Fatal("setup: the tracker should hold groq as exhausted")
	}

	// A route skips it...
	res := f.do(t, "duo", false)
	res.wantAnswer(t, "ollama", ollamaModel, "groq/openai/gpt-oss-20b=rate_limited_day")
	res.wantCalls(t, "ollama")

	// ...the same model pinned is asked, and here the provider answers.
	res = f.do(t, "groq/"+groqModel, false)
	res.wantAnswer(t, "groq", groqModel, "")
	res.wantCalls(t, "groq")
}

// What a pinned 429 says about the limit comes from the tracker.
func TestPinnedRateLimitDetails(t *testing.T) {
	tests := []struct {
		name      string
		headers   []string
		limitType string
		window    string
		reset     time.Duration // 0: resets_at must be absent
	}{
		{
			name:      "Retry-After only: a requests window as long as the wait",
			headers:   []string{"Retry-After", "30"},
			limitType: "requests", window: "minute", reset: 30 * time.Second,
		},
		{
			name:      "a long wait is a day limit",
			headers:   []string{"Retry-After", "7200"},
			limitType: "requests", window: "day", reset: 2 * time.Hour,
		},
		{
			name: "headers name the limit that ran out",
			headers: []string{"Retry-After", "20", "X-Ratelimit-Limit-Tokens", "6000",
				"X-Ratelimit-Remaining-Tokens", "0", "X-Ratelimit-Reset-Tokens", "20s",
				"X-Ratelimit-Remaining-Requests", "50", "X-Ratelimit-Reset-Requests", "40s"},
			limitType: "tokens", window: "minute", reset: 20 * time.Second,
		},
		{
			// The tracker backs off for a minute it made up. A pinned error
			// must not present that guess as the provider's reset time.
			name: "no time from the provider: nothing invented",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newFixtureWith(t, fixtureOpts{yaml: fallbackYAML, all: fails(429, "Rate limit reached", tt.headers...)})
			res := f.do(t, "gemini/flash", false)
			e := decodeError(t, res.body)
			if res.status != 429 || e.Type != "rate_limited" || e.Provider != "gemini" || e.Model != "flash" {
				t.Fatalf("= %d %+v", res.status, e)
			}
			if e.LimitType != tt.limitType || e.Window != tt.window {
				t.Errorf("limit_type/window = %q/%q, want %q/%q", e.LimitType, e.Window, tt.limitType, tt.window)
			}
			if tt.reset == 0 {
				if e.ResetsAt != "" || res.header.Get("Retry-After") != "" {
					t.Errorf("resets_at = %q, Retry-After = %q, want neither", e.ResetsAt, res.header.Get("Retry-After"))
				}
				return
			}
			f.wantReset(t, "error", e.ResetsAt, tt.reset)
		})
	}
}

// ---- which failures fall through ----

func TestRouteFallThroughByKind(t *testing.T) {
	hang := func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-time.After(3 * time.Second):
		}
	}
	tests := []struct {
		name   string
		first  http.HandlerFunc // what groq, the first entry of `duo`, does
		noKey  bool             // groq has no key registered
		reason string           // groq's reason in x-router-fallback
		// status and typ are set when the route must NOT move on: the
		// second entry is never called and the client gets this error.
		status int
		typ    string
	}{
		{name: "429", first: limited("5"), reason: "rate_limited_minute"},
		{name: "500", first: fails(500, "boom"), reason: "provider_error"},
		{name: "503", first: fails(503, "overloaded"), reason: "provider_error"},
		{name: "timeout", first: hang, reason: "provider_error"},
		{name: "provider key rejected", first: fails(401, "invalid api key"), reason: "provider_error"},
		{name: "unexpected status", first: fails(404, "no such model"), reason: "provider_error"},
		{name: "no key registered", noKey: true, reason: "provider_error"},
		{
			// OpenRouter style: HTTP 200 with an error object as the body.
			name: "error inside a 200 body",
			first: func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				io.WriteString(w, `{"error":{"code":429,"message":"Rate limit exceeded"}}`)
			},
			reason: "rate_limited_minute",
		},
		{name: "invalid request, first provider to say so", first: fails(400, "logprobs is not supported"), reason: "invalid_request"},
		{name: "request too large for this provider (413)", first: fails(413, "Request too large"), reason: "context_length_exceeded"},
		{
			// The second entry's context window is no bigger, so it is ruled
			// out without a call and the client gets the 400.
			name: "prompt too long, nothing bigger left", first: tooLong, reason: "context_length_exceeded",
			status: 400, typ: "context_length_exceeded",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			o := fixtureOpts{
				yaml:   fallbackYAML,
				client: upstream.Options{FirstByteTimeout: 100 * time.Millisecond, ResponseTimeout: 100 * time.Millisecond},
				each:   map[string]http.HandlerFunc{"groq": tt.first, "ollama": answers},
			}
			if tt.noKey {
				o.noKey = []string{"groq"}
			}
			f := newFixtureWith(t, o)
			res := f.do(t, "duo", false)

			if tt.status != 0 {
				res.wantCalls(t, "groq")
				if e := decodeError(t, res.body); res.status != tt.status || e.Type != tt.typ || e.Provider != "groq" {
					t.Errorf("= %d %+v, want %d %s from groq", res.status, e, tt.status, tt.typ)
				}
				return
			}
			res.wantAnswer(t, "ollama", ollamaModel, "groq/openai/gpt-oss-20b="+tt.reason)
			if tt.noKey {
				// Nothing was sent to groq, and the log can tell.
				res.wantCalls(t, "ollama")
				if res.sum.Attempts != 1 || res.sum.Fallback[0].Called {
					t.Errorf("summary = %+v, want one call and groq not called", res.sum)
				}
				return
			}
			res.wantCalls(t, "groq", "ollama")
			if res.sum.Attempts != 2 || !res.sum.Fallback[0].Called {
				t.Errorf("summary = %+v", res.sum)
			}
		})
	}
}

// The fall-through policy on its own, as a sequence of failures.
func TestFallbackStateAfter(t *testing.T) {
	entry := func(provider string, window int) target {
		return target{provider: &config.Provider{Name: provider}, model: config.Model{ID: "m", ContextLength: window}}
	}
	type step struct {
		t      target
		kind   upstream.Kind
		status int
		moveOn bool
	}
	tests := []struct {
		name     string
		steps    []step
		tooSmall map[int]bool // context window → ruled out afterwards
	}{
		{
			name:     "rate limits and provider errors always move on and rule nothing out",
			steps:    []step{{entry("a", 100), upstream.KindRateLimited, 429, true}, {entry("b", 100), upstream.KindProvider, 500, true}, {entry("c", 100), upstream.KindProvider, 0, true}},
			tooSmall: map[int]bool{1: false, 100: false},
		},
		{
			name:     "too long: only bigger models are left",
			steps:    []step{{entry("a", 1000), upstream.KindContextTooLong, 400, true}},
			tooSmall: map[int]bool{500: true, 1000: true, 1001: false},
		},
		{
			name: "too long twice: the largest window that refused counts",
			steps: []step{{entry("a", 8000), upstream.KindContextTooLong, 400, true},
				{entry("b", 1000), upstream.KindContextTooLong, 200, true}},
			tooSmall: map[int]bool{4000: true, 8000: true, 9000: false},
		},
		{
			name:     "413 is the provider's own size cap: nothing is ruled out",
			steps:    []step{{entry("a", 1000), upstream.KindContextTooLong, 413, true}},
			tooSmall: map[int]bool{500: false, 1000: false},
		},
		{
			name:  "invalid request: one provider may be wrong",
			steps: []step{{entry("a", 100), upstream.KindBadRequest, 400, true}},
		},
		{
			name: "invalid request: the same provider again proves nothing new",
			steps: []step{{entry("a", 100), upstream.KindBadRequest, 400, true},
				{entry("a", 100), upstream.KindBadRequest, 422, true}},
		},
		{
			name: "invalid request: a second provider settles it",
			steps: []step{{entry("a", 100), upstream.KindBadRequest, 400, true},
				{entry("b", 100), upstream.KindRateLimited, 429, true},
				{entry("c", 100), upstream.KindBadRequest, 400, false}},
		},
		{
			name:  "a kind this code has never heard of moves on",
			steps: []step{{entry("a", 100), upstream.Kind("something_new"), 418, true}},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var st fallbackState
			for i, s := range tt.steps {
				ue := &upstream.Error{Kind: s.kind, Status: s.status}
				if got := st.after(s.t, ue); got != s.moveOn {
					t.Errorf("step %d (%s from %s): moveOn = %v, want %v", i, s.kind, s.t.provider.Name, got, s.moveOn)
				}
			}
			for window, want := range tt.tooSmall {
				if got := st.tooSmall(entry("x", window)); got != want {
					t.Errorf("tooSmall(window %d) = %v, want %v", window, got, want)
				}
			}
		})
	}
}

// After a "too long", only entries with a bigger context window are worth a
// request; the rest are listed but never called.
func TestRouteContextTooLongOnlyTriesBiggerModels(t *testing.T) {
	// sizes: workers 500, groq 1000, ollama 1000, gemini 8000, openrouter 4000
	f := newFixtureWith(t, fixtureOpts{yaml: fallbackYAML, all: tooLong, each: map[string]http.HandlerFunc{"gemini": answers}})
	const refused = "workers/@cf/meta/llama=context_length_exceeded, groq/openai/gpt-oss-20b=context_length_exceeded, " +
		"ollama/llama3.2:3b=context_length_exceeded"

	res := f.do(t, "sizes", false)
	res.wantAnswer(t, "gemini", "flash", refused)
	res.wantCalls(t, "workers", "groq", "gemini") // ollama is no bigger than groq
	if res.sum.Attempts != 3 || res.sum.Fallback[2].Called {
		t.Errorf("summary = %+v", res.sum)
	}

	// Too long for the biggest as well: nobody was out of quota or down, so
	// this is the client's 400, not all_providers_exhausted.
	f.up.set("gemini", tooLong)
	res = f.do(t, "sizes", false)
	res.wantCalls(t, "workers", "groq", "gemini") // openrouter (4000) is smaller than gemini
	if res.status != 400 {
		t.Fatalf("status = %d, want 400: %s", res.status, res.body)
	}
	res.wantHeaders(t, "gemini", "flash", refused+", gemini/flash=context_length_exceeded, openrouter/google/gemma:free=context_length_exceeded")
	e := decodeError(t, res.body)
	if e.Type != "context_length_exceeded" || e.Provider != "gemini" || e.Model != "flash" || len(e.Attempts) != 0 {
		t.Errorf("error = %+v", e)
	}
	if res.sum.ErrorType != apierror.TypeContextLengthExceeded || res.sum.Status != 400 {
		t.Errorf("summary = %+v", res.sum)
	}
}

// A too-long prompt is the client's fault only if every entry judged it so.
// One entry that was merely out of quota keeps it all_providers_exhausted:
// that one may serve the request later.
func TestRouteRefusalsMixedWithOutagesStayExhausted(t *testing.T) {
	f := newFixtureWith(t, fixtureOpts{yaml: fallbackYAML, each: map[string]http.HandlerFunc{
		"groq": limited("10"), "ollama": fails(400, "tool_choice is not supported"),
	}})
	res := f.do(t, "duo", false)
	e := decodeError(t, res.body)
	if res.status != 503 || e.Type != "all_providers_exhausted" || len(e.Attempts) != 2 ||
		e.Attempts[0].Type != "rate_limited" || e.Attempts[1].Type != "invalid_request" {
		t.Errorf("= %d %+v", res.status, e)
	}
}

func TestRouteInvalidRequest(t *testing.T) {
	t.Run("one provider's quirk is rescued by the next", func(t *testing.T) {
		f := newFixtureWith(t, fixtureOpts{yaml: fallbackYAML, all: answers, each: map[string]http.HandlerFunc{
			"groq": fails(400, "property 'name' is unsupported"),
		}})
		res := f.do(t, "fast", false)
		res.wantAnswer(t, "gemini", "flash", "groq/openai/gpt-oss-20b=invalid_request")
		res.wantCalls(t, "groq", "gemini")
	})

	t.Run("a second provider rejecting it stops the route", func(t *testing.T) {
		// twins: gemini/flash, gemini/flash-lite, openrouter, ollama
		f := newFixtureWith(t, fixtureOpts{yaml: fallbackYAML, all: fails(400, "messages is required"), each: map[string]http.HandlerFunc{
			"ollama": answers, // would have answered: the route must not get this far
		}})
		res := f.do(t, "twins", false)
		// Both gemini models are asked (one provider's opinion, twice), then
		// openrouter confirms it and ollama is never called.
		res.wantCalls(t, "gemini", "gemini", "openrouter")
		if res.status != 400 {
			t.Fatalf("status = %d: %s", res.status, res.body)
		}
		res.wantHeaders(t, "openrouter", "google/gemma:free",
			"gemini/flash=invalid_request, gemini/flash-lite=invalid_request, openrouter/google/gemma:free=invalid_request")
		e := decodeError(t, res.body)
		if e.Type != "invalid_request" || e.Provider != "openrouter" || e.Message != "messages is required" {
			t.Errorf("error = %+v", e)
		}
		if res.sum.Attempts != 3 || res.sum.ErrorType != apierror.TypeInvalidRequest {
			t.Errorf("summary = %+v", res.sum)
		}
	})
}

// ---- skipping ----

// An entry the tracker learned about from response headers (not from a 429)
// is skipped too, and the header says which window ran out.
func TestRouteSkipsEntryExhaustedByHeaders(t *testing.T) {
	tests := []struct {
		name    string
		headers []string
		reason  string
		attempt wireAttempt
	}{
		{
			name: "day requests used up",
			headers: []string{"X-Ratelimit-Remaining-Requests-Day", "0", "X-Ratelimit-Limit-Requests-Day", "1000",
				"X-Ratelimit-Reset-Requests-Day", "5h"},
			reason:  "rate_limited_day",
			attempt: wireAttempt{LimitType: "requests", Window: "day"},
		},
		{
			name:    "minute tokens nearly gone",
			headers: []string{"X-Ratelimit-Remaining-Tokens", "12", "X-Ratelimit-Reset-Tokens", "40s"},
			reason:  "rate_limited_minute",
			attempt: wireAttempt{LimitType: "tokens", Window: "minute"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newFixtureWith(t, fixtureOpts{yaml: fallbackYAML, each: map[string]http.HandlerFunc{
				"groq": func(w http.ResponseWriter, r *http.Request) {
					for i := 0; i+1 < len(tt.headers); i += 2 {
						w.Header().Set(tt.headers[i], tt.headers[i+1])
					}
					answers(w, r)
				},
				"ollama": fails(500, "down"),
			}})
			// The first request succeeds and its headers say the window is spent.
			f.do(t, "duo", false).wantAnswer(t, "groq", groqModel, "")

			res := f.do(t, "duo", false)
			res.wantCalls(t, "ollama")
			if got, want := res.fallback(), "groq/openai/gpt-oss-20b="+tt.reason+", ollama/llama3.2:3b=provider_error"; got != want {
				t.Errorf("x-router-fallback = %q, want %q", got, want)
			}
			e := decodeError(t, res.body)
			if e.Type != "all_providers_exhausted" || len(e.Attempts) != 2 {
				t.Fatalf("error = %+v", e)
			}
			a := e.Attempts[0]
			if a.Type != "rate_limited" || a.LimitType != tt.attempt.LimitType || a.Window != tt.attempt.Window || a.ResetsAt == "" {
				t.Errorf("skipped attempt = %+v", a)
			}
		})
	}
}

func TestRouteSkipsDisabledProvidersSilently(t *testing.T) {
	f := newFixtureWith(t, fixtureOpts{yaml: fallbackYAML, all: answers, enabled: []string{"gemini", "ollama"}})
	res := f.do(t, "fast", false)
	// groq and openrouter have no key: not called, and not listed either.
	res.wantAnswer(t, "gemini", "flash", "")
	res.wantCalls(t, "gemini")

	f.up.set("gemini", fails(500, "boom"))
	res = f.do(t, "fast", false)
	res.wantAnswer(t, "ollama", ollamaModel, "gemini/flash=provider_error")
	res.wantCalls(t, "gemini", "ollama")
}

func TestRouteWithOneEntry(t *testing.T) {
	f := newFixtureWith(t, fixtureOpts{yaml: fallbackYAML, all: answers})
	f.do(t, "solo", false).wantAnswer(t, "ollama", ollamaModel, "")

	// It is still a route: its failure is all_providers_exhausted with the
	// one attempt, not the pinned single-provider error.
	f.up.set("ollama", fails(500, "model crashed"))
	res := f.do(t, "solo", false)
	res.wantCalls(t, "ollama")
	res.wantHeaders(t, "ollama", ollamaModel, "ollama/llama3.2:3b=provider_error")
	e := decodeError(t, res.body)
	if res.status != 503 || e.Type != "all_providers_exhausted" || len(e.Attempts) != 1 ||
		e.Attempts[0].Message != "model crashed" || !strings.HasPrefix(e.Message, "The only provider") {
		t.Errorf("= %d %+v", res.status, e)
	}

	// Unless the request itself is what's wrong.
	f.up.set("ollama", fails(400, "bad tool schema"))
	res = f.do(t, "solo", false)
	if e := decodeError(t, res.body); res.status != 400 || e.Type != "invalid_request" || e.Provider != "ollama" {
		t.Errorf("= %d %+v, want the provider's 400", res.status, e)
	}
}

// Once the budget is spent no further entry is started.
func TestRouteBudgetStopsNewAttempts(t *testing.T) {
	f := newFixtureWith(t, fixtureOpts{yaml: fallbackYAML, budget: time.Nanosecond, each: map[string]http.HandlerFunc{
		"groq": fails(500, "slow and broken"), "ollama": answers,
	}})
	res := f.do(t, "duo", false)
	res.wantCalls(t, "groq")
	e := decodeError(t, res.body)
	if res.status != 503 || e.Type != "all_providers_exhausted" || len(e.Attempts) != 1 {
		t.Errorf("= %d %+v", res.status, e)
	}
	res.wantHeaders(t, "groq", groqModel, "groq/openai/gpt-oss-20b=provider_error")
}

// ---- streaming: fallback only before the first token ----

func TestStreamFallsBackBeforeTheFirstToken(t *testing.T) {
	tests := []struct {
		name   string
		first  http.HandlerFunc
		reason string
	}{
		{name: "error status before the stream", first: limited("30"), reason: "rate_limited_minute"},
		{name: "5xx before the stream", first: fails(502, "bad gateway"), reason: "provider_error"},
		{
			name:   "error as the first event of a 200 stream",
			first:  streams(`{"error":{"code":429,"message":"Rate limit exceeded"}}`),
			reason: "rate_limited_minute",
		},
		{
			name: "keep-alive comments, then an error event",
			first: func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				io.WriteString(w, ": PROCESSING\n\n")
				w.(http.Flusher).Flush()
				sse(w, `{"error":{"code":502,"message":"provider unavailable"}}`)
			},
			reason: "provider_error",
		},
		{
			name: "connection dropped before the first event",
			first: func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				w.WriteHeader(200)
				w.(http.Flusher).Flush()
				panic(http.ErrAbortHandler) // closes the connection mid-response
			},
			reason: "provider_error",
		},
		{name: "stream that ends without any event", first: streams("[DONE]"), reason: "provider_error"},
		{name: "stream with only the hidden usage chunk", first: streams(usageOnly, "[DONE]"), reason: "provider_error"},
		{
			name: "200 with a JSON error instead of a stream",
			first: func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				io.WriteString(w, `{"error":{"message":"model overloaded"}}`)
			},
			reason: "provider_error",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newFixtureWith(t, fixtureOpts{yaml: fallbackYAML, each: map[string]http.HandlerFunc{
				"groq":   tt.first,
				"ollama": streams(chunk1, chunk2, usageOnly, "[DONE]"),
			}})
			res := f.do(t, "duo", true)
			if res.status != 200 {
				t.Fatalf("status = %d: %s", res.status, res.body)
			}
			// One clean stream, entirely from the second provider: nothing
			// of the first one's failure leaks into it.
			if want := "data: " + chunk1 + "\n\ndata: " + chunk2 + "\n\ndata: [DONE]\n\n"; res.body != want {
				t.Errorf("stream = %q, want %q", res.body, want)
			}
			if ct := res.header.Get("Content-Type"); ct != "text/event-stream" {
				t.Errorf("Content-Type = %q", ct)
			}
			res.wantHeaders(t, "ollama", ollamaModel, "groq/openai/gpt-oss-20b="+tt.reason)
			res.wantCalls(t, "groq", "ollama")
			if res.sum.Attempts != 2 || res.sum.ErrorType != "" || res.sum.Status != 200 ||
				res.sum.Usage == nil || *res.sum.Usage != (Usage{5, 2}) {
				t.Errorf("summary = %+v usage=%v", res.sum, res.sum.Usage)
			}
		})
	}
}

// Once one chunk has been forwarded the route is over: the failure is
// reported inside the stream and no other provider is called.
func TestStreamNeverFallsBackAfterTheFirstChunk(t *testing.T) {
	tests := []struct {
		name  string
		after http.HandlerFunc // what groq does after sending chunk1
		typ   string
	}{
		{
			name: "error event",
			after: func(w http.ResponseWriter, r *http.Request) {
				sse(w, `{"error":{"code":429,"message":"Rate limit exceeded"}}`)
			},
			typ: "rate_limited",
		},
		{
			name:  "connection dropped",
			after: func(w http.ResponseWriter, r *http.Request) { panic(http.ErrAbortHandler) },
			typ:   "provider_error",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newFixtureWith(t, fixtureOpts{yaml: fallbackYAML, each: map[string]http.HandlerFunc{
				"groq": func(w http.ResponseWriter, r *http.Request) {
					w.Header().Set("Content-Type", "text/event-stream")
					sse(w, chunk1)
					tt.after(w, r)
				},
				"ollama": streams(chunk2, "[DONE]"),
			}})
			res := f.do(t, "duo", true)
			res.wantCalls(t, "groq") // ollama must never be asked
			if res.status != 200 {
				t.Fatalf("status = %d", res.status)
			}
			res.wantHeaders(t, "groq", groqModel, "")

			br := bufio.NewReader(strings.NewReader(res.body))
			if got, err := readEvent(br); err != nil || got != chunk1 {
				t.Fatalf("first event = %q %v", got, err)
			}
			data, err := readEvent(br)
			if err != nil {
				t.Fatalf("no error event after the first chunk: %v (%q)", err, res.body)
			}
			if e := decodeError(t, data); e.Type != tt.typ || e.Provider != "groq" || e.Model != groqModel {
				t.Errorf("error event = %+v", e)
			}
			// Nothing after it: no [DONE], and none of ollama's chunks.
			if rest := readAll(t, br); rest != "" {
				t.Errorf("after the error event: %q", rest)
			}
			if res.sum.Attempts != 1 || string(res.sum.ErrorType) != tt.typ || len(res.sum.Fallback) != 0 {
				t.Errorf("summary = %+v", res.sum)
			}
		})
	}
}

// A rate limit reported inside a stream carries the tracker's details too.
func TestStreamErrorEventHasLimitDetails(t *testing.T) {
	f := newFixtureWith(t, fixtureOpts{yaml: fallbackYAML, all: func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("X-Ratelimit-Remaining-Tokens", "0")
		w.Header().Set("X-Ratelimit-Reset-Tokens", "30s")
		sse(w, chunk1)
		sse(w, `{"error":{"code":429,"message":"Rate limit exceeded"}}`)
	}})
	res := f.do(t, "groq/"+groqModel, true)
	br := bufio.NewReader(strings.NewReader(res.body))
	readEvent(br)
	data, err := readEvent(br)
	if err != nil {
		t.Fatal(err)
	}
	e := decodeError(t, data)
	if e.Type != "rate_limited" || e.LimitType != "tokens" || e.Window != "minute" {
		t.Errorf("error event = %+v", e)
	}
	f.wantReset(t, "error event", e.ResetsAt, 30*time.Second)
}

// ---- the client going away ----

// The client hangs up while the first provider is being waited on: the
// router stops there. No other entry is called and nothing is written.
func TestClientDisconnectStopsTheRoute(t *testing.T) {
	for _, stream := range []bool{false, true} {
		t.Run("stream="+strconv.FormatBool(stream), func(t *testing.T) {
			arrived := make(chan struct{})
			f := newFixtureWith(t, fixtureOpts{yaml: fallbackYAML, each: map[string]http.HandlerFunc{
				"groq": func(w http.ResponseWriter, r *http.Request) {
					close(arrived)
					// Answer with a failure that would normally fall through,
					// but only once the router has abandoned this call.
					select {
					case <-r.Context().Done():
					case <-time.After(3 * time.Second):
					}
					limited("1")(w, r)
				},
				"ollama": answers,
			}})
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			extra := ""
			if stream {
				extra = `,"stream":true`
			}
			req, _ := http.NewRequestWithContext(ctx, "POST", f.router.URL, strings.NewReader(chatBody("duo", extra)))
			done := make(chan error, 1)
			go func() {
				resp, err := http.DefaultClient.Do(req)
				if err == nil {
					resp.Body.Close()
				}
				done <- err
			}()
			<-arrived
			cancel()
			if err := <-done; err == nil {
				t.Fatal("request succeeded, want it cancelled")
			}

			s := f.summary(t)
			if !s.ClientGone || s.Status != StatusClientClosed || s.ErrorType != "" || s.Attempts != 1 {
				t.Errorf("summary = %+v", s)
			}
			if calls := f.drainCalls(); !slices.Equal(calls, []string{"groq"}) {
				t.Errorf("upstream calls = %v, want only groq", calls)
			}
			// A client leaving says nothing about the provider.
			if _, out := f.obs.Exhausted("groq", groqModel, f.clock.now()); out {
				t.Error("the tracker marked groq exhausted because the client left")
			}
		})
	}
}

// A request whose client is already gone makes no upstream call at all.
func TestClientGoneBeforeTheFirstAttempt(t *testing.T) {
	f := newFixtureWith(t, fixtureOpts{yaml: fallbackYAML, all: answers})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	for _, model := range []string{"fast", "groq/" + groqModel} {
		r := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(chatBody(model, ""))).WithContext(ctx)
		w := httptest.NewRecorder()
		f.handler.ServeHTTP(w, r)
		if w.Body.Len() != 0 {
			t.Errorf("%s: wrote %q to a client that is gone", model, w.Body)
		}
		if s := f.summary(t); !s.ClientGone || s.Status != StatusClientClosed {
			t.Errorf("%s: summary = %+v", model, s)
		}
	}
	// The pinned request gets as far as the upstream client, which refuses to
	// send on a dead context; the route doesn't even try.
	if calls := f.drainCalls(); len(calls) != 0 {
		t.Errorf("upstream calls = %v, want none", calls)
	}
}

// ---- the x-router-fallback value ----

// parseFallback reads the header back by the rules in FallbackHeader's
// comment, as a client would.
func parseFallback(t *testing.T, v string) []FallbackAttempt {
	t.Helper()
	var out []FallbackAttempt
	for _, item := range strings.Split(v, ",") {
		id, reason, ok := strings.Cut(strings.TrimSpace(item), "=")
		provider, model, ok2 := strings.Cut(id, "/")
		p, err1 := url.PathUnescape(provider)
		m, err2 := url.PathUnescape(model)
		if !ok || !ok2 || err1 != nil || err2 != nil {
			t.Fatalf("unparseable item %q in %q", item, v)
		}
		out = append(out, FallbackAttempt{Provider: p, Model: m, Reason: reason})
	}
	return out
}

func TestFallbackHeader(t *testing.T) {
	tests := []struct {
		name     string
		attempts []FallbackAttempt
		want     string
	}{
		{name: "none", want: ""},
		{
			name:     "the issue's example",
			attempts: []FallbackAttempt{{Provider: "groq", Model: "llama-3.3-70b-versatile", Reason: ReasonRateLimitedDay}},
			want:     "groq/llama-3.3-70b-versatile=rate_limited_day",
		},
		{
			name: "several, with slashes, colons and at-signs in model IDs",
			attempts: []FallbackAttempt{
				{Provider: "groq", Model: "openai/gpt-oss-20b", Reason: ReasonRateLimitedMinute},
				{Provider: "openrouter", Model: "google/gemma-4-26b-a4b-it:free", Reason: ReasonRateLimited},
				{Provider: "workers-ai", Model: "@cf/meta/llama-3.1-8b-instruct", Reason: ReasonContextLengthExceeded},
				{Provider: "ollama", Model: "llama3.2:3b", Reason: ReasonProviderError},
				{Provider: "gemini", Model: "gemini-3.5-flash-lite", Reason: ReasonInvalidRequest},
			},
			want: "groq/openai/gpt-oss-20b=rate_limited_minute, openrouter/google/gemma-4-26b-a4b-it:free=rate_limited, " +
				"workers-ai/@cf/meta/llama-3.1-8b-instruct=context_length_exceeded, ollama/llama3.2:3b=provider_error, " +
				"gemini/gemini-3.5-flash-lite=invalid_request",
		},
		{
			name: "anything that could break the list is percent-encoded",
			attempts: []FallbackAttempt{
				{Provider: "my,cloud", Model: "a=b, c%d \"é\"\r\nX-Evil: 1", Reason: ReasonProviderError},
				{Provider: "p", Model: "m", Reason: ReasonRateLimited},
			},
			want: "my%2Ccloud/a%3Db%2C%20c%25d%20%22%C3%A9%22%0D%0AX-Evil:%201=provider_error, p/m=rate_limited",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := FallbackHeader(tt.attempts)
			if got != tt.want {
				t.Fatalf("FallbackHeader = %q, want %q", got, tt.want)
			}
			for i := 0; i < len(got); i++ {
				if got[i] < 0x20 || got[i] > 0x7E {
					t.Errorf("byte %#x at %d is not visible ASCII: not a safe header value", got[i], i)
				}
			}
			if got == "" {
				return
			}
			back := parseFallback(t, got)
			if !slices.Equal(back, tt.attempts) {
				t.Errorf("parsed back = %+v, want %+v", back, tt.attempts)
			}
		})
	}
}

// The tracker's words for a limit are used as the error format's, unchanged.
func TestLimitWordsMatchTheErrorFormat(t *testing.T) {
	if apierror.LimitType(ratelimit.Requests) != apierror.LimitRequests || apierror.LimitType(ratelimit.Tokens) != apierror.LimitTokens ||
		apierror.Window(ratelimit.Minute) != apierror.WindowMinute || apierror.Window(ratelimit.Day) != apierror.WindowDay {
		t.Error("ratelimit and apierror no longer use the same words for limit kinds and windows")
	}
}

// ---- which entry Auto would use next (#17) ----

func TestNextInRoute(t *testing.T) {
	cfg, err := config.Parse([]byte(fallbackYAML), func(string) string { return "http://127.0.0.1:1" }, nil)
	if err != nil {
		t.Fatal(err)
	}
	tracker, err := ratelimit.Open(context.Background(), nil, cfg)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	all := func(string) bool { return true }
	limit := func(provider, model string, wait time.Duration) {
		tracker.ObserveError(&upstream.Error{Kind: upstream.KindRateLimited, Provider: provider, Model: model, RetryAfter: wait}, now)
	}
	next := func(enabled EnabledFunc, route string, at time.Time) string {
		p, m, ok := NextInRoute(cfg, enabled, tracker, route, at)
		if !ok {
			return ""
		}
		return p + "/" + m
	}

	if got := next(all, "fast", now); got != "groq/openai/gpt-oss-20b" {
		t.Errorf("fresh tracker: next = %q", got)
	}
	if got := next(func(p string) bool { return p != "groq" }, "fast", now); got != "gemini/flash" {
		t.Errorf("groq disabled: next = %q", got)
	}
	limit("groq", groqModel, time.Minute)
	limit("gemini", "flash", time.Hour)
	if got := next(all, "fast", now); got != "openrouter/google/gemma:free" {
		t.Errorf("two exhausted: next = %q", got)
	}
	if got := next(all, "fast", now.Add(2*time.Minute)); got != "groq/openai/gpt-oss-20b" {
		t.Errorf("after groq's reset: next = %q", got)
	}
	limit("ollama", ollamaModel, time.Minute)
	if got := next(all, "duo", now); got != "" {
		t.Errorf("every entry exhausted: next = %q, want none", got)
	}
	for _, notARoute := range []string{"nope", "groq/openai/gpt-oss-20b"} {
		if got := next(all, notARoute, now); got != "" {
			t.Errorf("NextInRoute(%q) = %q, want none", notARoute, got)
		}
	}
	// Without a tracker nothing is ever exhausted.
	if p, _, ok := NextInRoute(cfg, all, nil, "fast", now); !ok || p != "groq" {
		t.Errorf("nil tracker: next = %q %v", p, ok)
	}
}

// ---- logging ----

// lockedBuffer lets the handler's goroutines and the test share a log buffer.
type lockedBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *lockedBuffer) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *lockedBuffer) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

// A provider's error message is free text and can quote the prompt. It goes
// back to the caller, whose content it is, but never into the process log.
func TestProviderMessagesAreNeverLogged(t *testing.T) {
	const marker = "MARKER-QUOTED-FROM-THE-PROMPT"
	logs := &lockedBuffer{}
	old := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(old) })

	midStream := func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		sse(w, chunk1)
		sse(w, `{"error":{"code":500,"message":"`+marker+`"}}`)
	}
	tests := []struct {
		name   string
		model  string
		stream bool
		groq   http.HandlerFunc
		ollama http.HandlerFunc
	}{
		{name: "pinned, invalid request", model: "groq/" + groqModel, groq: fails(400, "Invalid value: "+marker)},
		{name: "pinned, rate limited", model: "groq/" + groqModel, groq: fails(429, marker, "Retry-After", "1")},
		{name: "pinned, provider error", model: "groq/" + groqModel, groq: fails(500, marker)},
		{name: "pinned, too long", model: "groq/" + groqModel, groq: fails(400, "context length exceeded by "+marker)},
		{name: "pinned, mid-stream", model: "groq/" + groqModel, stream: true, groq: midStream},
		{name: "route, fell back", model: "duo", groq: fails(500, marker), ollama: answers},
		{name: "route, all failed", model: "duo", groq: fails(429, marker), ollama: fails(400, marker)},
		{name: "route, error as first stream event", model: "duo", stream: true,
			groq: streams(`{"error":{"code":400,"message":"` + marker + `"}}`), ollama: fails(503, marker)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newFixtureWith(t, fixtureOpts{yaml: fallbackYAML, each: map[string]http.HandlerFunc{"groq": tt.groq, "ollama": tt.ollama}})
			res := f.do(t, tt.model, tt.stream)
			if tt.ollama == nil || res.status != 200 {
				// The caller does get the message: this proves the marker
				// really travelled through the router.
				if !strings.Contains(res.body, marker) && !strings.Contains(res.body, "hit its") {
					t.Errorf("the client's error does not carry the provider's message: %s", res.body)
				}
			}
		})
	}

	out := logs.String()
	if !strings.Contains(out, "chat: provider failed") || !strings.Contains(out, "chat: route entry failed") ||
		!strings.Contains(out, "chat: provider failed mid-stream") {
		t.Fatalf("the failures were not logged at all, so this test proves nothing:\n%s", out)
	}
	for _, secret := range []string{marker, secretPrompt, secretCompletion} {
		if strings.Contains(out, secret) {
			t.Errorf("log output contains %q:\n%s", secret, out)
		}
	}
	// What is logged instead: who, what kind, which status.
	for _, want := range []string{"provider=groq", "model=openai/gpt-oss-20b", "kind=rate_limited", "kind=bad_request", "status=500", "in_stream=true", "retry_after=1s"} {
		if !strings.Contains(out, want) {
			t.Errorf("log output lacks %q", want)
		}
	}
}
