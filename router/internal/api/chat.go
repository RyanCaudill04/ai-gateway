package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"time"

	"github.com/RyanCaudill04/ai-gateway/router/internal/apierror"
	"github.com/RyanCaudill04/ai-gateway/router/internal/config"
	"github.com/RyanCaudill04/ai-gateway/router/internal/upstream"
)

// MaxChatBody caps a chat completions request body. Requests can carry
// base64 images, so it is far above what text needs; 20 MiB is the most any
// supported provider accepts inline (Gemini's total request limit). The whole
// body is held in memory while it is forwarded, so it must stay modest on a Pi.
const MaxChatBody = 20 << 20

// KeyFunc returns the API key registered for a provider. ok is false when
// none is. It is called on every request (keys can be added while the router
// runs, #40) and never for local providers, which need no key.
type KeyFunc func(provider string) (key string, ok bool)

// Usage is the token counts a provider reported for one request.
type Usage struct {
	PromptTokens     int
	CompletionTokens int
}

// RequestSummary describes one finished chat request for the request log
// (#15). It deliberately holds no prompt or completion text, and nothing from
// the request body except the requested model name.
type RequestSummary struct {
	RequestedModel string // the client's `model`, e.g. "fast" or "groq/llama-3.3-70b-versatile"
	Provider       string // provider that answered, or was called last; "" if none was called
	Model          string // that provider's own model ID
	Stream         bool
	// Status is the HTTP status sent to the client, or StatusClientClosed when
	// the client went away before any response was sent.
	Status int
	// ErrorType is the apierror type sent to the client, before or inside the
	// stream; "" on success.
	ErrorType apierror.Type
	// ClientGone is true when the client disconnected before the response was
	// complete (the upstream request was then cancelled).
	ClientGone bool
	Usage      *Usage // nil when the provider didn't report token counts
	Latency    time.Duration
	// Attempts is how many upstream requests were made: 1 when the first
	// provider answered, more after fallback, 0 when the request failed before
	// any provider was called (bad request, or every entry skipped).
	Attempts int
	// Fallback lists the route entries that did not answer, in the order they
	// were tried: the same list as the x-router-fallback header. Empty when
	// the first entry answered, and always for a pinned model, whose one
	// failure is ErrorType.
	Fallback []FallbackAttempt
}

// StatusClientClosed is RequestSummary.Status for a client that disconnected
// before a response was sent (nginx's convention; never sent on the wire).
const StatusClientClosed = 499

// SummaryFunc receives the summary of every chat request, once, when it ends.
type SummaryFunc func(RequestSummary)

// ChatDeps is what the chat completions handler needs. Only Config, Enabled
// and Client are required; the rest default to "nothing registered".
type ChatDeps struct {
	Config  *config.Config
	Enabled EnabledFunc
	Client  *upstream.Client
	// Key looks up provider API keys. nil means no keys, so only local
	// providers can answer.
	Key KeyFunc
	// Limits is the rate-limit tracker (#16): it is told about every upstream
	// response and failure, and asked which models are out of quota. nil:
	// nothing is tracked and no model ever counts as exhausted.
	Limits LimitTracker
	// OnSummary receives each request's summary (#15). nil: ignored.
	OnSummary SummaryFunc
	// RouteBudget is how long a virtual model may keep starting new attempts
	// (see DefaultRouteBudget). Zero or less: the default.
	RouteBudget time.Duration
	// Now is the clock for rate-limit windows. nil: time.Now. Tests set it to
	// move past a reset without waiting.
	Now func() time.Time
}

type chatHandler struct {
	ChatDeps
}

// Chat serves POST /v1/chat/completions in OpenAI's format, streaming and not.
//
// A request goes through four separate steps. Only the last one writes to the
// client, which is what makes fallback safe: until a provider has produced
// something to send, the client has seen nothing and another can be tried.
//
//  1. resolve: `model` → an ordered list of targets (chat_resolve.go)
//  2. dispatch: find a target that answers. A pinned model gets one attempt,
//     a virtual model tries its entries in order (chat_fallback.go)
//  3. attempt: one call to one target, returning a ready response or an error
//     (chat_attempt.go)
//  4. write: send the ready response, streaming or not (chat_write.go)
func Chat(d ChatDeps) http.Handler {
	if d.Key == nil {
		d.Key = func(string) (string, bool) { return "", false }
	}
	if d.Limits == nil {
		d.Limits = noLimits{}
	}
	if d.OnSummary == nil {
		d.OnSummary = func(RequestSummary) {}
	}
	if d.RouteBudget <= 0 {
		d.RouteBudget = DefaultRouteBudget
	}
	if d.Now == nil {
		d.Now = time.Now
	}
	return &chatHandler{d}
}

