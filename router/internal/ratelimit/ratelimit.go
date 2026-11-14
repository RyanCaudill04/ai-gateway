// Package ratelimit tracks the latest known rate-limit windows of every
// provider + model, so the router can skip one it already knows is out of
// quota instead of waiting for a 429.
//
// Each window (requests or tokens, per minute or per day) is kept separately
// with its remaining count, size, reset time and when it was last checked.
// Routing reads an in-memory copy. Changes are written to SQLite in the
// background, so a slow disk never delays a chat response, and loaded back at
// startup, so a restart doesn't forget that a day window is used up.
//
// Providers that send no rate-limit headers (Gemini, Workers AI) have no
// windows: their usage is "unknown" and they are only skipped after a 429.
package ratelimit

import (
	"cmp"
	"errors"
	"net/http"
	"slices"
	"sync"
	"time"

	"github.com/RyanCaudill04/ai-gateway/router/internal/config"
	"github.com/RyanCaudill04/ai-gateway/router/internal/upstream"
)

// Kind is what a window counts. The values match the rate_limits.kind column
// and the error format's limit_type.
type Kind string

const (
	Requests Kind = "requests"
	Tokens   Kind = "tokens"
)

// Window is how long a limit lasts. The values match rate_limits.window.
type Window string

const (
	Minute Window = "minute"
	Day    Window = "day"
)

// Unknown marks a count the provider didn't report.
const Unknown int64 = -1

// MinTokens is the fewest remaining tokens a token window can have and still
// be tried. Any real chat request (prompt plus reply) needs more than this, so
// below it the request would certainly be refused. It is kept small on
// purpose: wrongly calling a window exhausted skips a working provider until
// the reset, while wrongly calling it available only costs one 429 and a
// fallback.
const MinTokens int64 = 100

// DefaultBackoff is how long a 429 blocks a model when the provider didn't
// say how long to wait (no Retry-After, reset header or retry delay). One
// minute is short enough that a per-minute limit is retried promptly, and a
// day limit only costs one wasted request per minute while it lasts.
const DefaultBackoff = time.Minute

// Limit is one window of one provider + model.
type Limit struct {
	Kind   Kind
	Window Window
	// Remaining is what's left in the window as last reported.
	Remaining int64
	// Max is the window's size (the "limit" column), or Unknown.
	Max int64
	// ResetsAt is when the window is full again, or zero if not reported.
	ResetsAt time.Time
	// CheckedAt is when this window was last reported.
	CheckedAt time.Time
}

// Exhausted reports whether the window is known to be used up at now: little
// or nothing remaining and a reset time still ahead. A window with no known
// reset time never counts as exhausted, since there'd be no "until".
func (l Limit) Exhausted(now time.Time) bool {
	if l.ResetsAt.IsZero() || !now.Before(l.ResetsAt) || l.Remaining == Unknown {
		return false
	}
	if l.Kind == Tokens {
		return l.Remaining < MinTokens
	}
	return l.Remaining <= 0
}

// HasReset reports whether the window's reset time has passed since it was
// checked, meaning it is known to be full again (Remaining is stale).
func (l Limit) HasReset(now time.Time) bool {
	return !l.ResetsAt.IsZero() && !now.Before(l.ResetsAt)
}

// Usage is everything known about one provider + model.
type Usage struct {
	Provider string
	Model    string
	// Limits are the windows the provider has reported, sorted by kind then
	// window. Empty means usage is unknown.
	Limits []Limit
	// LastObserved is the last time a response (or error) from this model
	// was seen in this process, with or without rate-limit headers. After a
	// restart it starts at the newest CheckedAt loaded from the database.
	LastObserved time.Time
}

// Known reports whether the provider has reported any window for this model.
func (u Usage) Known() bool { return len(u.Limits) > 0 }

type modelKey struct{ provider, model string }

type slot struct {
	kind   Kind
	window Window
}

type modelState struct {
	limits       map[slot]Limit
	lastObserved time.Time
}

// pendingKey identifies one row waiting to be written.
type pendingKey struct {
	modelKey
	slot
}

// Tracker holds the windows. It is safe for concurrent use.
type Tracker struct {
	db          DB
	conventions map[string]convention // provider name → how to read its headers

	mu     sync.RWMutex
	models map[modelKey]*modelState

	pmu     sync.Mutex
	pending map[pendingKey]Limit // latest value per window not yet written
	wake    chan struct{}        // buffered(1): "pending has something"

	flushMu   sync.Mutex // serialises writes so an older batch can't land after a newer one
	stop      chan struct{}
	done      chan struct{}
	closeOnce sync.Once
}

func newTracker(db DB, cfg *config.Config) *Tracker {
	t := &Tracker{
		db:          db,
		conventions: map[string]convention{},
		models:      map[modelKey]*modelState{},
		pending:     map[pendingKey]Limit{},
		wake:        make(chan struct{}, 1),
		stop:        make(chan struct{}),
		done:        make(chan struct{}),
	}
	if cfg != nil {
		for name, p := range cfg.Providers {
			t.conventions[name] = conventionFor(p.BaseURL)
		}
	}
	return t
}

// Observe records the rate-limit headers of a successful upstream response.
// It only updates memory and queues a background write, so it is safe to
// call on the request path. Missing or unrecognised headers change nothing.
func (t *Tracker) Observe(provider, model string, h http.Header, now time.Time) {
	limits := parseHeaders(h, t.conventionOf(provider), now)
	t.mu.Lock()
	defer t.mu.Unlock()
	st := t.stateLocked(provider, model, now)
	t.applyLocked(provider, model, st, limits)
}

