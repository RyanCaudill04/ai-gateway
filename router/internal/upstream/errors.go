package upstream

import (
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"
)

// Kind is what went wrong, in the terms the router acts on. The string values
// are stable and short enough to store as request_log.error_type.
type Kind string

const (
	// KindRateLimited: the provider said 429. Try another provider now, and
	// this one again after Error.RetryAfter (if known).
	KindRateLimited Kind = "rate_limited"
	// KindContextTooLong: the request is too big for this model. Another
	// model with a larger context window may accept it; retrying this one
	// will not help.
	KindContextTooLong Kind = "context_too_long"
	// KindBadRequest: the provider rejected the request itself (400/422, or
	// a body that isn't a JSON object). It is the client's to fix, and
	// another provider would most likely reject it too.
	KindBadRequest Kind = "bad_request"
	// KindProvider: the provider failed — 5xx, a timeout, a network error, a
	// rejected provider key (401/403), or any status not listed above. It is
	// not the client's fault, so falling back to another provider makes sense.
	KindProvider Kind = "provider_error"
)

// Error is a failed upstream call. Callers inspect it with errors.As:
//
//	var ue *upstream.Error
//	if errors.As(err, &ue) && ue.Kind == upstream.KindRateLimited { ... }
//
// Every field is safe to log and to show to the client: the provider key is
// never included, and Message is built from the upstream body with any copy
// of the key removed.
type Error struct {
	Kind     Kind
	Provider string
	Model    string
	// Status is the upstream HTTP status, or 0 when no response arrived
	// (network error, timeout). For an error sent inside a 200 stream or body,
	// it is 200 and InStream is true.
	Status int
	// InStream is true when the provider answered 200 and then reported the
	// error inside the body or event stream. For a stream, the caller may have
	// already forwarded tokens, so it can't always fall back.
	InStream bool
	// Message is a short, human-readable reason (at most a few hundred bytes).
	Message string
	// RetryAfter is how long the provider asked us to wait, from Retry-After,
	// x-ratelimit-reset* headers, or a Gemini RetryInfo detail. Zero means the
	// response didn't say. Only set for KindRateLimited.
	RetryAfter time.Duration
	// Header holds the upstream response headers, or nil if there was no
	// response. The rate-limit tracker reads x-ratelimit-* from it.
	Header http.Header
	// Err is the underlying cause for network and timeout errors.
	Err error
}

func (e *Error) Error() string {
	s := fmt.Sprintf("upstream %s/%s: %s", e.Provider, e.Model, e.Kind)
	if e.Status != 0 {
		s += fmt.Sprintf(" (HTTP %d)", e.Status)
	}
	if e.Message != "" {
		s += ": " + e.Message
	}
	return s
}

func (e *Error) Unwrap() error { return e.Err }

func (r Request) errorf(kind Kind, status int, h http.Header, cause error, format string, args ...any) *Error {
	return &Error{
		Kind:     kind,
		Provider: r.Provider,
		Model:    r.Model,
		Status:   status,
		Message:  safeMessage(fmt.Sprintf(format, args...), r.APIKey),
		Header:   h,
		Err:      cause,
	}
}

// errorDetail is what can be pulled out of a provider's error body, whatever
// its shape.
type errorDetail struct {
	message string
	codes   []string // error.code, error.type, error.status, metadata.error_type …
	numeric int      // a numeric error.code (OpenRouter), 0 if none
	retry   time.Duration
}

// effectiveStatus is the status to classify an in-body error by. OpenRouter
// puts the real status (e.g. 429) in error.code under an HTTP 200.
func (d errorDetail) effectiveStatus(httpStatus int) int {
	if d.numeric >= 400 && d.numeric <= 599 {
		return d.numeric
	}
	if httpStatus >= 200 && httpStatus <= 299 {
		return 0 // unknown: classify by text, else provider error
	}
	return httpStatus
}

// apiError covers the error object shapes seen across providers:
//
//	OpenAI / Groq:  {"error": {"message", "type", "code": "context_length_exceeded", "param"}}
//	OpenRouter:     {"error": {"code": 400, "message", "metadata": {"error_type": "..."}}}
//	Gemini:         [{"error": {"code": 400, "message", "status": "INVALID_ARGUMENT", "details": [...]}}]
//	Ollama:         {"error": {"message", "type"}} or {"error": "message"}
//	Cloudflare API: {"success": false, "errors": [{"code": 3036, "message"}]}
type apiError struct {
	Message  string          `json:"message"`
	Type     string          `json:"type"`
	Status   string          `json:"status"`
	Code     json.RawMessage `json:"code"` // string (OpenAI) or number (OpenRouter, Gemini)
	Metadata struct {
		ErrorType string `json:"error_type"`
		Raw       string `json:"raw"`
	} `json:"metadata"`
	Details []struct {
		Type       string `json:"@type"`
		RetryDelay string `json:"retryDelay"`
	} `json:"details"`
}

