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
	// first is the first event to forward, already read from stream, or nil
	// when the stream ended without one. Reading it before answering is what
	// makes "fall back only before the first token" possible: a stream that
	// fails before producing anything is still an attempt error.
	first []byte
	// usage is token counts seen so far (from a hidden usage chunk read
	// while looking for first), or nil.
	usage *Usage
}

// attempt makes one call to one target. On success the answer is ready to
// write and nothing has been sent to the client. On failure the error is an
// *upstream.Error (the provider failed; another target might work) or the
// context's error (the client went away); either way the client has been sent
// nothing, so the caller can still answer with a normal error response or
// try another target.
func (h *chatHandler) attempt(ctx context.Context, t target, body []byte, stream bool) (*answer, error) {
	req := upstream.Request{
		Provider: t.provider.Name,
		BaseURL:  t.provider.BaseURL,
		Model:    t.model.ID,
		Body:     body,
	}
	if !t.provider.Local {
		key, ok := h.Key(t.provider.Name)
		if !ok {
			// The provider was enabled when resolved but its key is gone (or
			// the key lookup isn't wired yet). Reported like a provider
			// failure, so fallback moves on; no call was made, so the
			// observer is not told.
			return nil, &upstream.Error{Kind: upstream.KindProvider, Provider: req.Provider, Model: req.Model,
				Message: "no API key is registered for this provider"}
		}
		req.APIKey = key
	}

	if !stream {
		resp, err := h.Client.Complete(ctx, req)
		if err != nil {
			h.observeErr(t, err)
			return nil, err
		}
		h.Observer.ObserveUpstream(req.Provider, req.Model, resp.Header, nil)
		return &answer{target: t, resp: resp}, nil
	}

	s, err := h.Client.Stream(ctx, req)
	if err != nil {
		h.observeErr(t, err)
		return nil, err
	}
	h.Observer.ObserveUpstream(req.Provider, req.Model, s.Header, nil)

	ans := &answer{target: t, stream: s}
	first, usage, err := nextEvent(s)
	ans.usage = usage
	switch {
	case err == io.EOF:
		// An empty but well-formed stream: the writer sends just [DONE].
		return ans, nil
	case err != nil:
		s.Close()
		h.observeErr(t, err)
		return nil, err
	}
	ans.first = first
	return ans, nil
}

// observeErr passes a provider failure to the observer. A client hanging up
// is not the provider's doing, so it isn't passed on.
func (h *chatHandler) observeErr(t target, err error) {
	var ue *upstream.Error
	if errors.As(err, &ue) {
		h.Observer.ObserveUpstream(t.provider.Name, t.model.ID, ue.Header, ue)
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
