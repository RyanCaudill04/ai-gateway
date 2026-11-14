package apierror

import (
	"time"

	"github.com/RyanCaudill04/ai-gateway/router/internal/upstream"
)

// LimitHint is what the rate-limit tracker (#16) knows about a rate limit that
// the upstream error alone doesn't say: which counter ran out. Zero values
// mean unknown and are left out of the response.
type LimitHint struct {
	LimitType LimitType
	Window    Window
}

// AttemptFrom turns a failed upstream call into one entry of the attempts
// list (for fallback, #11). For a rate limit, the reset time is now plus the
// provider's Retry-After, when it gave one.
func AttemptFrom(ue *upstream.Error, hint LimitHint) Attempt {
	return attemptFrom(ue, hint, time.Now())
}

func attemptFrom(ue *upstream.Error, hint LimitHint, now time.Time) Attempt {
	a := Attempt{Provider: ue.Provider, Model: ue.Model, Message: ue.Message}
	switch ue.Kind {
	case upstream.KindRateLimited:
		a.Type = TypeRateLimited
		a.LimitType, a.Window = hint.LimitType, hint.Window
		if ue.RetryAfter > 0 {
			a.ResetsAt = now.Add(ue.RetryAfter)
		}
	case upstream.KindContextTooLong:
		a.Type = TypeContextLengthExceeded
	case upstream.KindBadRequest:
		a.Type = TypeInvalidRequest
	default: // KindProvider, and any kind added later: assume it isn't the client's fault
		a.Type = TypeProviderError
	}
	return a
}

// FromUpstream is the client-facing error for a pinned model: one provider,
// no fallback, so its failure is the answer. Provider and model are copied
// from ue so the UI can say who failed.
//
//	rate_limited     -> 429 rate_limited, resets_at from RetryAfter
//	context_too_long -> 400 context_length_exceeded
//	bad_request      -> 400 invalid_request
//	provider_error   -> 502 provider_error
//
// Except for rate limits, whose message is generated from the limit fields,
// the upstream message is used as-is; the upstream package has already
// removed keys from it.
func FromUpstream(ue *upstream.Error, hint LimitHint) *Error {
	return fromUpstream(ue, hint, time.Now())
}

func fromUpstream(ue *upstream.Error, hint LimitHint, now time.Time) *Error {
	a := attemptFrom(ue, hint, now)
	switch a.Type {
	case TypeRateLimited:
		return RateLimited(Limit{
			Provider: a.Provider, Model: a.Model,
			LimitType: a.LimitType, Window: a.Window, ResetsAt: a.ResetsAt,
		})
	case TypeContextLengthExceeded:
		return ContextLengthExceeded(a.Provider, a.Model, a.Message)
	case TypeInvalidRequest:
		e := InvalidRequest(a.Message, "")
		e.Provider, e.Model = a.Provider, a.Model
		return e
	}
	return ProviderError(a.Provider, a.Model, a.Message)
}