// chatRequest is the part of the body the router itself reads. Everything
// else is forwarded untouched; the provider validates it.
type chatRequest struct {
	Model  string
	Stream bool
}

func (h *chatHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	sum := &RequestSummary{}
	defer func() {
		sum.Latency = time.Since(start)
		// Metadata only: never the body, prompt or completion.
		slog.Debug("chat completion", "requested_model", sum.RequestedModel,
			"provider", sum.Provider, "model", sum.Model, "stream", sum.Stream,
			"status", sum.Status, "error_type", sum.ErrorType, "client_gone", sum.ClientGone,
			"attempts", sum.Attempts, "fallback", FallbackHeader(sum.Fallback),
			"latency_ms", sum.Latency.Milliseconds())
		h.OnSummary(*sum)
	}()

	body, req, aerr := readChatRequest(w, r)
	if aerr != nil {
		writeError(w, aerr, sum)
		return
	}
	sum.RequestedModel, sum.Stream = req.Model, req.Stream

	res, aerr := resolve(h.Config, h.Enabled, req.Model)
	if aerr != nil {
		if len(res.targets) > 0 {
			// The target is known but can't be used (pinned model of a
			// disabled provider): still say which one it was.
			setRouterHeaders(w, res.targets[0], sum)
		}
		writeError(w, aerr, sum)
		return
	}

	// Find a provider that answers. Nothing is written to the client in
	// here, so the headers describe sets go out before the status line, for a
	// stream as much as for a JSON body.
	out := h.dispatch(r.Context(), res, body, req.Stream)
	out.describe(w, sum)
	switch {
	case out.gone:
		// The client hung up while a provider was being found: there is
		// nobody left to answer.
		sum.Status, sum.ClientGone = StatusClientClosed, true
	case out.err != nil:
		if !res.pinned {
			slog.Warn("chat: no provider answered", "requested_model", req.Model,
				"error_type", out.err.Type, "calls", out.calls, "not_answered", len(out.failed))
		}
		writeError(w, out.err, sum)
	case req.Stream:
		h.writeStream(w, r, out.answer, sum)
	default:
		writeJSON(w, out.answer, sum)
	}
}

// readChatRequest reads the body (up to MaxChatBody) and the two fields the
// router needs. It does not check anything the provider will check anyway.
func readChatRequest(w http.ResponseWriter, r *http.Request) ([]byte, chatRequest, *apierror.Error) {
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, MaxChatBody))
	if err != nil {
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			return nil, chatRequest{}, apierror.RequestTooLarge(MaxChatBody)
		}
		return nil, chatRequest{}, apierror.InvalidRequest("Could not read the request body.", "")
	}

	// Decoding into a struct still checks the whole body is valid JSON, but
	// only keeps these two fields. A JSON array or string fails here; `null`
	// decodes to nothing and is caught by the missing model below.
	var raw struct {
		Model  json.RawMessage `json:"model"`
		Stream json.RawMessage `json:"stream"`
	}
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, chatRequest{}, apierror.InvalidRequest("The request body must be a JSON object.", "")
	}
	var req chatRequest
	if json.Unmarshal(raw.Model, &req.Model) != nil || req.Model == "" {
		return nil, chatRequest{}, apierror.InvalidRequest("`model` is required and must be a string.", "model")
	}
	if len(raw.Stream) > 0 && string(raw.Stream) != "null" {
		if json.Unmarshal(raw.Stream, &req.Stream) != nil {
			return nil, chatRequest{}, apierror.InvalidRequest("`stream` must be true or false.", "stream")
		}
	}
	return body, req, nil
}

// setRouterHeaders names the provider and model that answered (or were
// called last), on success and error alike. x-router-model is the provider's
// own model ID, matching the `model` field of an error body; together they
// make the pinned ID provider + "/" + model.
func setRouterHeaders(w http.ResponseWriter, t target, sum *RequestSummary) {
	w.Header().Set("X-Router-Provider", t.provider.Name)
	w.Header().Set("X-Router-Model", t.model.ID)
	sum.Provider, sum.Model = t.provider.Name, t.model.ID
}

// clientGone tells "the client hung up" from a real failure. The upstream
// package returns the context's error in that case, never an *upstream.Error.
func clientGone(ctx context.Context, err error) bool {
	return ctx.Err() != nil || errors.Is(err, context.Canceled)
}

func writeError(w http.ResponseWriter, e *apierror.Error, sum *RequestSummary) {
	sum.Status, sum.ErrorType = e.Status, e.Type
	apierror.Write(w, e)
}
