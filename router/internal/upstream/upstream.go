// Package upstream sends chat completion requests to OpenAI-compatible
// providers (Groq, OpenRouter, Gemini, Workers AI, Ollama) and turns their
// many ways of failing into one error type with a handful of kinds.
//
// It knows nothing about routes, fallback or key storage. The caller picks a
// provider and model, looks up the key, and hands over the client's request
// body; this package rewrites the model, sends it, and reports what happened.
// The upstream response headers are always returned (on success and failure)
// so the rate-limit tracker can read them.
//
// Caller cancellation is not a provider failure: when the caller's context is
// done, Complete, Stream and Stream.Next return the context's error (check
// with errors.Is(err, context.Canceled)), not an *Error, so a client hanging
// up never counts against a provider.
package upstream

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"
)

// Default timeouts, used when the matching Options field is zero.
const (
	DefaultConnectTimeout   = 10 * time.Second
	DefaultFirstByteTimeout = 60 * time.Second
	DefaultResponseTimeout  = 5 * time.Minute
)

// Size limits. A provider is trusted not to be malicious, but a broken proxy
// in front of one could send an endless body, and that must not eat the Pi's
// memory.
const (
	maxResponseBody = 32 << 20 // non-streaming success body
	maxErrorBody    = 64 << 10 // only the start of an error body is needed to classify it
)

// Options configures a Client. Zero values mean the defaults above.
type Options struct {
	// ConnectTimeout bounds the TCP connect and the TLS handshake.
	ConnectTimeout time.Duration
	// FirstByteTimeout bounds how long Stream waits for the response headers.
	// Once they arrive the stream has no time limit at all: a long answer can
	// take minutes, and the caller's context is what stops it.
	FirstByteTimeout time.Duration
	// ResponseTimeout bounds a whole non-streaming call. It has to be long,
	// because a non-streaming provider sends nothing (not even headers) until
	// the full answer is generated.
	ResponseTimeout time.Duration
}

// Client calls providers. It is safe for concurrent use; make one and share it
// so connections to each provider are reused.
type Client struct {
	http             *http.Client
	firstByteTimeout time.Duration
	responseTimeout  time.Duration
}

// New returns a Client.
func New(opts Options) *Client {
	if opts.ConnectTimeout <= 0 {
		opts.ConnectTimeout = DefaultConnectTimeout
	}
	if opts.FirstByteTimeout <= 0 {
		opts.FirstByteTimeout = DefaultFirstByteTimeout
	}
	if opts.ResponseTimeout <= 0 {
		opts.ResponseTimeout = DefaultResponseTimeout
	}
	t := http.DefaultTransport.(*http.Transport).Clone()
	t.DialContext = (&net.Dialer{Timeout: opts.ConnectTimeout, KeepAlive: 30 * time.Second}).DialContext
	t.TLSHandshakeTimeout = opts.ConnectTimeout
	t.MaxIdleConnsPerHost = 8
	return &Client{
		// No http.Client.Timeout: it would also cut off long streams. Time
		// limits are applied per call instead, through the request context.
		http:             &http.Client{Transport: t},
		firstByteTimeout: opts.FirstByteTimeout,
		responseTimeout:  opts.ResponseTimeout,
	}
}

// Request is one call to one provider.
type Request struct {
	// Provider is the provider's name from providers.yaml, used in errors.
	Provider string
	// BaseURL is config.Provider.BaseURL (no trailing slash). The request goes
	// to BaseURL + "/chat/completions".
	BaseURL string
	// Model is the provider's own model ID (config.Model.ID, e.g.
	// "llama-3.3-70b-versatile"). It replaces whatever `model` the client
	// sent, which may be a route like "fast" or a "groq/..." ID.
	Model string
	// APIKey is sent as "Authorization: Bearer <key>". Leave it empty for
	// keyless local providers (Ollama) and no Authorization header is sent.
	// It never appears in errors.
	APIKey string
	// Body is the client's OpenAI-format JSON request body, as received. Only
	// `model`, `stream` and `stream_options` are changed; every other
	// member keeps its value exactly (insignificant whitespace may be
	// compacted and member order may change).
	Body []byte
}

