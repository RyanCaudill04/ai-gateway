package api

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/RyanCaudill04/ai-gateway/router/internal/apierror"
	"github.com/RyanCaudill04/ai-gateway/router/internal/upstream"
)

// DefaultRouteBudget is how long a virtual model may keep starting new
// attempts, counted from when it starts looking for a provider. Once it is
// spent, no further entry is tried and the request fails with what was
// learned so far.
//
// The usual failures (429, 5xx, a refused connection) come back in
// milliseconds, so a route walks its whole list long before this. The budget
// only matters when providers hang: each attempt has its own time limit (60s
// to the first byte of a stream, 5 minutes for a non-streaming reply), and
// without a budget a four-entry route of hanging providers would hold a
// request for up to 20 minutes while spending a call on each. 90 seconds lets
// one provider use its whole first-byte timeout and a second one be started;
// after that a caller is better served by an honest error than by a third
// slow provider.
//
// It never cuts short an attempt that is already running. A non-streaming
// answer can legitimately take minutes, and a stream that has produced its
// first token is past fallback anyway. So the worst case for one request is
// this budget plus one attempt's own limit.
const DefaultRouteBudget = 90 * time.Second

// outcome is the result of looking for a provider to answer one request.
// Exactly one of answer, err and gone is set.
type outcome struct {
	answer *answer         // a provider answered; nothing has been written yet
	err    *apierror.Error // none did: the error to send
	gone   bool            // the client left; there is nobody to answer
	// refused is true when the last gate (upstreamRequest) stopped a private
	// request. err is then set and the route stops: no other entry is tried.
	refused bool

	calls int // upstream requests made
	// last is the target of the last upstream request: the one that answered
	// when one did. For a pinned model it is always that model.
	last *target
	// failed are the route entries that did not answer, in order. A pinned
	// model has no such list: its one failure is err.
	failed []failure
}

// failure is one route entry that did not answer.
type failure struct {
	attempt apierror.Attempt
	// called is false when no upstream request was made for it: the entry
	// was skipped, or its provider's key was missing.
	called bool
	// err is what the client would be sent if this failure were the whole
	// answer. nil for a skipped entry.
	err *apierror.Error
}

func (o *outcome) skip(a apierror.Attempt) {
	o.failed = append(o.failed, failure{attempt: a})
	slog.Debug("chat: route entry skipped", "provider", a.Provider, "model", a.Model,
		"reason", reasonOf(a), "resets_at", a.ResetsAt)
}

// dispatch finds a provider to answer. It writes nothing to the client.
func (h *chatHandler) dispatch(ctx context.Context, res resolution, body []byte, stream bool) *outcome {
	out := &outcome{}
	if res.pinned {
		h.pinned(ctx, res.targets[0], body, stream, out)
	} else {
		h.route(ctx, res.targets, body, stream, out)
	}
	if res.localOnly && out.err != nil && out.err.Type == apierror.TypeAllProvidersExhausted {
		// The generic text ("no provider is available") does not say that the
		// private route has nowhere else to go, nor what to check. The type,
		// code, status and attempts stay as they are.
		out.err.Message = "None of the local providers the private route is limited to could answer " +
			"(is the local model server, Ollama, running?). Nothing was sent to any other provider. " +
			"See attempts for each reason."
	}
	return out
}

// pinned makes the one attempt a provider/model ID gets. It never falls back:
// the caller chose this model, and an answer from another one is not what
// they asked for. The provider's failure is the answer.
//
// The request is sent even when the tracker believes the model is out of
// quota. Skipping an exhausted entry is worth it in a route because there is
// somewhere else to go; here there isn't, so answering from the tracker would
// only save one cheap 429 while risking a refusal the provider would not have
// given. The tracker can be wrong in that direction: a 429 with no
// Retry-After is backed off for a guessed minute, and a 429 for one large
// request (tokens per minute) blocks small ones too. Sending also keeps the
// UI's "Retry" button honest, and whatever the provider says refreshes the
// tracker. A model is therefore never locked out by stale data.
func (h *chatHandler) pinned(ctx context.Context, t target, body []byte, stream bool, out *outcome) {
	out.last = &t
	ans, ue, _ := h.call(ctx, t, body, stream, out)
	switch {
	case out.gone:
	case ue != nil:
		slog.Log(ctx, failureLevel(ue), "chat: provider failed", failureAttrs(ue)...)
		out.err = h.providerError(ue)
	default:
		out.answer = ans
	}
}

