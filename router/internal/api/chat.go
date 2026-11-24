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
// "groq/llama-3.3-70b-versatile". Routes, fallback and streaming come later;
// for now it forwards the body to that one provider and returns its answer.
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
		if req.Stream {
			apierror.Write(w, apierror.InvalidRequest("Streaming is not supported yet.", "stream"))
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

		resp, err := client.Complete(r.Context(), upstream.Request{
			Provider: p.Name,
			BaseURL:  p.BaseURL,
			Model:    m.ID,
			APIKey:   key(p.Name),
			Body:     body,
		})
		if err != nil {
			if ue, ok := errors.AsType[*upstream.Error](err); ok {
				apierror.Write(w, apierror.FromUpstream(ue, apierror.LimitHint{}))
			} else if r.Context().Err() == nil { // not just the caller hanging up
				apierror.Write(w, apierror.ProviderError(p.Name, m.ID, ""))
			}
			return
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(resp.Status)
		w.Write(resp.Body)
	})
}
