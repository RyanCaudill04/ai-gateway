package api

import (
	"encoding/json"
	"net/http"
	"sort"
	"time"

	"github.com/RyanCaudill04/ai-gateway/router/internal/apierror"
	"github.com/RyanCaudill04/ai-gateway/router/internal/apikeys"
	"github.com/RyanCaudill04/ai-gateway/router/internal/config"
	"github.com/RyanCaudill04/ai-gateway/router/internal/poller"
	"github.com/RyanCaudill04/ai-gateway/router/internal/ratelimit"
)

// UsageTracker is what GET /v1/usage reads from the rate-limit tracker: the
// windows of one model, plus the same "is it exhausted" question the chat
// handler asks. *ratelimit.Tracker satisfies it.
type UsageTracker interface {
	LimitTracker
	// Model returns what is known about one provider + model; ok is false if
	// nothing has ever been observed for it.
	Model(provider, model string) (ratelimit.Usage, bool)
}

// HealthSource is the last health check of a local provider. *poller.Poller
// satisfies it. ok is false until the first check has run.
type HealthSource interface {
	Health(provider string) (poller.Health, bool)
}

// KeyUsageSource is the calling key's daily cap and count.
// *apikeys.Store satisfies it.
type KeyUsageSource interface {
	Usage(name string) (apikeys.Usage, bool)
}

// UsageDeps is everything the usage endpoint reads. It only reads: nothing
// here is changed by a request, and nothing is stored. A nil Limits, Health
// or Keys means "nothing known": every model then reports as unknown, local
// providers as not yet checked, and the key as absent.
type UsageDeps struct {
	Config  *config.Config
	Enabled EnabledFunc
	Limits  UsageTracker
	Health  HealthSource
	Keys    KeyUsageSource
	// Now is the clock; nil means time.Now.
	Now func() time.Time
}

// The status values of a provider or model. See docs/api.md.
const (
	statusKnown     = "known"     // the provider reported at least one window
	statusUnknown   = "unknown"   // it has reported nothing (Gemini, Workers AI, not used yet)
	statusUnlimited = "unlimited" // local: no quota, only reachable or not
	statusDisabled  = "disabled"  // no key registered; the router will not call it
)

// Route statuses.
const (
	routeAvailable   = "available"   // some entry would be tried now
	routeExhausted   = "exhausted"   // every enabled entry is out of quota
	routeUnavailable = "unavailable" // no entry is enabled
)

type usageResponse struct {
	Object    string          `json:"object"` // always "usage"
	Now       string          `json:"now"`
	Key       *keyUsage       `json:"key"`
	Routes    []routeUsage    `json:"routes"`
	Providers []providerUsage `json:"providers"`
}

type keyUsage struct {
	Name      string `json:"name"`
	DailyCap  *int64 `json:"daily_cap"` // null: no cap
	UsedToday int64  `json:"used_today"`
	Remaining *int64 `json:"remaining"` // null: no cap
	ResetsAt  string `json:"resets_at"`
}

type routeUsage struct {
	ID     string     `json:"id"`
	Status string     `json:"status"`
	Next   *routeNext `json:"next"` // null unless status is available
	// ResetsAt is when the earliest exhausted entry is usable again. Only
	// when status is exhausted.
	ResetsAt *string `json:"resets_at"`
}

type routeNext struct {
	ID       string `json:"id"` // provider/model, as in /v1/models
	Provider string `json:"provider"`
	Model    string `json:"model"`
}

type providerUsage struct {
	Provider string       `json:"provider"`
	Local    bool         `json:"local"`
	Enabled  bool         `json:"enabled"`
	Status   string       `json:"status"`
	Health   *healthUsage `json:"health,omitempty"` // local providers only
	Models   []modelUsage `json:"models"`
}

type healthUsage struct {
	Online    *bool   `json:"online"`     // null: not checked yet
	CheckedAt *string `json:"checked_at"` // null: not checked yet
}

type modelUsage struct {
	ID            string        `json:"id"`
	Model         string        `json:"model"`
	ContextLength int           `json:"context_length"`
	Status        string        `json:"status"`
	Exhausted     bool          `json:"exhausted"`
	LimitType     string        `json:"limit_type,omitempty"` // only when exhausted
	Window        string        `json:"window,omitempty"`     // only when exhausted
	ResetsAt      *string       `json:"resets_at"`            // only when exhausted
	ObservedAt    *string       `json:"observed_at"`
	Windows       []windowUsage `json:"windows"`
}

