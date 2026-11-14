package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/RyanCaudill04/ai-gateway/router/internal/auth"
	"github.com/RyanCaudill04/ai-gateway/router/internal/config"
	"github.com/RyanCaudill04/ai-gateway/router/internal/ratelimit"
)

// The chat endpoint and the rate-limit tracker are joined in newHandler: what
// a provider answers reaches the tracker, and what the tracker knows decides
// whether a route calls that provider again.
func TestChatEndpointUsesTheRateLimitTracker(t *testing.T) {
	var calls atomic.Int32
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Header().Set("Retry-After", "3600")
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer up.Close()

	cfg, err := config.Parse([]byte(`
providers:
  box:
    base_url: `+up.URL+`/v1
    local: true
    models:
      - id: tiny
        context_length: 4096
routes:
  auto: [box/tiny]
`), func(string) string { return "" }, nil)
	if err != nil {
		t.Fatal(err)
	}
	limits, err := ratelimit.Open(context.Background(), nil, cfg) // nil: memory only
	if err != nil {
		t.Fatal(err)
	}
	h := newHandler(handlerDeps{authn: auth.Disabled(), cfg: cfg, enabled: allEnabled, limits: limits})
	chat := func(model string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(`{"model":"`+model+`","messages":[]}`))
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}

	// A pinned request is sent, and the provider's 429 is recorded.
	w := chat("box/tiny")
	if w.Code != http.StatusTooManyRequests {
		t.Fatalf("pinned = %d: %s", w.Code, w.Body)
	}
	wantEnvelope(t, w, "rate_limited", "rate_limit_exceeded")
	l, exhausted := limits.Exhausted("box", "tiny", time.Now())
	if !exhausted || l.Window != ratelimit.Day {
		t.Fatalf("tracker after the 429: exhausted=%v limit=%+v", exhausted, l)
	}

	// The route now skips the model without spending a request on it.
	w = chat("auto")
	if w.Code != http.StatusTooManyRequests {
		t.Fatalf("route = %d: %s", w.Code, w.Body)
	}
	wantEnvelope(t, w, "all_providers_exhausted", "all_providers_exhausted")
	if got := w.Header().Get("X-Router-Fallback"); got != "box/tiny=rate_limited_day" {
		t.Errorf("x-router-fallback = %q", got)
	}
	if n := calls.Load(); n != 1 {
		t.Errorf("the provider was called %d times, want once", n)
	}
}