type errorEnvelope struct {
	Error  json.RawMessage `json:"error"`
	Errors []apiError      `json:"errors"`
}

// parseErrorBody extracts what it can from an error body. Non-JSON bodies
// (an HTML page from a proxy, plain text) become the message as-is.
func parseErrorBody(data []byte) errorDetail {
	trimmed := strings.TrimSpace(string(data))
	if strings.HasPrefix(trimmed, "[") { // Gemini wraps the object in an array
		var arr []json.RawMessage
		if json.Unmarshal(data, &arr) == nil && len(arr) > 0 {
			data = arr[0]
		}
	}
	if d, ok := parseInlineError(data); ok {
		return d
	}
	var env errorEnvelope
	if json.Unmarshal(data, &env) == nil && len(env.Errors) > 0 {
		return detailOf(env.Errors[0])
	}
	if strings.HasPrefix(trimmed, "<") {
		return errorDetail{} // an HTML error page says nothing useful
	}
	return errorDetail{message: trimmed}
}

// parseInlineError looks for a top-level "error" member (object or string).
// It is also used on 2xx bodies and stream events, where its absence is normal.
func parseInlineError(data []byte) (errorDetail, bool) {
	if !strings.Contains(string(data), `"error"`) {
		return errorDetail{}, false // fast path for ordinary stream chunks
	}
	var env errorEnvelope
	if json.Unmarshal(data, &env) != nil || len(env.Error) == 0 || string(env.Error) == "null" {
		return errorDetail{}, false
	}
	var s string
	if json.Unmarshal(env.Error, &s) == nil {
		return errorDetail{message: s}, true
	}
	var e apiError
	if json.Unmarshal(env.Error, &e) != nil {
		return errorDetail{}, false
	}
	return detailOf(e), true
}

func detailOf(e apiError) errorDetail {
	d := errorDetail{message: e.Message}
	if e.Metadata.Raw != "" && d.message == "" {
		d.message = e.Metadata.Raw
	}
	var codeStr string
	if json.Unmarshal(e.Code, &codeStr) == nil {
		d.codes = append(d.codes, codeStr)
	} else if n, err := strconv.Atoi(strings.TrimSpace(string(e.Code))); err == nil {
		d.numeric = n
	}
	d.codes = append(d.codes, e.Type, e.Status, e.Metadata.ErrorType)
	for _, det := range e.Details {
		if strings.HasSuffix(det.Type, "RetryInfo") {
			if dur, err := time.ParseDuration(det.RetryDelay); err == nil {
				d.retry = dur
			}
		}
	}
	return d
}

// classify turns a status and error body into an *Error. httpStatus is what
// goes in Error.Status; status is what to classify by (they differ for errors
// sent inside a 200).
func (r Request) classify(httpStatus, status int, h http.Header, d errorDetail, inStream bool) *Error {
	kind := KindProvider
	switch {
	case status == http.StatusTooManyRequests:
		// First, so a rate-limit message that happens to mention tokens is
		// never mistaken for an over-long prompt.
		kind = KindRateLimited
	case isContextTooLong(d):
		// Checked before the other statuses: providers send this as 400, 413,
		// sometimes 500, or inside a 200 stream, and it always means "try a
		// model with a bigger context window".
		kind = KindContextTooLong
	case status == http.StatusRequestEntityTooLarge:
		// Groq answers 413 when one request exceeds the per-minute token
		// limit, Workers AI when the body is too large. Either way this
		// provider can never take this request, which is what
		// KindContextTooLong tells the caller.
		kind = KindContextTooLong
	case isKeyRejected(d):
		// Gemini reports a bad key as 400 INVALID_ARGUMENT; that's our
		// config, not the client's request.
		kind = KindProvider
	case status == http.StatusBadRequest || status == http.StatusUnprocessableEntity:
		kind = KindBadRequest
	}

	msg := d.message
	if msg == "" {
		msg = http.StatusText(status)
	}
	if status == http.StatusUnauthorized || status == http.StatusForbidden || isKeyRejected(d) {
		msg = "provider rejected the API key or denied access: " + msg
	}
	e := &Error{
		Kind:     kind,
		Provider: r.Provider,
		Model:    r.Model,
		Status:   httpStatus,
		InStream: inStream,
		Message:  safeMessage(msg, r.APIKey),
		Header:   h,
	}
	if kind == KindRateLimited {
		e.RetryAfter = retryAfter(h, time.Now())
		if e.RetryAfter == 0 {
			e.RetryAfter = d.retry
		}
	}
	return e
}

