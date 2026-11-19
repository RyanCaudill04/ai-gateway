package api

import (
	"net/http"
	"strings"

	"github.com/RyanCaudill04/ai-gateway/router/internal/apierror"
)

// FallbackAttempt is one entry of a virtual model that did not answer a
// request. It is what the x-router-fallback header lists and what the request
// log (#15) can store about fallback. It holds names and a reason, never
// anything from the request or a response.
type FallbackAttempt struct {
	Provider string
	Model    string // the provider's own model ID
	// Reason is why the entry did not answer: one of the Reason constants.
	Reason string
	// Called is true when an upstream request was made and failed. False
	// means the entry cost nothing: it was skipped (known to be out of
	// quota, or too small for the prompt) or had no key to call with.
	Called bool
}

// The reasons an entry did not answer. They are the words of the error
// format's types (docs/api.md), with the window added to a rate limit when it
// is known. The set is closed: the UI switches on it.
const (
	// The provider answered 429, or the router already knew the model was out
	// of quota and did not call it. Which window ran out is not known.
	ReasonRateLimited = "rate_limited"
	// A per-minute limit: worth trying again shortly.
	ReasonRateLimitedMinute = "rate_limited_minute"
	// A per-day limit: gone until tomorrow's reset.
	ReasonRateLimitedDay = "rate_limited_day"
	// The prompt is too long for the model, as the provider said, or as a
	// model at least as large had already said.
	ReasonContextLengthExceeded = "context_length_exceeded"
	// The provider rejected the request itself (400/422).
	ReasonInvalidRequest = "invalid_request"
	// The provider failed: 5xx, timeout, network error, a rejected or missing
	// provider key.
	ReasonProviderError = "provider_error"
)

// reasonOf maps an attempt of the error body to its header reason, so the
// two always tell the same story.
func reasonOf(a apierror.Attempt) string {
	switch a.Type {
	case apierror.TypeRateLimited:
		switch a.Window {
		case apierror.WindowMinute:
			return ReasonRateLimitedMinute
		case apierror.WindowDay:
			return ReasonRateLimitedDay
		}
		return ReasonRateLimited
	case apierror.TypeContextLengthExceeded:
		return ReasonContextLengthExceeded
	case apierror.TypeInvalidRequest:
		return ReasonInvalidRequest
	}
	return ReasonProviderError
}

// FallbackHeader formats attempts as the value of x-router-fallback, or ""
// when there are none (the header is then left out).
//
//	value   = attempt *( ", " attempt )       in the order they were tried
//	attempt = provider "/" model "=" reason
//	reason  = one of the Reason constants     lower-case letters and "_"
//
// for example
//
//	groq/openai/gpt-oss-20b=rate_limited_day, gemini/gemini-3.5-flash-lite=provider_error
//
// Model IDs contain "/", ":" and "@" ("openai/gpt-oss-20b",
// "llama3.2:3b", "@cf/meta/llama-3.1-8b-instruct"), so those are written as
// they are and the value stays readable. Every other byte outside
// A-Z a-z 0-9 - . _ ~ is percent-encoded (%2C for a comma, UTF-8 bytes for
// anything non-ASCII). That keeps the three separators unambiguous and the
// value plain visible ASCII, which is all an HTTP header may safely carry.
//
// To read it back: split at ","; trim spaces; split each item at its only
// "="; split the left side at its FIRST "/" (provider names cannot contain
// one, model IDs can); percent-decode the provider and the model.
func FallbackHeader(attempts []FallbackAttempt) string {
	var b strings.Builder
	for i, a := range attempts {
		if i > 0 {
			b.WriteString(", ")
		}
		escapeTo(&b, a.Provider, false)
		b.WriteByte('/')
		escapeTo(&b, a.Model, true)
		b.WriteByte('=')
		b.WriteString(a.Reason)
	}
	return b.String()
}

// escapeTo writes s with every byte that could be mistaken for a separator,
// or that a header should not carry, percent-encoded. A provider name must
// not contribute a "/" (the first one ends it), a model ID may.
func escapeTo(b *strings.Builder, s string, slashOK bool) {
	const hex = "0123456789ABCDEF"
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9',
			c == '-', c == '.', c == '_', c == '~', c == ':', c == '@',
			c == '/' && slashOK:
			b.WriteByte(c)
		default:
			b.WriteByte('%')
			b.WriteByte(hex[c>>4])
			b.WriteByte(hex[c&0x0F])
		}
	}
}

// describe puts what dispatch found into the response headers and the
// request summary. It must run before anything is written.
//
// x-router-provider and x-router-model name the entry that answered, or the
// last one called when none did. x-router-fallback lists every route entry
// that did not answer; it is absent when there was none.
func (o *outcome) describe(w http.ResponseWriter, sum *RequestSummary) {
	if o.last != nil {
		setRouterHeaders(w, *o.last, sum)
	}
	sum.Attempts = o.calls
	for _, f := range o.failed {
		sum.Fallback = append(sum.Fallback, FallbackAttempt{
			Provider: f.attempt.Provider, Model: f.attempt.Model,
			Reason: reasonOf(f.attempt), Called: f.called,
		})
	}
	if v := FallbackHeader(sum.Fallback); v != "" {
		w.Header().Set("X-Router-Fallback", v)
	}
}
