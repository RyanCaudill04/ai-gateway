package api

import (
	"errors"
	"io"
	"log/slog"
	"net/http"

	"github.com/RyanCaudill04/ai-gateway/router/internal/apierror"
	"github.com/RyanCaudill04/ai-gateway/router/internal/upstream"
)

// writeJSON sends a non-streaming answer: the provider's status and JSON body
// exactly as received.
func writeJSON(w http.ResponseWriter, ans *answer, sum *RequestSummary) {
	sum.Usage, _ = parseChunkUsage(ans.resp.Body)
	sum.Status = ans.resp.Status
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(ans.resp.Status)
	w.Write(ans.resp.Body)
}

var doneEvent = []byte("data: [DONE]\n\n")

// writeStream sends a streaming answer as server-sent events, flushing each
// one as soon as it arrives so tokens reach the client while they are being
// generated. It owns ans.stream and closes it, which also stops the upstream
// request if the client goes away.
//
// The stream ends with "data: [DONE]" when the provider finishes. If the
// provider fails mid-stream, the error goes out as one apierror SSE event and
// the stream ends WITHOUT [DONE]: [DONE] means "complete", and leaving it out
// lets a client that only watches for it see the answer was cut short. The
// OpenAI SDKs raise on the error event either way.
func (h *chatHandler) writeStream(w http.ResponseWriter, r *http.Request, ans *answer, sum *RequestSummary) {
	s := ans.stream
	defer s.Close()
	sum.Usage = ans.usage

	hdr := w.Header()
	hdr.Set("Content-Type", "text/event-stream")
	// no-transform and X-Accel-Buffering keep proxies (nginx, Cloudflare)
	// from buffering or compressing the stream, which would hold tokens back.
	hdr.Set("Cache-Control", "no-store, no-transform")
	hdr.Set("X-Accel-Buffering", "no")
	w.WriteHeader(s.Status)
	sum.Status = s.Status

	rc := http.NewResponseController(w)
	send := func(b []byte) bool {
		if _, err := w.Write(b); err != nil {
			return false
		}
		return rc.Flush() == nil
	}
	sendEvent := func(data []byte) bool {
		buf := make([]byte, 0, len(data)+8)
		buf = append(append(append(buf, "data: "...), data...), '\n', '\n')
		return send(buf)
	}

	// The first event was read during the attempt. Without one the stream
	// was empty and only [DONE] remains.
	if ans.first == nil {
		if !send(doneEvent) {
			sum.ClientGone = true
		}
		return
	}
	if !sendEvent(ans.first) {
		sum.ClientGone = true
		return
	}

	for {
		data, usage, err := nextEvent(s)
		if usage != nil {
			sum.Usage = usage
		}
		switch {
		case err == nil:
			if !sendEvent(data) {
				// A failed write means the client is gone; returning closes
				// the upstream stream.
				sum.ClientGone = true
				return
			}
		case errors.Is(err, io.EOF):
			if !send(doneEvent) {
				sum.ClientGone = true
			}
			return
		case clientGone(r.Context(), err):
			sum.ClientGone = true
			return
		default:
			var ue *upstream.Error
			if !errors.As(err, &ue) {
				ue = &upstream.Error{Kind: upstream.KindProvider, Provider: ans.target.provider.Name,
					Model: ans.target.model.ID, Message: "stream failed"}
			}
			h.Observer.ObserveUpstream(ue.Provider, ue.Model, ue.Header, ue)
			slog.Warn("chat: provider failed mid-stream", "provider", ue.Provider, "model", ue.Model,
				"kind", ue.Kind, "status", ue.Status, "message", ue.Message)
			e := apierror.FromUpstream(ue, apierror.LimitHint{})
			sum.ErrorType = e.Type
			if !send(e.SSE()) {
				sum.ClientGone = true
			}
			return
		}
	}
}
