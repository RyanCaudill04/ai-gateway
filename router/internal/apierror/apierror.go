// Package apierror is the one shape of error the router sends to clients.
//
// Every failure, from a missing sign-in to a provider outage, is written as
//
//	{"error": {"message": "...", "type": "rate_limited", "code": "...", ...}}
//
// The four fields the official OpenAI SDKs read (message, type, code, param)
// are where they expect them, so an SDK client gets a typed exception with a
// readable message. The extra fields (provider, model, limit_type, window,
// resets_at, attempts) let the chat UI say which limit was hit and when it
// resets; each is left out when it does not apply.
//
// This package imports only the upstream package (to translate its errors), so
// api, auth and every later handler can use it.
//
// Messages are shown to people, so they must never hold a key, a request body
// or a header. The constructors here add only fixed text and names from the
// router's own config; text that came from a provider is already redacted by
// the upstream package.
package apierror

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"time"
)

// Type is the machine-readable category of an error. The set is closed: the
// UI switches on it.
type Type string

const (
	// TypeRateLimited: a limit was hit (a provider's, or a service key's daily
	// cap). Waiting until resets_at, if known, fixes it. HTTP 429.
	TypeRateLimited Type = "rate_limited"
	// TypeContextLengthExceeded: the prompt is too big for the model. Retrying
	// the same request will not help. HTTP 400, like OpenAI's own error.
	TypeContextLengthExceeded Type = "context_length_exceeded"
	// TypeProviderError: the provider (or the router itself) failed in a way
	// the client can't fix. HTTP 502, or 503 for the router's own faults.
	TypeProviderError Type = "provider_error"
	// TypeAllProvidersExhausted: a virtual model tried every provider and none
	// worked. The attempts list says why each failed. HTTP 429 or 503.
	TypeAllProvidersExhausted Type = "all_providers_exhausted"
	// TypeInvalidRequest: the request itself is wrong (bad JSON, unknown model,
	// unknown route, wrong method). HTTP 400, 404 or 405.
	TypeInvalidRequest Type = "invalid_request"
	// TypeUnauthorized: no credentials, or not allowed in. HTTP 401 or 403.
	TypeUnauthorized Type = "unauthorized"
)

// LimitType says what a rate limit counts.
type LimitType string

const (
	LimitRequests LimitType = "requests"
	LimitTokens   LimitType = "tokens"
)

// Window is the period a rate limit applies to.
type Window string

const (
	WindowMinute Window = "minute"
	WindowDay    Window = "day"
)

// Attempt is one provider a virtual model tried (or skipped because it was
// already known to be exhausted). Type is why it failed, using the same words
// as an error's type; only rate_limited, context_length_exceeded,
// provider_error and invalid_request make sense here.
type Attempt struct {
	Provider string
	Model    string
	Type     Type
	Message  string
	// LimitType, Window and ResetsAt are set when Type is rate_limited and
	// they are known (from the rate-limit tracker). Zero means unknown and is
	// left out of the JSON.
	LimitType LimitType
	Window    Window
	ResetsAt  time.Time
}

// Error is a client-facing error. Build one with a constructor below rather
// than a literal, so Type, Status and Code stay consistent.
type Error struct {
	Type Type
	// Status is the HTTP status. Constructors set it; unauthorized,
	// invalid_request, provider_error and all_providers_exhausted each have
	// more than one (see the constructors).
	Status int
	// Code is a finer, stable identifier within Type, such as
	// "rate_limit_exceeded" or "not_found". The OpenAI SDKs expose it as .code.
	Code    string
	Message string
	// Param names the request field at fault, for invalid_request. Optional.
	Param string

	Provider  string
	Model     string
	LimitType LimitType
	Window    Window
	// ResetsAt is when the limit clears, if known. It is also the source of
	// the Retry-After header.
	ResetsAt time.Time
	// Attempts is only for all_providers_exhausted.
	Attempts []Attempt
}