// route tries a virtual model's entries in order until one answers.
//
// Before spending a request on an entry it is skipped if the tracker already
// knows it is out of quota, or if a bigger model has already said the prompt
// is too long. After a failed call, fallbackState.after decides whether the
// next entry is worth trying. Every entry that did not answer is recorded, in
// order, for the x-router-fallback header and the error body.
//
// The loop is bounded three ways: it visits each entry at most once (the
// config rejects duplicates, so that is at most the length of the route), it
// stops starting attempts when RouteBudget is spent, and it stops at once
// when the client goes away.
//
// It can only run before anything is written: an attempt returns either an
// error or an answer nothing has been sent for, and for a stream that answer
// already holds the first event. Once that event is written (writeStream),
// the route is over, and a later failure is reported inside the stream.
func (h *chatHandler) route(ctx context.Context, targets []target, body []byte, stream bool, out *outcome) {
	start := time.Now()
	var st fallbackState
	for i, t := range targets {
		if ctx.Err() != nil {
			out.gone = true
			return
		}
		// Only after a real call: skipping costs no time, and a route always
		// gets to make its first call.
		if out.calls > 0 && time.Since(start) >= h.RouteBudget {
			slog.Warn("chat: route out of time, remaining entries not tried",
				"budget", h.RouteBudget.String(), "calls", out.calls, "untried", len(targets)-i)
			break
		}

		// Too small is checked first: it holds for this request whatever the
		// quota, so an entry that is both is listed as too small.
		if st.tooSmall(t) {
			out.skip(tooSmallAttempt(t))
			continue
		}
		if l, exhausted := h.Limits.Exhausted(t.provider.Name, t.model.ID, h.Now()); exhausted {
			out.skip(skippedAttempt(t, l))
			continue
		}

		ans, ue, called := h.call(ctx, t, body, stream, out)
		switch {
		case out.gone, out.refused:
			return
		case ue == nil:
			out.answer = ans
			return
		}
		f := failure{attempt: h.attemptOf(ue), called: called, err: h.providerError(ue)}
		out.failed = append(out.failed, f)
		moveOn := st.after(t, ue)
		slog.Log(ctx, failureLevel(ue), "chat: route entry failed",
			append(failureAttrs(ue), "called", called, "falls_through", moveOn)...)
		if !moveOn {
			out.err = f.err
			return
		}
	}
	out.err = routeError(out.failed)
}

// call makes one attempt at t and sorts what happened into an answer, a
// provider failure (ue), or the client having left (out.gone, with nothing
// else returned). called is false when the failure came before any request
// was sent.
func (h *chatHandler) call(ctx context.Context, t target, body []byte, stream bool, out *outcome) (ans *answer, ue *upstream.Error, called bool) {
	req, ue := h.upstreamRequest(t, body)
	if ue != nil {
		if errors.Is(ue, errLocalOnly) {
			// Not a provider failure to fall back from: the route is over. The
			// refused entry is not listed as an attempt, as it was not tried.
			out.refused = true
			out.err = apierror.Unavailable("private_route_violation",
				"The private route refused to send this request to a provider that is not local. Nothing was sent.")
		}
		return nil, ue, false
	}
	out.calls++
	out.last = &t
	ans, err := h.attempt(ctx, t, req, stream)
	switch {
	case err == nil:
		return ans, nil, true
	case clientGone(ctx, err):
		// Checked before the error is looked at: once the client has left,
		// even a genuine provider failure is no reason to try another.
		out.gone = true
		return nil, nil, true
	}
	if !errors.As(err, &ue) {
		// attempt only returns *upstream.Error or the context's error; this
		// is a safety net, not an expected path. Only the error's type is
		// logged: its text is unknown, so it is kept out like any message.
		slog.Error("chat: unexpected attempt error", "provider", t.provider.Name, "model", t.model.ID,
			"err_type", fmt.Sprintf("%T", err))
		ue = &upstream.Error{Kind: upstream.KindProvider, Provider: t.provider.Name, Model: t.model.ID}
	}
	return nil, ue, true
}

// fallbackState is what a route remembers about the entries that failed so
// far, to decide whether later ones are worth a request.
type fallbackState struct {
	// tooLong is the largest context window among the entries that rejected
	// the prompt as longer than their window. Zero: none has.
	tooLong int
	// rejectedBy is the first provider that rejected the request as invalid.
	rejectedBy string
}

