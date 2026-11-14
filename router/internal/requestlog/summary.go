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
// FallbackAttempts is the number of route entries that did not answer: the
// summary's Fallback list, which holds every entry that failed or was skipped
// because it was known to be out of quota. Skips count, since on free tiers
// "the first provider was already exhausted" is the usual reason a request
// lands on the second one, and it costs no upstream call. A pinned model
// never falls back, so it is always 0.
func entryFrom(s api.RequestSummary, finished time.Time) Entry {
	e := Entry{
		Time:             finished.Add(-s.Latency),
		Service:          s.Service,
		RequestedModel:   s.RequestedModel,
		Provider:         s.Provider,
		Model:            s.Model,
		FallbackAttempts: len(s.Fallback),
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