// Response is a successful non-streaming completion.
type Response struct {
	Status int
	Header http.Header // upstream response headers, e.g. x-ratelimit-*
	Body   []byte      // the provider's JSON, unmodified
}

// Complete sends a non-streaming request and reads the whole response.
//
// `stream` is forced to false and `stream_options` is removed (OpenAI-style
// APIs reject stream_options on a non-streaming request).
//
// On failure the error is an *Error, or the context's error if ctx is done.
// A 200 response whose body is an error object (OpenRouter does this when
// generation fails after starting) is reported as an *Error too.
func (c *Client) Complete(ctx context.Context, req Request) (*Response, error) {
	body, _, err := rewriteBody(req.Body, req.Model, false, false)
	if err != nil {
		return nil, req.errorf(KindBadRequest, 0, nil, err, "request body: %v", err)
	}

	// The whole call, body included, is bounded here. Unlike a stream, a
	// non-streaming answer is useless if it never finishes.
	cctx, cancel := context.WithTimeoutCause(ctx, c.responseTimeout, errResponseTimeout)
	defer cancel()

	resp, err := c.do(cctx, ctx, req, body, false)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return nil, req.statusError(resp)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBody+1))
	if err != nil {
		return nil, req.transportError(cctx, ctx, resp.Header, err)
	}
	if len(data) > maxResponseBody {
		return nil, req.errorf(KindProvider, resp.StatusCode, resp.Header, nil, "response body larger than %d bytes", maxResponseBody)
	}
	if e := req.bodyError(data, resp.StatusCode, resp.Header); e != nil {
		return nil, e
	}
	return &Response{Status: resp.StatusCode, Header: resp.Header, Body: data}, nil
}

// Stream sends a streaming request and returns once the provider has answered
// with response headers. If Stream returns an error, nothing has been read
// from the provider's body, so the caller has sent nothing to its own client
// and can safely fall back to another provider. If it succeeds, read events
// with Stream.Next and always call Stream.Close.
//
// `stream` is forced to true. Unless the client set `stream_options` itself,
// {"include_usage": true} is added so the last chunk carries token counts for
// the request log; Stream.UsageAdded reports this, so a caller can drop that
// usage-only chunk (it has an empty `choices` array) for a client that never
// asked for it. If a provider rejects stream_options with a 400 that names it,
// the request is retried once without it.
func (c *Client) Stream(ctx context.Context, req Request) (*Stream, error) {
	body, added, err := rewriteBody(req.Body, req.Model, true, true)
	if err != nil {
		return nil, req.errorf(KindBadRequest, 0, nil, err, "request body: %v", err)
	}
	s, err := c.openStream(ctx, req, body, added)
	var ue *Error
	if added && errors.As(err, &ue) && ue.Kind == KindBadRequest && strings.Contains(strings.ToLower(ue.Message), "stream_options") {
		body, _, _ = rewriteBody(req.Body, req.Model, true, false)
		s, err = c.openStream(ctx, req, body, false)
	}
	return s, err
}

