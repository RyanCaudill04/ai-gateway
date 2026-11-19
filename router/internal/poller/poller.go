// Package poller keeps the rate-limit numbers fresh when no real request has
// gone through a provider for a while, without wasting the free quota it is
// measuring.
//
// Rate-limit numbers normally arrive on the headers of real responses, so they
// go stale when a provider sits idle. Every tick, for each model that is worth
// watching, the poller picks the cheapest way to learn the current numbers:
//
//   - OpenRouter: its key endpoint reports the free-model daily request count
//     and costs no quota. Nothing else is ever sent to OpenRouter.
//   - Local providers (Ollama): a health check (is it reachable?). Never a
//     generation.
//   - Everyone else: a tiny completion (one word in, max_tokens 1), and the
//     response headers go to the tracker like any real response. A probe spends
//     one request of the quota it measures, so it is the last resort and the
//     skip rules in decide.go keep it rare.
//
// Providers that send no rate-limit headers (Gemini, Workers AI) are never
// probed: a probe there would spend quota and learn nothing except "not 429".
// Everything learned goes into the same ratelimit.Tracker as real traffic.
//
// Probes are counted per UTC day and the count is logged, so the cost stays
// visible. Provider keys are never logged.
package poller

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/RyanCaudill04/ai-gateway/router/internal/config"
	"github.com/RyanCaudill04/ai-gateway/router/internal/ratelimit"
	"github.com/RyanCaudill04/ai-gateway/router/internal/upstream"
)

// Defaults for Options fields left at zero.
const (
	// DefaultInterval is how old a model's numbers may get before the poller
	// refreshes them. Probes are the only thing that costs quota, and the
	// worst case is one probe per model per interval: 24 a day at one hour,
	// against free allowances of about 1,000 requests a day (see docs). The
	// plan's five minutes would be 288 a day per model, over a quarter of
	// that allowance spent on measuring. An hour is still fresh enough to
	// notice that another client used up a day window, and a real 429 is
	// always recorded straight away by the request path.
	DefaultInterval = time.Hour
	// MinInterval stops a typo from turning the poller into a load generator.
	MinInterval = time.Minute
	// DefaultMaxProbesPerTick spreads work out: after a restart every model
	// looks stale, and this keeps them from all being probed in one burst.
	// Models not probed this tick are the stalest-first candidates next tick.
	DefaultMaxProbesPerTick = 3
	// DefaultProbeTimeout bounds one probe, key check or health check, so a
	// hanging provider can neither stall the loop nor hold up the others.
	DefaultProbeTimeout = 20 * time.Second
	// DefaultRecentUse is how long after its last real request a model that is
	// in no route stays worth watching.
	DefaultRecentUse = 24 * time.Hour
	// maxTick is how often the loop wakes at most. Waking is nearly free (the
	// skip rules decide whether anything is sent), and it keeps the local
	// health check and the stagger above reasonably responsive even with a
	// long interval.
	maxTick = 5 * time.Minute
)

// Completer sends a completion. *upstream.Client is one.
type Completer interface {
	Complete(ctx context.Context, req upstream.Request) (*upstream.Response, error)
}

// Deps is what the poller reads and writes.
type Deps struct {
	Config    *config.Config
	Tracker   *ratelimit.Tracker
	Completer Completer
	// Key returns a provider's decrypted API key (providerkeys.Keys.Get).
	Key func(provider string) (string, bool)
	// Enabled says whether a provider can be used right now (a key is
	// registered, or it is local). A disabled provider is never contacted.
	Enabled func(provider string) bool
}

// Options tunes the poller. Zero values mean the defaults. Now, NewTicker,
// HTTP and Logger exist so tests don't sleep or touch the network.
type Options struct {
	// Interval is the staleness limit; zero or negative turns the poller off
	// (Start does nothing). Use IntervalFromEnv to read it.
	Interval         time.Duration
	Tick             time.Duration // how often to look; default min(Interval, 5m)
	MaxProbesPerTick int
	ProbeTimeout     time.Duration
	RecentUse        time.Duration

	HTTP      *http.Client // key endpoint and health checks
	Logger    *slog.Logger
	Now       func() time.Time
	NewTicker func(d time.Duration) (ticks <-chan time.Time, stop func())
}

