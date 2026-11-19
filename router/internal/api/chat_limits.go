package api

import (
	"net/http"
	"time"

	"github.com/RyanCaudill04/ai-gateway/router/internal/apierror"
	"github.com/RyanCaudill04/ai-gateway/router/internal/config"
	"github.com/RyanCaudill04/ai-gateway/router/internal/ratelimit"
	"github.com/RyanCaudill04/ai-gateway/router/internal/upstream"
)

// LimitTracker is what the chat handler needs from the rate-limit tracker
// (#16). *ratelimit.Tracker satisfies it as it is; a tracker opened with a
// nil database keeps everything in memory, which is what tests use.
//
// The handler tells it about every upstream outcome and asks it two things:
// "is this model out of quota?" before spending a request on a route entry,
// and "which limit was that?" after a 429, to fill in the error.
//
// All three run on the request's goroutine, so they must be quick.
type LimitTracker interface {
	// Observe is called with the headers of every successful upstream
	// response. model is the provider's own model ID.
	Observe(provider, model string, h http.Header, now time.Time)
	// ObserveError is called with every *upstream.Error, before or inside a
	// stream. A streamed call can therefore be observed twice: once when its
	// headers arrive, and again if it then fails. A client hanging up is
	// never passed on: it says nothing about the provider.
	ObserveError(err error, now time.Time)
	// Exhausted reports whether the model is known to be out of quota at
	// now, and if so which window (the one that lasts longest).
	Exhausted(provider, model string, now time.Time) (ratelimit.Limit, bool)
}

// noLimits is the tracker when none is wired: it records nothing, so no model
// is ever exhausted and a 429 carries only what the provider itself said.
type noLimits struct{}

func (noLimits) Observe(string, string, http.Header, time.Time) {}
func (noLimits) ObserveError(error, time.Time)                  {}
func (noLimits) Exhausted(string, string, time.Time) (ratelimit.Limit, bool) {
	return ratelimit.Limit{}, false
}

// NextInRoute reports which entry of a virtual model a request would be sent
// to first right now: the first one whose provider is enabled and which the
// tracker does not know to be out of quota. Those are the same two checks, in
// the same order, the chat handler makes before it spends a request, so the
// usage display (#17) and the router agree on what "Auto would use next".
//
// model is the provider's own model ID. ok is false when route is not a
// virtual model, or when no entry is usable at the moment (every one is
// disabled or exhausted); a chat request would then fail without calling
// anyone. It is a prediction, not a promise: the entry can still fail when
// called, and a prompt too long for it is only found out by asking.
func NextInRoute(cfg *config.Config, enabled EnabledFunc, limits LimitTracker, route string, now time.Time) (provider, model string, ok bool) {
	if _, isRoute := cfg.Routes[route]; !isRoute {
		return "", "", false
	}
	if limits == nil {
		limits = noLimits{}
	}
	res, _ := resolve(cfg, enabled, route)
	for _, t := range res.targets {
		if _, out := limits.Exhausted(t.provider.Name, t.model.ID, now); !out {
			return t.provider.Name, t.model.ID, true
		}
	}
	return "", "", false
}

// limitAfter is what the tracker knows about the rate limit behind ue, asked
// after the tracker has been told about ue. ok is false when ue is not a rate
// limit or the tracker has nothing (none is wired).
//
// After a 429 the tracker always holds something for the model: the window
// the provider's headers showed as used up, or else a window it made up to
// back off for Retry-After (one minute if the provider gave no time at all).
func (h *chatHandler) limitAfter(ue *upstream.Error) (ratelimit.Limit, bool) {
	if ue.Kind != upstream.KindRateLimited {
		return ratelimit.Limit{}, false
	}
	return h.Limits.Exhausted(ue.Provider, ue.Model, h.Now())
}

// attemptOf describes a failed call for a route's attempts list. A rate limit
// gets the tracker's limit type, window and reset time. That reset is when
// the router will next try the model, whether the provider reported it or
// the router is only backing off, and a route's caller wants exactly that:
// before then, asking again cannot reach this model.
func (h *chatHandler) attemptOf(ue *upstream.Error) apierror.Attempt {
	a := apierror.AttemptFrom(ue, apierror.LimitHint{})
	if l, ok := h.limitAfter(ue); ok {
		a.LimitType, a.Window, a.ResetsAt = apierror.LimitType(l.Kind), apierror.Window(l.Window), l.ResetsAt
	}
	return a
}

// skippedAttempt describes a route entry that was not called because the
// tracker already knows it is out of quota until l resets.
func skippedAttempt(t target, l ratelimit.Limit) apierror.Attempt {
	return apierror.Attempt{
		Provider: t.provider.Name, Model: t.model.ID, Type: apierror.TypeRateLimited,
		Message:   "Not tried: already known to be out of quota.",
		LimitType: apierror.LimitType(l.Kind), Window: apierror.Window(l.Window), ResetsAt: l.ResetsAt,
	}
}

// providerError is the client-facing error for one provider's failure, when
// that failure is the whole answer: a pinned model, an error after a stream
// has started, or a route that stopped because the request itself is wrong.
//
// A rate limit says which limit was hit and when it resets, from the tracker.
// One case is left out on purpose: when the provider gave no time to wait and
// all the tracker holds is its own default back-off (recognised by a reset no
// further away than that back-off), nothing is reported. The router made that
// window up to pace its routes; a pinned request is sent to the provider
// regardless, so showing the guess as "resets at" would put a countdown in
// the UI that the provider never promised.
func (h *chatHandler) providerError(ue *upstream.Error) *apierror.Error {
	l, ok := h.limitAfter(ue)
	if ok && ue.RetryAfter <= 0 && !l.ResetsAt.After(h.Now().Add(ratelimit.DefaultBackoff)) {
		ok = false
	}
	if !ok {
		// Without a tracker this still carries resets_at, from the
		// provider's Retry-After.
		return apierror.FromUpstream(ue, apierror.LimitHint{})
	}
	return apierror.RateLimited(apierror.Limit{
		Provider: ue.Provider, Model: ue.Model,
		LimitType: apierror.LimitType(l.Kind), Window: apierror.Window(l.Window), ResetsAt: l.ResetsAt,
	})
}