// ObserveError records a failed upstream call. Errors that aren't an
// *upstream.Error are ignored. Any rate-limit headers on the error are read
// as for a success. A 429 also marks the model exhausted until
// now + RetryAfter (DefaultBackoff if the provider didn't say), unless the
// headers already show a window exhausted at least that long. That extra
// window is recorded as a requests window (the 429 means "no more requests"),
// per minute or per day depending on how long the wait is.
func (t *Tracker) ObserveError(err error, now time.Time) {
	var ue *upstream.Error
	if !errors.As(err, &ue) {
		return
	}
	limits := parseHeaders(ue.Header, t.conventionOf(ue.Provider), now)

	t.mu.Lock()
	defer t.mu.Unlock()
	st := t.stateLocked(ue.Provider, ue.Model, now)
	t.applyLocked(ue.Provider, ue.Model, st, limits)

	if ue.Kind != upstream.KindRateLimited {
		return
	}
	wait := ue.RetryAfter
	if wait <= 0 {
		wait = DefaultBackoff
	}
	until := now.Add(wait)
	if l, ok := longestExhausted(st, now); ok && !l.ResetsAt.Before(until) {
		return // the headers already explain the 429
	}
	l := Limit{Kind: Requests, Window: windowFor(wait), Remaining: 0, Max: Unknown, ResetsAt: until, CheckedAt: now}
	if old, ok := st.limits[slot{l.Kind, l.Window}]; ok {
		l.Max = old.Max // a 429 says nothing about the window's size; keep what we knew
	}
	t.applyLocked(ue.Provider, ue.Model, st, []Limit{l})
}

// Exhausted reports whether provider/model is known to be out of quota at
// now. If several windows are exhausted it returns the one that lasts
// longest, since that's when the model can be used again. Models with no
// reported windows are never exhausted.
func (t *Tracker) Exhausted(provider, model string, now time.Time) (Limit, bool) {
	t.mu.RLock()
	defer t.mu.RUnlock()
	st := t.models[modelKey{provider, model}]
	if st == nil {
		return Limit{}, false
	}
	return longestExhausted(st, now)
}

func longestExhausted(st *modelState, now time.Time) (Limit, bool) {
	var best Limit
	found := false
	for _, l := range st.limits {
		if l.Exhausted(now) && (!found || l.ResetsAt.After(best.ResetsAt)) {
			best, found = l, true
		}
	}
	return best, found
}

// Model returns what is known about one provider + model. ok is false if it
// has never been observed (nor loaded from the database).
func (t *Tracker) Model(provider, model string) (u Usage, ok bool) {
	t.mu.RLock()
	defer t.mu.RUnlock()
	st := t.models[modelKey{provider, model}]
	if st == nil {
		return Usage{Provider: provider, Model: model}, false
	}
	return usageOf(modelKey{provider, model}, st), true
}

// Snapshot returns every observed provider + model, sorted by provider then
// model. The result is a copy and safe to keep.
func (t *Tracker) Snapshot() []Usage {
	t.mu.RLock()
	out := make([]Usage, 0, len(t.models))
	for k, st := range t.models {
		out = append(out, usageOf(k, st))
	}
	t.mu.RUnlock()
	slices.SortFunc(out, func(a, b Usage) int {
		return cmp.Or(cmp.Compare(a.Provider, b.Provider), cmp.Compare(a.Model, b.Model))
	})
	return out
}

func usageOf(k modelKey, st *modelState) Usage {
	u := Usage{Provider: k.provider, Model: k.model, LastObserved: st.lastObserved}
	for _, l := range st.limits {
		u.Limits = append(u.Limits, l)
	}
	slices.SortFunc(u.Limits, func(a, b Limit) int {
		// requests before tokens, minute before day
		return cmp.Or(cmp.Compare(a.Kind, b.Kind), -cmp.Compare(a.Window, b.Window))
	})
	return u
}

func (t *Tracker) conventionOf(provider string) convention {
	if c, ok := t.conventions[provider]; ok {
		return c
	}
	return openAIConvention
}

func (t *Tracker) stateLocked(provider, model string, now time.Time) *modelState {
	k := modelKey{provider, model}
	st := t.models[k]
	if st == nil {
		st = &modelState{limits: map[slot]Limit{}}
		t.models[k] = st
	}
	if now.After(st.lastObserved) {
		st.lastObserved = now
	}
	return st
}

// applyLocked stores new window values and queues them for writing. A value
// older than the one held is dropped, so a slow response that finishes late
// can't overwrite a newer reading. Windows not in limits are left as they
// were: a response without headers says nothing about them.
func (t *Tracker) applyLocked(provider, model string, st *modelState, limits []Limit) {
	if len(limits) == 0 {
		return
	}
	queued := false
	t.pmu.Lock()
	for _, l := range limits {
		s := slot{l.Kind, l.Window}
		if old, ok := st.limits[s]; ok && l.CheckedAt.Before(old.CheckedAt) {
			continue
		}
		st.limits[s] = l
		if t.db != nil {
			t.pending[pendingKey{modelKey{provider, model}, s}] = l
			queued = true
		}
	}
	t.pmu.Unlock()
	if queued {
		select {
		case t.wake <- struct{}{}:
		default: // a wake-up is already queued; the writer will see this too
		}
	}
}