// after records that t failed with ue and reports whether the route moves on
// to its next entry. This is the whole fall-through policy, one kind at a
// time:
//
//   - rate_limited: move on. This is what fallback is for, and the tracker
//     now knows to skip t until it resets.
//
//   - provider_error (5xx, timeout, network error, an unexpected status, a
//     rejected or missing provider key): move on. None of these says anything
//     about the request, and another provider is not affected by them.
//
//   - context_too_long: move on, but only to bigger models. A route's entries
//     differ in context window, so a bigger one may well take the prompt; an
//     entry with a window no larger than t's would reject it just the same
//     and only waste a request, so tooSmall skips those. That turns "try
//     everything" into at most one call per step up in size.
//     A 413 is the exception: it is a provider's own cap on request size
//     (Groq's per-minute token limit, Workers AI's body limit), which says
//     nothing about the model's window or about anyone else's cap. It moves
//     on without ruling any entry out.
//
//   - bad_request: move on once. The request is the same for every provider,
//     but they do not accept the same parameters (Groq rejects logprobs,
//     logit_bias, n > 1 and messages[].name; others take them), so one
//     provider's 400 may be its own quirk, and stopping there would make a
//     route only as tolerant as its first entry. When a second, different
//     provider rejects it too, the request itself is at fault: the route
//     stops and the client gets that 400, instead of every remaining
//     provider being charged a request to say the same thing.
//
// A kind added to the upstream package later moves on: it is safer to try
// the next entry than to fail a request another provider could have served.
func (s *fallbackState) after(t target, ue *upstream.Error) (moveOn bool) {
	switch ue.Kind {
	case upstream.KindContextTooLong:
		if ue.Status != http.StatusRequestEntityTooLarge {
			s.tooLong = max(s.tooLong, t.model.ContextLength)
		}
	case upstream.KindBadRequest:
		if s.rejectedBy != "" && s.rejectedBy != t.provider.Name {
			return false
		}
		if s.rejectedBy == "" {
			s.rejectedBy = t.provider.Name
		}
	}
	return true
}

// tooSmall reports whether t can be ruled out without asking: an entry with
// a context window at least as large as t's has already rejected the prompt
// as too long.
func (s *fallbackState) tooSmall(t target) bool {
	return s.tooLong > 0 && t.model.ContextLength <= s.tooLong
}

func tooSmallAttempt(t target) apierror.Attempt {
	return apierror.Attempt{
		Provider: t.provider.Name, Model: t.model.ID, Type: apierror.TypeContextLengthExceeded,
		Message: "Not tried: a model with a context window at least as large already rejected this request as too long.",
	}
}

// routeError is the answer when a route ran out of entries.
//
// Normally that is all_providers_exhausted with every attempt listed. But
// when every entry turned the request down for what is in it (too long, or
// invalid), no provider was "exhausted" and waiting will not help: a 503
// would tell the client, and its SDK's retry loop, to send the same doomed
// request again. So the client gets the last such refusal as an ordinary 400,
// naming the provider that gave it, as if that model had been pinned. The
// other refusals are still in the x-router-fallback header.
//
// One entry that was rate limited or down is enough to keep it
// all_providers_exhausted: that entry never judged the request, so it may
// well be served later.
func routeError(failed []failure) *apierror.Error {
	attempts := make([]apierror.Attempt, len(failed))
	onlyRefusals := true
	var refusal *apierror.Error // the last one from a provider that was actually asked
	for i, f := range failed {
		attempts[i] = f.attempt
		switch f.attempt.Type {
		case apierror.TypeContextLengthExceeded, apierror.TypeInvalidRequest:
			if f.err != nil {
				refusal = f.err
			}
		default:
			onlyRefusals = false
		}
	}
	if onlyRefusals && refusal != nil {
		return refusal
	}
	return apierror.AllProvidersExhausted(attempts)
}

// failureLevel is the log level for a provider failure. Only a provider
// actually being broken is a warning, since someone may need to act on it (a
// rejected key, an outage). Rate limits are routine on free tiers, and a
// request the provider turned down is the client's business.
func failureLevel(ue *upstream.Error) slog.Level {
	if ue.Kind == upstream.KindProvider {
		return slog.LevelWarn
	}
	return slog.LevelInfo
}

// failureAttrs is what gets logged about a provider failure: metadata only.
// The provider's message is never logged. It is free text from outside, and
// a provider can quote the parameter or part of the prompt it objects to;
// request content must not reach the process log, by default or by accident.
// The caller still gets the message in the error body, where it is their own
// content coming back to them.
func failureAttrs(ue *upstream.Error) []any {
	return []any{"provider", ue.Provider, "model", ue.Model, "kind", ue.Kind,
		"status", ue.Status, "in_stream", ue.InStream, "retry_after", ue.RetryAfter.String()}
}
