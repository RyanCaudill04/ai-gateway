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
	"github.com/RyanCaudill04/ai-gateway/router/internal/apikeys"
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

// UpstreamObserver sees the outcome of every call to a provider, so the
// rate-limit tracker (#16) can read x-ratelimit-* headers and notice 429s.
// model is the provider's own model ID. On success err is nil and h holds the
// response headers; on failure err is the provider's error and h is
// err.Header (nil when no response arrived). A streamed call can be observed
// twice: once when its headers arrive, and again if it then fails inside the
// stream. A client hanging up is not observed: it says nothing about the
// provider.
//
// It runs on the request's goroutine, so it must be quick.
type UpstreamObserver interface {
	ObserveUpstream(provider, model string, h http.Header, err *upstream.Error)
}

// Usage is the token counts a provider reported for one request.
type Usage struct {
	PromptTokens     int
	CompletionTokens int
}

// RequestSummary describes one finished chat request for the request log
// (#15). It deliberately holds no prompt or completion text, and nothing from
// the request body except the requested model name.
type RequestSummary struct {
	Service        string // name of the calling service's API key; "" if the context has none
	RequestedModel string // the client's `model`, e.g. "fast" or "groq/llama-3.3-70b-versatile"
	Provider       string // provider used (or last attempted); "" if none was resolved
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
	// Attempts is how many providers were called: 1 until fallback (#11), 0
	// when the request failed before any provider was tried.
	Attempts int
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
	// Observer sees every upstream outcome (#16). nil: ignored.
	Observer UpstreamObserver
	// OnSummary receives each request's summary (#15). nil: ignored.
	OnSummary SummaryFunc
}

type chatHandler struct {
	ChatDeps
}

// Chat serves POST /v1/chat/completions in OpenAI's format, streaming and not.
//
// A request goes through three separate steps, so fallback (#11) only has to
// change the middle one:
//
//  1. resolve: `model` → an ordered list of targets (chat_resolve.go)
//  2. attempt: one call to one target, returning a ready response or an error
//     before anything is written to the client (chat_attempt.go)
//  3. write: send the ready response, streaming or not (chat_write.go)
func Chat(d ChatDeps) http.Handler {
	if d.Key == nil {
		d.Key = func(string) (string, bool) { return "", false }
	}
	if d.Observer == nil {
		d.Observer = nopObserver{}
	}
	if d.OnSummary == nil {
		d.OnSummary = func(RequestSummary) {}
	}
	return &chatHandler{d}
}

type nopObserver struct{}

func (nopObserver) ObserveUpstream(string, string, http.Header, *upstream.Error) {}

// chatRequest is the part of the body the router itself reads. Everything
// else is forwarded untouched; the provider validates it.
type chatRequest struct {
	Model  string
	Stream bool
}

func (h *chatHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	sum := &RequestSummary{}
	sum.Service, _ = apikeys.NameFrom(r.Context())
	defer func() {
		sum.Latency = time.Since(start)
		// Metadata only: never the body, prompt or completion.
		slog.Debug("chat completion", "requested_model", sum.RequestedModel,
			"provider", sum.Provider, "model", sum.Model, "stream", sum.Stream,
			"status", sum.Status, "error_type", sum.ErrorType, "client_gone", sum.ClientGone,
			"attempts", sum.Attempts, "latency_ms", sum.Latency.Milliseconds())
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

	// ---- Fallback (#11) goes here. ----
	// Today only the first enabled target is called. #11 turns this into a
	// loop over res.targets (unless res.pinned): attempt each in order, and on
	// an *upstream.Error that warrants it, record the attempt and try the
	// next. That is safe because attempt writes nothing to the client; the
	// first byte goes out in writeJSON/writeStream, after the loop.
	t := res.targets[0]
	setRouterHeaders(w, t, sum)
	sum.Attempts++
	ans, err := h.attempt(r.Context(), t, body, req.Stream)
	if err != nil {
		h.attemptFailed(w, r, err, sum)
		return
	}
	// ---- End of the part #11 changes. ----

	if req.Stream {
		h.writeStream(w, r, ans, sum)
	} else {
		writeJSON(w, ans, sum)
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
// attempted), on success and error alike. x-router-model is the provider's
// own model ID, matching the `model` field of an error body; together they
// make the pinned ID provider + "/" + model.
func setRouterHeaders(w http.ResponseWriter, t target, sum *RequestSummary) {
	w.Header().Set("X-Router-Provider", t.provider.Name)
	w.Header().Set("X-Router-Model", t.model.ID)
	sum.Provider, sum.Model = t.provider.Name, t.model.ID
}

// attemptFailed answers a failed attempt. Nothing has been written yet, so
// the client gets a normal error response with the right status.
func (h *chatHandler) attemptFailed(w http.ResponseWriter, r *http.Request, err error, sum *RequestSummary) {
	if clientGone(r.Context(), err) {
		sum.Status, sum.ClientGone = StatusClientClosed, true
		return
	}
	var ue *upstream.Error
	if !errors.As(err, &ue) {
		// attempt only returns *upstream.Error or the context's error; this
		// is a safety net, not an expected path.
		slog.Error("chat: unexpected attempt error", "provider", sum.Provider, "model", sum.Model, "err", err)
		writeError(w, apierror.ProviderError(sum.Provider, sum.Model, ""), sum)
		return
	}
	slog.Warn("chat: provider failed", "provider", ue.Provider, "model", ue.Model,
		"kind", ue.Kind, "status", ue.Status, "message", ue.Message)
	// LimitHint (which counter ran out) comes from the rate-limit tracker
	// once it exists (#16).
	writeError(w, apierror.FromUpstream(ue, apierror.LimitHint{}), sum)
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
