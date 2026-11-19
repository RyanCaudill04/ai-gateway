package poller

import (
	"cmp"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/RyanCaudill04/ai-gateway/router/internal/ratelimit"
)

// target is one model worth looking at, with what the tracker knows of it.
type target struct {
	name    string // provider name in providers.yaml
	p       providerInfo
	model   string
	inRoute bool
	usage   ratelimit.Usage
	known   bool // the tracker has seen this model at all
}

type providerInfo struct {
	baseURL string
	local   bool
	kind    hostKind
}

type action int

const (
	actSkip   action = iota
	actProbe         // a one-word completion
	actKey           // OpenRouter's free key endpoint
	actHealth        // is the local provider reachable
)

func (a action) String() string {
	return [...]string{"skip", "probe", "key-endpoint", "health-check"}[a]
}

type decision struct {
	action action
	reason string // why, for debug logs and tests
	// stale is only used with actKey: this model's numbers are older than the
	// interval, so the (free) key endpoint is worth calling.
	stale bool
}

// hostKind is what the poller knows about a provider from its base_url host.
// Like the tracker, it goes by the host, not by the name someone gave the
// provider in providers.yaml.
type hostKind int

const (
	hostGeneric    hostKind = iota // probe, and see whether headers come back
	hostOpenRouter                 // free key endpoint
	hostNoHeaders                  // Gemini, Workers AI: never probe
)

func hostKindOf(baseURL string) hostKind {
	u, err := url.Parse(baseURL)
	if err != nil {
		return hostGeneric
	}
	host := strings.ToLower(u.Hostname())
	is := func(domain string) bool { return host == domain || strings.HasSuffix(host, "."+domain) }
	switch {
	case is("openrouter.ai"):
		return hostOpenRouter
	case is("googleapis.com"), is("cloudflare.com"):
		return hostNoHeaders
	}
	return hostGeneric
}

// Thresholds for "nearly exhausted": a requests window with at most this many
// left, or at most one tenth of its size, is left alone. A probe would spend
// one of the last few requests (or earn a 429) to re-measure a number that
// real traffic will report anyway. Token windows aren't checked: a probe
// uses a handful of tokens, and a window with too few left is already
// "exhausted" by Limit.Exhausted.
const (
	nearlyFloor    int64 = 5
	nearlyFraction int64 = 10
)

func nearlyExhausted(l ratelimit.Limit, now time.Time) bool {
	if l.Kind != ratelimit.Requests || l.Remaining == ratelimit.Unknown || l.HasReset(now) {
		return false
	}
	if l.Remaining <= nearlyFloor {
		return true
	}
	return l.Max > 0 && l.Remaining*nearlyFraction <= l.Max
}

// candidates lists the models worth watching, stalest first (so a capped tick
// picks the ones that need it most): every model in a route, plus any other
// configured model a real request used within RecentUse.
//
// The tracker can't tell a probe from a real request, so the poller remembers
// the time it stamped its own observations with. A later, different
// LastObserved is real traffic. Without this a model used once would keep
// itself "recently used" forever by being probed.
func (p *Poller) candidates(now time.Time) []target {
	inRoute := map[modelID]bool{}
	for _, ids := range p.deps.Config.Routes {
		for _, id := range ids {
			if prov, m, ok := p.deps.Config.Lookup(id); ok {
				inRoute[modelID{prov.Name, m.ID}] = true
			}
		}
	}

	p.mu.Lock()
	defer p.mu.Unlock()
	for _, u := range p.deps.Tracker.Snapshot() {
		k := modelID{u.Provider, u.Model}
		if !u.LastObserved.IsZero() && !u.LastObserved.Equal(p.own[k]) {
			p.real[k] = u.LastObserved
		}
	}

	var out []target
	for name, prov := range p.deps.Config.Providers {
		for _, m := range prov.Models {
			k := modelID{name, m.ID}
			if !inRoute[k] && now.Sub(p.real[k]) > p.opts.RecentUse {
				continue
			}
			u, known := p.deps.Tracker.Model(name, m.ID)
			out = append(out, target{
				name: name, model: m.ID, inRoute: inRoute[k], usage: u, known: known,
				p: providerInfo{baseURL: prov.BaseURL, local: prov.Local, kind: hostKindOf(prov.BaseURL)},
			})
		}
	}
	slices.SortFunc(out, func(a, b target) int {
		return cmp.Or(a.usage.LastObserved.Compare(b.usage.LastObserved),
			cmp.Compare(a.name, b.name), cmp.Compare(a.model, b.model))
	})
	return out
}

// decide is the whole decision table, cheapest source first:
//
//	provider disabled (or no key)            skip, never contacted
//	local provider                           health check
//	OpenRouter, model ends in ":free"        key endpoint (free); a paid model is skipped
//	provider known to send no headers        skip (a probe learns nothing)
//	refreshed within Interval                skip (a real request or earlier probe)
//	some window exhausted                    skip (would only earn a 429)
//	some requests window nearly exhausted    skip (would spend the last requests)
//	has windows, all of them past their reset skip (known to be full again)
//	otherwise                                probe
//
// "Has windows" matters: a model nothing is known about is probed, since
// there is nothing else to go on. A window with no reset time never counts as
// reset.
func (p *Poller) decide(c target, now time.Time) decision {
	if !p.deps.Enabled(c.name) {
		return decision{reason: "provider disabled"}
	}
	if !c.p.local {
		if key, ok := p.deps.Key(c.name); !ok || key == "" {
			return decision{reason: "no key"}
		}
	}
	if c.p.local {
		return decision{action: actHealth, reason: "local provider"}
	}
	stale := now.Sub(c.usage.LastObserved) >= p.opts.Interval
	if c.p.kind == hostOpenRouter {
		if !strings.HasSuffix(c.model, ":free") {
			return decision{reason: "paid OpenRouter model: no free usage source, a probe would cost credits"}
		}
		return decision{action: actKey, reason: "free key endpoint", stale: stale}
	}
	p.mu.Lock()
	learned := p.noHeaders[c.name]
	p.mu.Unlock()
	if c.p.kind == hostNoHeaders || learned {
		return decision{reason: "provider sends no rate-limit headers"}
	}
	if !stale {
		return decision{reason: "refreshed within the interval"}
	}
	if len(c.usage.Limits) == 0 {
		return decision{action: actProbe, reason: "nothing known about this model"}
	}
	allReset := true
	for _, l := range c.usage.Limits {
		if l.Exhausted(now) {
			return decision{reason: "exhausted"}
		}
		if nearlyExhausted(l, now) {
			return decision{reason: "nearly exhausted"}
		}
		if !l.HasReset(now) {
			allReset = false
		}
	}
	if allReset {
		return decision{reason: "every window has reset: known to be full again"}
	}
	return decision{action: actProbe, reason: "stale"}
}
