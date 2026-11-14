package poller

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/RyanCaudill04/ai-gateway/router/internal/providerkeys"
	"github.com/RyanCaudill04/ai-gateway/router/internal/ratelimit"
	"github.com/RyanCaudill04/ai-gateway/router/internal/upstream"
)

// probeBody is the whole request: one word in, at most one token out.
var probeBody = []byte(`{"messages":[{"role":"user","content":"hi"}],"max_tokens":1}`)

// markOwn remembers the time stamped on our own observation of a model.
func (p *Poller) markOwn(provider, model string, now time.Time) {
	p.mu.Lock()
	p.own[modelID{provider, model}] = now
	p.mu.Unlock()
}

// probe sends one tiny completion and hands the outcome to the tracker. A 429
// is information, not a fault: ObserveError records the model as exhausted.
func (p *Poller) probe(ctx context.Context, c target, now time.Time) {
	key, _ := p.deps.Key(c.name)

	p.mu.Lock()
	p.stats.Probes++
	p.own[modelID{c.name, c.model}] = now
	p.mu.Unlock()

	resp, err := p.deps.Completer.Complete(ctx, upstream.Request{
		Provider: c.name, BaseURL: c.p.baseURL, Model: c.model, APIKey: key, Body: probeBody,
	})
	if err != nil {
		if ctx.Err() != nil && !errors.Is(context.Cause(ctx), context.DeadlineExceeded) {
			return // shutting down; not the provider's fault
		}
		p.deps.Tracker.ObserveError(err, now)
		var ue *upstream.Error
		if errors.As(err, &ue) {
			p.log.Info("usage poller: probe failed", "provider", c.name, "model", c.model, "kind", string(ue.Kind), "status", ue.Status)
		} else {
			p.log.Info("usage poller: probe failed", "provider", c.name, "model", c.model, "err", providerkeys.Redact(err.Error(), key))
		}
		return
	}
	p.deps.Tracker.Observe(c.name, c.model, resp.Header, now)
	if u, _ := p.deps.Tracker.Model(c.name, c.model); len(u.Limits) == 0 {
		// A working probe that brought back no windows: this provider doesn't
		// send them, and further probes would spend quota for nothing.
		p.mu.Lock()
		p.noHeaders[c.name] = true
		p.mu.Unlock()
		p.log.Info("usage poller: provider sends no rate-limit headers; it will not be probed again", "provider", c.name)
	}
	p.log.Info("usage poller: probed", "provider", c.name, "model", c.model, "status", resp.Status)
}

// keyResponse is the part of OpenRouter's GET /api/v1/key we use. Per its
// docs, free_model_daily_requests counts the account's `:free` model requests
// in the current UTC day (50, or 1000 with credits bought) and resets at UTC
// midnight. The per-minute limit isn't reported there.
type keyResponse struct {
	Data struct {
		Free *struct {
			Limit     *int64 `json:"limit"`
			Remaining *int64 `json:"remaining"`
		} `json:"free_model_daily_requests"`
	} `json:"data"`
}

// checkKey asks OpenRouter's key endpoint (free: it uses no quota) for the
// daily free-request count and records it for every `:free` model, since the
// allowance is shared by all of them.
func (p *Poller) checkKey(ctx context.Context, models []target, now time.Time) {
	first := models[0]
	key, _ := p.deps.Key(first.name)

	p.mu.Lock()
	p.stats.KeyChecks++
	p.lastKey[first.name] = now
	p.mu.Unlock()

	status, body, err := p.get(ctx, strings.TrimRight(first.p.baseURL, "/")+"/key", key)
	if err != nil {
		if ctx.Err() == nil {
			p.log.Warn("usage poller: key endpoint unreachable", "provider", first.name, "err", providerkeys.Redact(err.Error(), key))
		}
		return
	}
	var kr keyResponse
	if status != http.StatusOK || json.Unmarshal(body, &kr) != nil || kr.Data.Free == nil || kr.Data.Free.Remaining == nil {
		p.log.Warn("usage poller: key endpoint gave no usable numbers", "provider", first.name, "status", status)
		return
	}
	l := ratelimit.Limit{
		Kind: ratelimit.Requests, Window: ratelimit.Day,
		Remaining: max(*kr.Data.Free.Remaining, 0), Max: ratelimit.Unknown,
		ResetsAt: nextUTCMidnight(now),
	}
	if kr.Data.Free.Limit != nil {
		l.Max = *kr.Data.Free.Limit
	}
	for _, m := range models {
		p.markOwn(m.name, m.model, now)
		p.deps.Tracker.ObserveWindow(m.name, m.model, l, now)
	}
	p.log.Info("usage poller: key endpoint checked", "provider", first.name, "models", len(models))
}

// checkHealth records whether a local provider answers, using its (cheap)
// model list. Never a generation.
func (p *Poller) checkHealth(ctx context.Context, c target, now time.Time) {
	status, _, err := p.get(ctx, strings.TrimRight(c.p.baseURL, "/")+"/models", "")
	online := err == nil && status >= 200 && status < 300

	p.mu.Lock()
	p.stats.HealthChecks++
	prev, had := p.health[c.name]
	p.health[c.name] = Health{Online: online, CheckedAt: now}
	p.mu.Unlock()

	if ctx.Err() != nil && !online {
		return
	}
	if !had || prev.Online != online {
		p.log.Info("usage poller: local provider health", "provider", c.name, "online", online)
	}
}

// get sends an authenticated GET and returns the status and the start of the
// body.
func (p *Poller) get(ctx context.Context, url, key string) (int, []byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return 0, nil, err
	}
	if key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}
	resp, err := p.opts.HTTP.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	return resp.StatusCode, body, err
}

func nextUTCMidnight(now time.Time) time.Time {
	y, m, d := now.UTC().Date()
	return time.Date(y, m, d+1, 0, 0, 0, 0, time.UTC)
}