type windowUsage struct {
	Kind      string  `json:"kind"`
	Window    string  `json:"window"`
	Remaining *int64  `json:"remaining"` // null: not reported
	Limit     *int64  `json:"limit"`     // null: not reported
	ResetsAt  *string `json:"resets_at"` // null: not reported, or already reset
	CheckedAt *string `json:"checked_at"`
	Exhausted bool    `json:"exhausted"`
	Reset     bool    `json:"reset"`
}

// UsageHandler serves GET /v1/usage: a read-only view of what the tracker, the
// poller's health checks and the key store already know, shaped for the UI's
// usage bars. It spends no provider quota and writes nothing.
//
// It is registered behind the service-key check like every /v1 route, and
// apikeys.Uncounted exempts it from the daily-cap count (see there).
func UsageHandler(d UsageDeps) http.Handler {
	if d.Now == nil {
		d.Now = time.Now
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			w.Header().Set("Allow", http.MethodGet)
			apierror.Write(w, apierror.MethodNotAllowed())
			return
		}
		now := d.Now()
		resp := usageResponse{
			Object:    "usage",
			Now:       utc(now),
			Key:       keyOf(d, r, now),
			Routes:    routesOf(d, now),
			Providers: providersOf(d, now),
		}
		w.Header().Set("Content-Type", "application/json")
		// Polled by the UI: a cached answer would show old numbers as new.
		w.Header().Set("Cache-Control", "no-store")
		json.NewEncoder(w).Encode(resp)
	})
}

// utc formats a time like apierror's resets_at: UTC RFC 3339, whole seconds.
func utc(t time.Time) string { return t.UTC().Truncate(time.Second).Format(time.RFC3339) }

// utcOrNil is utc, with null for a time that was never set.
func utcOrNil(t time.Time) *string {
	if t.IsZero() {
		return nil
	}
	s := utc(t)
	return &s
}

// count turns a tracker count into JSON: ratelimit.Unknown (-1) must not
// leak out as a number a bar would draw.
func count(n int64) *int64 {
	if n < 0 {
		return nil
	}
	return &n
}

func keyOf(d UsageDeps, r *http.Request, now time.Time) *keyUsage {
	name, ok := apikeys.NameFrom(r.Context())
	if !ok || d.Keys == nil {
		return nil
	}
	u, ok := d.Keys.Usage(name)
	if !ok {
		return nil
	}
	k := &keyUsage{Name: name, UsedToday: u.Used, ResetsAt: utc(u.ResetsAt)}
	if u.DailyCap > 0 {
		left := max(u.DailyCap-u.Used, 0)
		k.DailyCap, k.Remaining = &u.DailyCap, &left
	}
	return k
}

// providersOf lists every configured provider, enabled or not, by name, with
// its models in the order of the config file.
//
// A disabled provider (no key registered) is listed, with status "disabled"
// and no windows, where /v1/models leaves it out. /v1/models is what a client
// may send, so it hides what would fail. This is a status view: the plan's
// picker shows unavailable providers greyed out, and a provider missing from
// the list can't say why ("no key yet" versus "typo in the config").
func providersOf(d UsageDeps, now time.Time) []providerUsage {
	names := make([]string, 0, len(d.Config.Providers))
	for name := range d.Config.Providers {
		names = append(names, name)
	}
	sort.Strings(names)

	out := make([]providerUsage, 0, len(names))
	for _, name := range names {
		p := d.Config.Providers[name]
		pu := providerUsage{Provider: name, Local: p.Local, Enabled: d.Enabled(name), Models: []modelUsage{}}
		for _, m := range p.Models {
			pu.Models = append(pu.Models, modelOf(d, p, m, pu.Enabled, now))
		}
		pu.Status = providerStatus(pu)
		if p.Local {
			pu.Health = healthOf(d, name)
		}
		out = append(out, pu)
	}
	return out
}

func providerStatus(p providerUsage) string {
	switch {
	case !p.Enabled:
		return statusDisabled
	case p.Local:
		return statusUnlimited
	}
	for _, m := range p.Models {
		if m.Status == statusKnown {
			return statusKnown
		}
	}
	return statusUnknown
}

