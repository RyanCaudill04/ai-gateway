package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"

	"github.com/RyanCaudill04/ai-gateway/router/internal/upstream"
)

// answer is a provider's successful reply that nothing has been written for
// yet. Exactly one of resp and stream is set.
type answer struct {
	target target
	resp   *upstream.Response // non-streaming

	stream *upstream.Stream // streaming; the writer must Close it
	// first is the first event to forward, already read from stream. Reading
	// it before answering is what makes "fall back only before the first
	// token" possible: a stream that fails before producing anything is still
	// an attempt error, and the client has seen nothing of it.
	first []byte
	// usage is token counts seen so far (from a hidden usage chunk read
	// while looking for first), or nil.
	usage *Usage
}

// upstreamRequest builds the call to t. It is the only place a request to a
// provider is put together, so a rule about where a request may go has one
// place to stand.
//
// It fails, without calling anyone, when t's provider needs a key and none is
// registered: the provider was enabled when the model was resolved but its
// key has gone since (or the key lookup isn't wired). That is reported like a
// provider failure, so a route moves on to its next entry.
func (h *chatHandler) upstreamRequest(t target, body []byte) (upstream.Request, *upstream.Error) {
	req := upstream.Request{
		Provider: t.provider.Name,
		BaseURL:  t.provider.BaseURL,
		Model:    t.model.ID,
		Body:     body,
	}
	if !t.provider.Local {
		key, ok := h.Key(t.provider.Name)
		if !ok {
			return req, &upstream.Error{Kind: upstream.KindProvider, Provider: req.Provider, Model: req.Model,
				Message: "no API key is registered for this provider"}
		}
		req.APIKey = key
	}
	return req, nil
}

// attempt makes one call to one target. On success the answer is ready to
// write and nothing has been sent to the client. On failure the error is an
// *upstream.Error (the provider failed; another target might work) or the
// context's error (the client went away); either way the client has been sent
// nothing, so the caller can still answer with a normal error response or
// try another target.
//
// The rate-limit tracker is told the outcome before attempt returns, so the
// caller can ask it straight away what it knows about a failure.
func (h *chatHandler) attempt(ctx context.Context, t target, req upstream.Request, stream bool) (*answer, error) {
	if !stream {
		resp, err := h.Client.Complete(ctx, req)
		if err != nil {
			h.observeErr(err)
			return nil, err
		}
		h.Limits.Observe(req.Provider, req.Model, resp.Header, h.Now())
		return &answer{target: t, resp: resp}, nil
	}

	s, err := h.Client.Stream(ctx, req)
	if err != nil {
		h.observeErr(err)
		return nil, err
	}
	h.Limits.Observe(req.Provider, req.Model, s.Header, h.Now())

	first, usage, err := nextEvent(s)
	if err == io.EOF {
		// A 200 that ends without a single chunk is not an answer: a real
		// completion always has at least one. It is treated like the
		// connection dropping before the first event, so a route can still
		// fall back instead of handing the client an empty reply.
		err = &upstream.Error{Kind: upstream.KindProvider, Provider: req.Provider, Model: req.Model,
			Status: s.Status, InStream: true, Header: s.Header,
			Message: "the provider ended the stream without sending anything"}
	}
	if err != nil {
		s.Close()
		h.observeErr(err)
		return nil, err
	}
	return &answer{target: t, stream: s, first: first, usage: usage}, nil
}

// observeErr passes a provider failure to the rate-limit tracker. A client
// hanging up is not the provider's doing, so it isn't passed on.
func (h *chatHandler) observeErr(err error) {
	var ue *upstream.Error
	if errors.As(err, &ue) {
		h.Limits.ObserveError(ue, h.Now())
	}
}

// nextEvent returns the next event to forward to the client, skipping the
// usage-only chunk the upstream client asked for on the client's behalf
// (Stream.UsageAdded). Token counts from any chunk that has them are
// returned in usage (nil if this read saw none), whether or not that chunk
// is forwarded. At the end of the stream err is io.EOF.
func nextEvent(s *upstream.Stream) (data []byte, usage *Usage, err error) {
	for {
		data, err := s.Next()
		if err != nil {
			return nil, usage, err
		}
		u, usageOnly := parseChunkUsage(data)
		if u != nil {
			usage = u
		}
		if usageOnly && s.UsageAdded {
			continue // the client never asked for it
		}
		return data, usage, nil
	}
}

// parseChunkUsage reads token counts from a stream chunk or a non-streaming
// body. usageOnly is true for the final chunk include_usage produces: a
// `usage` object and no choices. Only these numbers are read; the content is
// never looked at.
func parseChunkUsage(data []byte) (u *Usage, usageOnly bool) {
	if !bytes.Contains(data, []byte(`"usage"`)) {
		return nil, false // fast path: most chunks carry no usage
	}
	var v struct {
		Choices []json.RawMessage `json:"choices"`
		Usage   *struct {
			PromptTokens     int `json:"prompt_tokens"`
			CompletionTokens int `json:"completion_tokens"`
		} `json:"usage"`
	}
	if json.Unmarshal(data, &v) != nil || v.Usage == nil {
		return nil, false
	}
	return &Usage{PromptTokens: v.Usage.PromptTokens, CompletionTokens: v.Usage.CompletionTokens}, len(v.Choices) == 0
}