// Health is the last health check of a local provider.
type Health struct {
	Online    bool
	CheckedAt time.Time
}

// Stats counts what the poller has sent in the current UTC day.
type Stats struct {
	Day          string // "2006-01-02", UTC
	Probes       int    // completions sent: these spend quota
	KeyChecks    int    // OpenRouter key endpoint calls: free
	HealthChecks int    // local provider reachability checks: free
}

type modelID struct{ provider, model string }

// Poller is safe for concurrent use. Create it with New, then Start and Stop.
type Poller struct {
	deps Deps
	opts Options
	log  *slog.Logger

	mu        sync.Mutex
	own       map[modelID]time.Time // the stamp of our own last observation, to tell it from real traffic
	real      map[modelID]time.Time // last observation that wasn't ours
	noHeaders map[string]bool       // providers a probe showed to send no rate-limit headers
	lastKey   map[string]time.Time  // last key endpoint call per provider
	health    map[string]Health
	stats     Stats

	cancel context.CancelFunc
	done   chan struct{}
}

// New returns a poller that hasn't started.
func New(deps Deps, opts Options) *Poller {
	if opts.Tick <= 0 {
		opts.Tick = min(opts.Interval, maxTick)
	}
	if opts.MaxProbesPerTick <= 0 {
		opts.MaxProbesPerTick = DefaultMaxProbesPerTick
	}
	if opts.ProbeTimeout <= 0 {
		opts.ProbeTimeout = DefaultProbeTimeout
	}
	if opts.RecentUse <= 0 {
		opts.RecentUse = DefaultRecentUse
	}
	if opts.HTTP == nil {
		opts.HTTP = &http.Client{}
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	if opts.NewTicker == nil {
		opts.NewTicker = func(d time.Duration) (<-chan time.Time, func()) {
			t := time.NewTicker(d)
			return t.C, t.Stop
		}
	}
	log := opts.Logger
	if log == nil {
		log = slog.Default()
	}
	return &Poller{
		deps: deps, opts: opts, log: log,
		own:       map[modelID]time.Time{},
		real:      map[modelID]time.Time{},
		noHeaders: map[string]bool{},
		lastKey:   map[string]time.Time{},
		health:    map[string]Health{},
	}
}

// IntervalFromEnv reads USAGE_POLL_INTERVAL: a Go duration such as "30m" or
// "2h", or "off" (also "0") to turn the poller off. Unset means
// DefaultInterval. Anything else is an error so a typo can't silently change
// how much quota is spent.
func IntervalFromEnv(getenv func(string) string) (time.Duration, error) {
	v := strings.TrimSpace(getenv("USAGE_POLL_INTERVAL"))
	switch strings.ToLower(v) {
	case "":
		return DefaultInterval, nil
	case "off", "0":
		return 0, nil
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		return 0, fmt.Errorf("USAGE_POLL_INTERVAL %q: want a duration like 30m or 2h, or \"off\"", v)
	}
	if d < MinInterval {
		return 0, fmt.Errorf("USAGE_POLL_INTERVAL %q: at least %v (or \"off\")", v, MinInterval)
	}
	return d, nil
}

// Start begins polling in the background. It returns at once and does nothing
// when the interval is off. The first look happens one Tick after Start, so
// nothing is sent while the router is still starting up. Call it once.
func (p *Poller) Start(ctx context.Context) {
	if p.opts.Interval <= 0 {
		p.log.Info("usage poller: off")
		return
	}
	ctx, p.cancel = context.WithCancel(ctx)
	p.done = make(chan struct{})
	p.log.Info("usage poller: started", "interval", p.opts.Interval.String(), "max_probes_per_tick", p.opts.MaxProbesPerTick)
	go p.loop(ctx)
}

// Stop cancels the poller (and any probe in flight) and waits for it to
// finish, or for ctx to end. Call it before closing the tracker.
func (p *Poller) Stop(ctx context.Context) error {
	if p.cancel == nil {
		return nil
	}
	p.cancel()
	select {
	case <-p.done:
		return nil
	case <-ctx.Done():
		return errors.New("usage poller did not stop in time")
	}
}

// Health returns the last health check of a local provider. ok is false for
// providers that are not local or haven't been checked yet.
func (p *Poller) Health(provider string) (h Health, ok bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	h, ok = p.health[provider]
	return
}

// LocalHealth returns the last health check of every local provider checked
// so far, by provider name. The result is a copy.
func (p *Poller) LocalHealth() map[string]Health {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make(map[string]Health, len(p.health))
	for k, v := range p.health {
		out[k] = v
	}
	return out
}

// Stats returns today's (UTC) counters.
func (p *Poller) Stats() Stats {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.stats
}

func (p *Poller) loop(ctx context.Context) {
	defer close(p.done)
	ticks, stop := p.opts.NewTicker(p.opts.Tick)
	defer stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticks:
			p.safeTick(ctx)
		}
	}
}