func (e *Error) Error() string { return string(e.Type) + ": " + e.Message }

// wireError and wireAttempt are the JSON shapes. They exist so empty fields
// are omitted (a zero time.Time is not, even with omitempty) and resets_at is
// always UTC RFC 3339 at second precision.
type wireError struct {
	Message   string        `json:"message"`
	Type      Type          `json:"type"`
	Code      string        `json:"code"`
	Param     string        `json:"param,omitempty"`
	Provider  string        `json:"provider,omitempty"`
	Model     string        `json:"model,omitempty"`
	LimitType LimitType     `json:"limit_type,omitempty"`
	Window    Window        `json:"window,omitempty"`
	ResetsAt  string        `json:"resets_at,omitempty"`
	Attempts  []wireAttempt `json:"attempts,omitempty"`
}

type wireAttempt struct {
	Provider  string    `json:"provider"`
	Model     string    `json:"model"`
	Type      Type      `json:"type"`
	Message   string    `json:"message,omitempty"`
	LimitType LimitType `json:"limit_type,omitempty"`
	Window    Window    `json:"window,omitempty"`
	ResetsAt  string    `json:"resets_at,omitempty"`
}

func formatTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Truncate(time.Second).Format(time.RFC3339)
}

// Body returns the JSON body, {"error": {...}}. It is the same object for a
// normal response and for an SSE event, so the UI parses one shape.
func (e *Error) Body() []byte {
	w := wireError{
		Message: e.Message, Type: e.Type, Code: e.Code, Param: e.Param,
		Provider: e.Provider, Model: e.Model,
		LimitType: e.LimitType, Window: e.Window, ResetsAt: formatTime(e.ResetsAt),
	}
	for _, a := range e.Attempts {
		w.Attempts = append(w.Attempts, wireAttempt{
			Provider: a.Provider, Model: a.Model, Type: a.Type, Message: a.Message,
			LimitType: a.LimitType, Window: a.Window, ResetsAt: formatTime(a.ResetsAt),
		})
	}
	b, err := json.Marshal(map[string]wireError{"error": w})
	if err != nil { // only strings in here, so this cannot happen
		return []byte(`{"error":{"message":"internal error","type":"provider_error","code":"provider_error"}}`)
	}
	return b
}

// SSE returns the error as one server-sent event, for an error that happens
// after a streamed response has started. By then the status line (200) and
// some tokens are gone, so the error travels in the stream as
//
//	data: {"error": {...}}
//
// plus a blank line: the object OpenAI itself sends mid-stream, and the same
// one Body returns. The caller writes the bytes, flushes, and ends the stream
// (whether to also send "data: [DONE]" is its call).
func (e *Error) SSE() []byte {
	return append(append([]byte("data: "), e.Body()...), '\n', '\n')
}

// Write sends e as a complete HTTP response: status, headers, body. It is the
// one way handlers return an error before any of the real response has been
// written; once a stream has started, use SSE instead.
func Write(w http.ResponseWriter, e *Error) { WriteAt(w, e, time.Now()) }

// WriteAt is Write with the current time given, so Retry-After is testable.
func WriteAt(w http.ResponseWriter, e *Error, now time.Time) {
	h := w.Header()
	h.Set("Content-Type", "application/json")
	h.Set("Cache-Control", "no-store")
	// Retry-After only where waiting is the cure, and only when we know how
	// long. The OpenAI SDKs honour it on 429/503 (when it is 60s or less)
	// instead of guessing a backoff.
	if (e.Type == TypeRateLimited || e.Type == TypeAllProvidersExhausted) && !e.ResetsAt.IsZero() {
		wait := e.ResetsAt.Sub(now)
		secs := int64(wait / time.Second)
		if time.Duration(secs)*time.Second < wait {
			secs++ // round up: retrying a moment early just fails again
		}
		if secs < 1 {
			secs = 1
		}
		h.Set("Retry-After", strconv.FormatInt(secs, 10))
	}
	status := e.Status
	if status == 0 {
		status = http.StatusInternalServerError
	}
	w.WriteHeader(status)
	w.Write(e.Body())
}