// healthOf is a local provider's last health check. Before the first one
// (the poller waits one tick after start, and is off with
// USAGE_POLL_INTERVAL=off) both fields are null rather than a guess, so the UI
// can say "checking" instead of drawing a false "offline".
func healthOf(d UsageDeps, provider string) *healthUsage {
	h := &healthUsage{}
	if d.Health == nil {
		return h
	}
	if got, ok := d.Health.Health(provider); ok {
		online := got.Online
		h.Online, h.CheckedAt = &online, utcOrNil(got.CheckedAt)
	}
	return h
}

func modelOf(d UsageDeps, p *config.Provider, m config.Model, enabled bool, now time.Time) modelUsage {
	mu := modelUsage{
		ID: p.Name + "/" + m.ID, Model: m.ID, ContextLength: m.ContextLength,
		Status: statusUnknown, Windows: []windowUsage{},
	}
	if !enabled {
		// Whatever the tracker holds from before the key was removed is not
		// something the router would act on, so it is not reported either.
		mu.Status = statusDisabled
		return mu
	}
	if p.Local {
		mu.Status = statusUnlimited
	}
	if d.Limits == nil {
		return mu
	}
	if u, ok := d.Limits.Model(p.Name, m.ID); ok {
		mu.ObservedAt = utcOrNil(u.LastObserved)
		for _, l := range u.Limits {
			mu.Windows = append(mu.Windows, windowOf(l, now))
		}
		if u.Known() && !p.Local {
			mu.Status = statusKnown
		}
	}
	// The router's own check, so a greyed-out model is one the router skips.
	if l, out := d.Limits.Exhausted(p.Name, m.ID, now); out {
		mu.Exhausted = true
		mu.LimitType, mu.Window, mu.ResetsAt = string(l.Kind), string(l.Window), utcOrNil(l.ResetsAt)
	}
	return mu
}

// windowOf reports one window. A window whose reset time has passed is known
// to be full again, so its stored remaining count is stale: it is reported as
// full (remaining = limit, or null when the size was never reported) with
// reset true, and resets_at is dropped because the next reset is not known
// until the next reading. checked_at stays the time of the real reading.
func windowOf(l ratelimit.Limit, now time.Time) windowUsage {
	w := windowUsage{
		Kind: string(l.Kind), Window: string(l.Window),
		Remaining: count(l.Remaining), Limit: count(l.Max),
		ResetsAt: utcOrNil(l.ResetsAt), CheckedAt: utcOrNil(l.CheckedAt),
		Exhausted: l.Exhausted(now),
	}
	if l.HasReset(now) {
		w.Reset, w.ResetsAt = true, nil
		w.Remaining = w.Limit
	}
	return w
}

// routesOf lists every route by name, with the entry Auto would use now.
func routesOf(d UsageDeps, now time.Time) []routeUsage {
	names := make([]string, 0, len(d.Config.Routes))
	for name := range d.Config.Routes {
		names = append(names, name)
	}
	sort.Strings(names)

	out := make([]routeUsage, 0, len(names))
	for _, name := range names {
		out = append(out, routeOf(d, name, now))
	}
	return out
}

// routeOf uses NextInRoute, the same check the chat handler makes, so the
// answer is what a request sent now would try first.
//
// When nothing is usable it says why: exhausted (with the soonest time any
// entry is back) when at least one entry is enabled, unavailable when none
// is. The soonest reset is only an estimate of when the route works again: it
// is the first entry to come back, and a request may still fail for other
// reasons.
func routeOf(d UsageDeps, name string, now time.Time) routeUsage {
	ru := routeUsage{ID: name}
	var limits LimitTracker = noLimits{}
	if d.Limits != nil {
		limits = d.Limits
	}
	if provider, model, ok := NextInRoute(d.Config, d.Enabled, limits, name, now); ok {
		ru.Status = routeAvailable
		ru.Next = &routeNext{ID: provider + "/" + model, Provider: provider, Model: model}
		return ru
	}
	res, _ := resolve(d.Config, d.Enabled, name)
	if len(res.targets) == 0 {
		ru.Status = routeUnavailable
		return ru
	}
	ru.Status = routeExhausted
	var earliest time.Time
	for _, t := range res.targets {
		if l, out := limits.Exhausted(t.provider.Name, t.model.ID, now); out && (earliest.IsZero() || l.ResetsAt.Before(earliest)) {
			earliest = l.ResetsAt
		}
	}
	ru.ResetsAt = utcOrNil(earliest)
	return ru
}
