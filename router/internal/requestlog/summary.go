package requestlog

import (
	"time"

	"github.com/RyanCaudill04/ai-gateway/router/internal/api"
)

// OnSummary is the api.SummaryFunc that feeds the log: pass l.OnSummary as
// ChatDeps.OnSummary. It copies the summary's metadata fields one by one into
// an Entry, so nothing new that api adds to a summary reaches the database
// unless someone adds it here on purpose.
func (l *Logger) OnSummary(s api.RequestSummary) {
	l.Log(entryFrom(s, l.now()))
}

// entryFrom converts a finished request's summary. arrived is when the log
// saw it; the request started Latency earlier.
//
// FallbackAttempts is Attempts - 1: the summary counts upstream calls (1 = the
// first provider answered), the log counts how many times the router had to
// fall back. 0 attempts (failed before any provider was tried) is also 0.
func entryFrom(s api.RequestSummary, finished time.Time) Entry {
	e := Entry{
		Time:             finished.Add(-s.Latency),
		Service:          s.Service,
		RequestedModel:   s.RequestedModel,
		Provider:         s.Provider,
		Model:            s.Model,
		FallbackAttempts: max(s.Attempts-1, 0),
		PromptTokens:     Unknown,
		CompletionTokens: Unknown,
		Latency:          s.Latency,
		Status:           s.Status,
		ErrorType:        string(s.ErrorType), // the category, never a message
	}
	if s.Usage != nil {
		e.PromptTokens, e.CompletionTokens = s.Usage.PromptTokens, s.Usage.CompletionTokens
	}
	return e
}