// Limit describes a limit that was hit. Provider and Model are empty for the
// service-key daily cap (#14), which belongs to the caller, not a provider.
// Everything except the fact of the limit is optional.
type Limit struct {
	Provider  string
	Model     string
	LimitType LimitType
	Window    Window
	ResetsAt  time.Time
}

// RateLimited builds a 429. The message names the limit as precisely as the
// fields allow ("groq/llama-3.3-70b hit its daily request limit."), and with
// no provider it reads as a limit on the caller, which is what the service-key
// cap is:
//
//	apierror.RateLimited(apierror.Limit{LimitType: apierror.LimitRequests,
//	    Window: apierror.WindowDay, ResetsAt: midnightUTC})
func RateLimited(l Limit) *Error {
	subject := "This API key has reached its"
	switch {
	case l.Provider != "" && l.Model != "":
		subject = l.Provider + "/" + l.Model + " hit its"
	case l.Provider != "":
		subject = l.Provider + " hit its"
	}
	msg := subject + " " + limitName(l.LimitType, l.Window) + "."
	if !l.ResetsAt.IsZero() {
		msg += " It resets at " + formatTime(l.ResetsAt) + "."
	}
	return &Error{
		Type: TypeRateLimited, Status: http.StatusTooManyRequests,
		// OpenAI's own code for 429s, which SDK users may already match on.
		Code: "rate_limit_exceeded", Message: msg,
		Provider: l.Provider, Model: l.Model,
		LimitType: l.LimitType, Window: l.Window, ResetsAt: l.ResetsAt,
	}
}

func limitName(t LimitType, w Window) string {
	var name string
	switch t {
	case LimitRequests:
		name = "request limit"
	case LimitTokens:
		name = "token limit"
	default:
		name = "rate limit"
	}
	switch w {
	case WindowMinute:
		return "per-minute " + name
	case WindowDay:
		return "daily " + name
	}
	return name
}

// ContextLengthExceeded builds a 400. OpenAI answers an over-long prompt with
// 400 and this same code, and the SDKs do not retry 4xx other than 408, 409
// and 429, which is right: the same prompt will never fit.
func ContextLengthExceeded(provider, model, message string) *Error {
	if message == "" {
		message = "The request is too long for this model's context window."
	}
	return &Error{
		Type: TypeContextLengthExceeded, Status: http.StatusBadRequest,
		Code: "context_length_exceeded", Message: message,
		Provider: provider, Model: model,
	}
}

// ProviderError builds a 502 Bad Gateway: the router is fine, the provider
// behind it failed. 502 (not 500) tells a client this is an upstream problem;
// the SDKs retry 5xx, which suits outages and timeouts. Provider and model may
// be empty.
func ProviderError(provider, model, message string) *Error {
	if message == "" {
		message = "The provider failed to answer."
	}
	return &Error{
		Type: TypeProviderError, Status: http.StatusBadGateway,
		Code: "provider_error", Message: message,
		Provider: provider, Model: model,
	}
}

// InvalidRequest builds a 400 for a request the client must change. param may
// be "" or the name of the offending field.
func InvalidRequest(message, param string) *Error {
	return &Error{
		Type: TypeInvalidRequest, Status: http.StatusBadRequest,
		Code: "invalid_request", Message: message, Param: param,
	}
}

// NotFound is a 404 for a path with no handler. It is an invalid_request
// because the client asked for something that does not exist.
func NotFound() *Error {
	return &Error{
		Type: TypeInvalidRequest, Status: http.StatusNotFound,
		Code: "not_found", Message: "No such endpoint.",
	}
}