// contextPhrases are lower-case fragments of the messages providers use for
// an over-long prompt. Sources are in docs/providers.md. Several of these are
// only user-reported (and Workers AI publishes none), so the list errs on the
// side of matching: a wrong "context too long" still falls back to another
// model, which is what a provider error would do too.
var contextPhrases = []string{
	"context_length_exceeded",           // OpenAI-style code, also in some messages
	"context length",                    // OpenAI "maximum context length is", Ollama
	"context window",                    // Workers AI (unverified), Anthropic via OpenRouter
	"reduce the length of the messages", // Groq
	"input token count",                 // Gemini "The input token count (N) exceeds…"
	"model only supports up to",         // Gemini on Vertex/OpenRouter
	"prompt is too long",                // Anthropic via OpenRouter
	"input is too long",                 // Workers AI (unverified)
	"too many tokens",
}

func isContextTooLong(d errorDetail) bool {
	for _, c := range d.codes {
		if strings.EqualFold(c, "context_length_exceeded") {
			return true
		}
	}
	m := strings.ToLower(d.message)
	for _, p := range contextPhrases {
		if strings.Contains(m, p) {
			return true
		}
	}
	return false
}

func isKeyRejected(d errorDetail) bool {
	for _, c := range d.codes {
		if strings.EqualFold(c, "API_KEY_INVALID") || strings.EqualFold(c, "invalid_api_key") {
			return true
		}
	}
	m := strings.ToLower(d.message)
	return strings.Contains(m, "api key not valid") || strings.Contains(m, "invalid api key")
}

// retryAfter reads how long to wait from the response headers. Retry-After
// (seconds or an HTTP date) wins. Otherwise: OpenRouter's X-RateLimit-Reset
// (Unix milliseconds), then Groq/OpenAI-style x-ratelimit-reset-requests /
// -tokens (Go-style durations like "2m59.56s"), taking the longest of the
// limits that are exhausted.
func retryAfter(h http.Header, now time.Time) time.Duration {
	if h == nil {
		return 0
	}
	if v := strings.TrimSpace(h.Get("Retry-After")); v != "" {
		if secs, err := strconv.ParseFloat(v, 64); err == nil && secs > 0 {
			return time.Duration(secs * float64(time.Second))
		}
		if t, err := http.ParseTime(v); err == nil && t.After(now) {
			return t.Sub(now)
		}
	}
	if v := h.Get("X-RateLimit-Reset"); v != "" {
		if ms, err := strconv.ParseInt(v, 10, 64); err == nil {
			if t := time.UnixMilli(ms); t.After(now) {
				return t.Sub(now)
			}
		}
	}
	var longest time.Duration
	for _, kind := range []string{"requests", "tokens"} {
		reset := h.Get("X-Ratelimit-Reset-" + kind)
		if reset == "" || strings.TrimSpace(h.Get("X-Ratelimit-Remaining-"+kind)) != "0" {
			continue
		}
		if d, err := time.ParseDuration(reset); err == nil && d > longest {
			longest = d
		}
	}
	return longest
}

// keyLike matches strings shaped like provider API keys (gsk_…, sk-or-…,
// AIza…, long bearer tokens), as a second line of defence after removing the
// exact key: a provider echoing a *different* key we hold must not leak it.
var keyLike = regexp.MustCompile(`(?i)(bearer\s+\S+|\b(sk|gsk|sk-or|xai|ghp|github_pat)[-_][A-Za-z0-9_\-]{12,}|\bAIza[0-9A-Za-z_\-]{20,})`)

const maxMessage = 300

// safeMessage makes an upstream-provided message safe to log and return: the
// key and anything key-shaped are removed, control characters dropped, and the
// length capped.
func safeMessage(msg, key string) string {
	msg = redact(msg, key)
	msg = keyLike.ReplaceAllString(msg, "[redacted]")
	msg = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, msg)
	msg = strings.Join(strings.Fields(msg), " ")
	if len(msg) > maxMessage {
		cut := maxMessage
		for cut > 0 && !utf8Start(msg[cut]) {
			cut--
		}
		msg = msg[:cut] + "…"
	}
	return msg
}

func redact(s, key string) string {
	if key == "" {
		return s
	}
	return strings.ReplaceAll(s, key, "[redacted]")
}

func utf8Start(b byte) bool { return b&0xC0 != 0x80 }
