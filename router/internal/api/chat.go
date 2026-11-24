package api

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"github.com/RyanCaudill04/ai-gateway/router/internal/apierror"
	"github.com/RyanCaudill04/ai-gateway/router/internal/config"
	"github.com/RyanCaudill04/ai-gateway/router/internal/upstream"
)

// maxChatBody caps a request body so a huge upload can't eat the Pi's memory.
const maxChatBody = 8 << 20

// KeyFunc returns the API key for a provider ("" for keyless local ones).
// Taking a function (not reading the environment here) lets the encrypted key
// store (#40) replace the source without touching the handler.
type KeyFunc func(provider string) string

// ChatCompletions serves POST /v1/chat/completions for a pinned model such as
// "groq/llama-3.3-70b-versatile". Routes and fallback come later;
// for now it forwards the body to that one provider and returns its answer
// (as one JSON body, or as server-sent events when the request sets stream).
func ChatCompletions(cfg *config.Config, client *upstream.Client, enabled EnabledFunc, key KeyFunc) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxChatBody))
		if err != nil {
			apierror.Write(w, apierror.InvalidRequest("Could not read the request body.", ""))
			return
		}

		// Decode only what the router needs. The original bytes are forwarded,
		// so every other field reaches the provider untouched.
		var req struct {
			Model  string `json:"model"`
			Stream bool   `json:"stream"`
		}
		if err := json.Unmarshal(body, &req); err != nil {
			apierror.Write(w, apierror.InvalidRequest("The request body is not valid JSON.", ""))
			return
		}
		p, m, ok := cfg.Lookup(req.Model)
		if !ok {
			apierror.Write(w, apierror.InvalidRequest("Unknown model. Use provider/model, e.g. groq/llama-3.3-70b-versatile.", "model"))
			return
		}
		if !enabled(p.Name) {
			apierror.Write(w, apierror.Unavailable("provider_disabled", p.Name+" has no API key registered."))
			return
		}

		ureq := upstream.Request{
			Provider: p.Name,
			BaseURL:  p.BaseURL,
			Model:    m.ID,
			APIKey:   key(p.Name),
			Body:     body,
		}
		if req.Stream {
			streamChat(w, r, client, ureq)
			return
		}

		resp, err := client.Complete(r.Context(), ureq)
		if err != nil {
			writeUpstreamErr(w, r, err, ureq)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(resp.Status)
		w.Write(resp.Body)
	})
}

// streamChat relays a streamed completion as server-sent events. Until the
// first event is written the HTTP status is still free, so a failure then is
// a normal error response. After that the 200 is gone, so a failure is sent as
// one more event (the same {"error": ...} object) and the stream ends.
func streamChat(w http.ResponseWriter, r *http.Request, client *upstream.Client, ureq upstream.Request) {
	s, err := client.Stream(r.Context(), ureq)
	if err != nil {
		writeUpstreamErr(w, r, err, ureq)
		return
	}
	defer s.Close()

	flusher, _ := w.(http.Flusher)
	flush := func() {
		if flusher != nil {
			flusher.Flush()
		}
	}
	started := false
	start := func() {
		if started {
			return
		}
		started = true
		h := w.Header()
		h.Set("Content-Type", "text/event-stream")
		h.Set("Cache-Control", "no-store")
		h.Set("X-Accel-Buffering", "no") // keep proxies from buffering the stream
		w.WriteHeader(http.StatusOK)
	}

	for {
		data, err := s.Next()
		if errors.Is(err, io.EOF) {
			start()
			io.WriteString(w, "data: [DONE]\n\n")
			flush()
			return
		}
		if err != nil {
			if !started {
				writeUpstreamErr(w, r, err, ureq)
				return
			}
			if ue, ok := errors.AsType[*upstream.Error](err); ok {
				w.Write(apierror.FromUpstream(ue, apierror.LimitHint{}).SSE())
				flush()
			}
			return // caller hung up, or the error event was sent
		}
		// Usage-only final chunk that the router asked for, not the client.
		if s.UsageAdded && noChoices(data) {
			continue
		}
		start()
		w.Write([]byte("data: "))
		w.Write(data)
		w.Write([]byte("\n\n"))
		flush()
	}
}

func noChoices(chunk []byte) bool {
	var c struct {
		Choices []json.RawMessage `json:"choices"`
	}
	return json.Unmarshal(chunk, &c) == nil && len(c.Choices) == 0
}

// writeUpstreamErr answers with the router's error JSON. It writes nothing
// when the caller has already hung up.
func writeUpstreamErr(w http.ResponseWriter, r *http.Request, err error, ureq upstream.Request) {
	if ue, ok := errors.AsType[*upstream.Error](err); ok {
		apierror.Write(w, apierror.FromUpstream(ue, apierror.LimitHint{}))
	} else if r.Context().Err() == nil {
		apierror.Write(w, apierror.ProviderError(ureq.Provider, ureq.Model, ""))
	}
}