func (c *Client) openStream(ctx context.Context, req Request, body []byte, added bool) (*Stream, error) {
	// The first-byte timer is cancelled as soon as headers arrive; after that
	// only the caller's context can stop the stream.
	sctx, cancel := context.WithCancelCause(ctx)
	timer := time.AfterFunc(c.firstByteTimeout, func() { cancel(errFirstByteTimeout) })

	resp, err := c.do(sctx, ctx, req, body, true)
	if !timer.Stop() && err == nil {
		// The timer fired just as the headers arrived; the body is already
		// cancelled, so this is a timeout after all.
		resp.Body.Close()
		err = req.transportError(sctx, ctx, nil, context.Cause(sctx))
	}
	if err != nil {
		cancel(nil)
		return nil, err
	}

	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		defer cancel(nil)
		defer resp.Body.Close()
		return nil, req.statusError(resp)
	}
	// A provider that ignores `stream` and answers with plain JSON (usually an
	// error object) is handled here, still before anything is forwarded.
	if strings.HasPrefix(resp.Header.Get("Content-Type"), "application/json") {
		defer cancel(nil)
		defer resp.Body.Close()
		data, _ := io.ReadAll(io.LimitReader(resp.Body, maxErrorBody))
		if e := req.bodyError(data, resp.StatusCode, resp.Header); e != nil {
			return nil, e
		}
		return nil, req.errorf(KindProvider, resp.StatusCode, resp.Header, nil, "expected an event stream, got JSON")
	}

	return &Stream{
		Status:     resp.StatusCode,
		Header:     resp.Header,
		UsageAdded: added,
		req:        req,
		ctx:        sctx,
		parent:     ctx,
		cancel:     cancel,
		body:       resp.Body,
		events:     newEventReader(resp.Body),
	}, nil
}

// do sends the request. ctx carries this call's time limits; parent is the
// caller's own context, used to tell "caller gave up" from "provider timed out".
func (c *Client) do(ctx, parent context.Context, req Request, body []byte, stream bool) (*http.Response, error) {
	hreq, err := http.NewRequestWithContext(ctx, http.MethodPost, req.BaseURL+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		// The URL comes from validated config, so this is a config problem.
		return nil, req.errorf(KindProvider, 0, nil, err, "build request: %v", redact(err.Error(), req.APIKey))
	}
	hreq.Header.Set("Content-Type", "application/json")
	if stream {
		hreq.Header.Set("Accept", "text/event-stream")
		// Compression makes some servers and proxies buffer the stream, which
		// would delay tokens. Setting this also stops Go adding gzip itself.
		hreq.Header.Set("Accept-Encoding", "identity")
	} else {
		hreq.Header.Set("Accept", "application/json")
	}
	if req.APIKey != "" {
		hreq.Header.Set("Authorization", "Bearer "+req.APIKey)
	}
	resp, err := c.http.Do(hreq)
	if err != nil {
		return nil, req.transportError(ctx, parent, nil, err)
	}
	return resp, nil
}

var (
	errFirstByteTimeout = errors.New("no response headers before the first-byte timeout")
	errResponseTimeout  = errors.New("no complete response before the response timeout")
)

// transportError classifies a failure with no usable HTTP response: the
// caller cancelling, one of our timeouts, or a network error.
func (r Request) transportError(ctx, parent context.Context, h http.Header, err error) error {
	if parent.Err() != nil {
		return fmt.Errorf("upstream %s: %w", r.Provider, parent.Err())
	}
	if cause := context.Cause(ctx); cause == errFirstByteTimeout || cause == errResponseTimeout {
		return r.errorf(KindProvider, 0, h, cause, "timeout: %v", cause)
	}
	var ne net.Error
	if errors.As(err, &ne) && ne.Timeout() {
		return r.errorf(KindProvider, 0, h, err, "connect timeout")
	}
	// *url.Error messages include the URL and the cause (e.g. "connection
	// refused"). The URL never holds the key, but redact anyway in case a
	// base_url was misconfigured with one in it.
	return r.errorf(KindProvider, 0, h, err, "network error: %s", redact(err.Error(), r.APIKey))
}

// statusError reads (the start of) a non-2xx response and classifies it.
func (r Request) statusError(resp *http.Response) *Error {
	data, _ := io.ReadAll(io.LimitReader(resp.Body, maxErrorBody))
	d := parseErrorBody(data)
	return r.classify(resp.StatusCode, resp.StatusCode, resp.Header, d, false)
}

// bodyError reports an error object in a 2xx body, or nil if there is none.
func (r Request) bodyError(data []byte, status int, h http.Header) *Error {
	d, ok := parseInlineError(data)
	if !ok {
		return nil
	}
	return r.classify(status, d.effectiveStatus(status), h, d, true)
}