// safeTick keeps a bug in one tick from killing the loop (or the router).
func (p *Poller) safeTick(ctx context.Context) {
	defer func() {
		if r := recover(); r != nil {
			p.log.Error("usage poller: tick panicked", "panic", fmt.Sprint(r))
		}
	}()
	p.tick(ctx)
}

// tick looks at every candidate model once, sends what the decision table
// says, and waits for it all (each piece is bounded by ProbeTimeout).
func (p *Poller) tick(ctx context.Context) {
	now := p.opts.Now()
	p.rollDay(now)

	var jobs []func(context.Context)
	probes := 0
	keyModels := map[string][]target{} // OpenRouter-style providers → their :free models
	keyStale := map[string]bool{}
	healthDone := map[string]bool{}

	for _, c := range p.candidates(now) {
		d := p.decide(c, now)
		p.log.Debug("usage poller: decided", "provider", c.name, "model", c.model, "action", d.action.String(), "reason", d.reason)
		switch d.action {
		case actHealth:
			if !healthDone[c.name] {
				healthDone[c.name] = true
				jobs = append(jobs, func(ctx context.Context) { p.checkHealth(ctx, c, now) })
			}
		case actKey:
			keyModels[c.name] = append(keyModels[c.name], c)
			keyStale[c.name] = keyStale[c.name] || d.stale
		case actProbe:
			if probes >= p.opts.MaxProbesPerTick {
				p.log.Debug("usage poller: tick probe cap reached", "provider", c.name, "model", c.model)
				continue
			}
			probes++
			jobs = append(jobs, func(ctx context.Context) { p.probe(ctx, c, now) })
		}
	}
	for name, models := range keyModels {
		if keyStale[name] && p.keyDue(name, now) {
			jobs = append(jobs, func(ctx context.Context) { p.checkKey(ctx, models, now) })
		}
	}
	if len(jobs) == 0 {
		return
	}

	// Run in parallel: one provider hanging until its timeout must not delay
	// the others. The cap above keeps the number of goroutines small.
	var wg sync.WaitGroup
	for _, job := range jobs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer func() {
				if r := recover(); r != nil {
					p.log.Error("usage poller: job panicked", "panic", fmt.Sprint(r))
				}
			}()
			jctx, cancel := context.WithTimeout(ctx, p.opts.ProbeTimeout)
			defer cancel()
			job(jctx)
		}()
	}
	wg.Wait()

	if probes > 0 {
		st := p.Stats()
		p.log.Info("usage poller: probes today", "day", st.Day, "probes", st.Probes)
	}
}

// rollDay starts a new count at UTC midnight, logging the finished day.
func (p *Poller) rollDay(now time.Time) {
	day := now.UTC().Format(time.DateOnly)
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.stats.Day == day {
		return
	}
	if p.stats.Day != "" {
		p.log.Info("usage poller: day finished", "day", p.stats.Day, "probes", p.stats.Probes,
			"key_checks", p.stats.KeyChecks, "health_checks", p.stats.HealthChecks)
	}
	p.stats = Stats{Day: day}
}

func (p *Poller) keyDue(provider string, now time.Time) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	last, ok := p.lastKey[provider]
	return !ok || now.Sub(last) >= p.opts.Interval
}