// ModelNotFound is a 404 for a `model` that is neither a route nor a
// provider/model ID in the config. It mirrors OpenAI's answer to an unknown
// model (404, code model_not_found, param model), so SDK users get the same
// NotFoundError they would from OpenAI. The client's string is not echoed:
// messages only hold text from the router's own config.
func ModelNotFound() *Error {
	return &Error{
		Type: TypeInvalidRequest, Status: http.StatusNotFound,
		Code: "model_not_found", Param: "model",
		Message: "The requested model does not exist. GET /v1/models lists the models you can use.",
	}
}

// RequestTooLarge is a 413 for a request body over the router's size limit.
// It is the client's to fix (send smaller images, fewer messages), so the
// type is invalid_request; the SDKs do not retry 413.
func RequestTooLarge(limitBytes int64) *Error {
	return &Error{
		Type: TypeInvalidRequest, Status: http.StatusRequestEntityTooLarge,
		Code:    "request_too_large",
		Message: fmt.Sprintf("The request body is larger than the %d MiB limit.", limitBytes>>20),
	}
}

// MethodNotAllowed is a 405 for a known path used with the wrong method. The
// caller should also set the Allow header (Routes does).
func MethodNotAllowed() *Error {
	return &Error{
		Type: TypeInvalidRequest, Status: http.StatusMethodNotAllowed,
		Code: "method_not_allowed", Message: "This endpoint does not support that HTTP method.",
	}
}

// Unauthorized is a 401: no credential, or one that did not verify. The
// message should not say why a token failed (that helps forgers).
func Unauthorized(message string) *Error {
	return &Error{
		Type: TypeUnauthorized, Status: http.StatusUnauthorized,
		Code: "unauthorized", Message: message,
	}
}

// Forbidden is a 403: the caller is genuine but not allowed. There is no
// separate type for it, so it shares unauthorized and code tells them apart.
// The SDKs treat 403 as "permission denied" and do not retry it.
func Forbidden(code, message string) *Error {
	return &Error{
		Type: TypeUnauthorized, Status: http.StatusForbidden,
		Code: code, Message: message,
	}
}

// Unavailable is a 503 for the router's own fault, such as authentication not
// being configured. None of the six types means "the gateway is broken", so it
// uses provider_error (a server-side failure the client can't fix), with a
// code that says what.
func Unavailable(code, message string) *Error {
	return &Error{
		Type: TypeProviderError, Status: http.StatusServiceUnavailable,
		Code: code, Message: message,
	}
}

// AllProvidersExhausted builds the error for a virtual model whose every entry
// failed or was skipped. Keep the attempts in the order they were tried.
//
// Status is 429 when every attempt was a rate limit (the cure is to wait, and
// a 429 makes SDKs back off and honour Retry-After), otherwise 503 (the
// service can't answer right now, and nothing about the client's request is at
// fault; SDKs retry that too). With no attempts at all (no usable provider)
// it is 503.
//
// ResetsAt is the earliest reset among the attempts that know one: the first
// moment retrying could work. Retry-After uses it.
func AllProvidersExhausted(attempts []Attempt) *Error {
	status := http.StatusServiceUnavailable
	allLimited := len(attempts) > 0
	var earliest time.Time
	for _, a := range attempts {
		if a.Type != TypeRateLimited {
			allLimited = false
		}
		if !a.ResetsAt.IsZero() && (earliest.IsZero() || a.ResetsAt.Before(earliest)) {
			earliest = a.ResetsAt
		}
	}
	if allLimited {
		status = http.StatusTooManyRequests
	}
	msg := "No provider is available for this request."
	switch len(attempts) {
	case 0:
	case 1:
		msg = "The only provider available for this request failed or is out of quota. See attempts for the reason."
	default:
		msg = fmt.Sprintf("All %d providers tried for this request failed or are out of quota. See attempts for each reason.", len(attempts))
	}
	return &Error{
		Type: TypeAllProvidersExhausted, Status: status,
		Code: "all_providers_exhausted", Message: msg,
		ResetsAt: earliest, Attempts: attempts,
	}
}
