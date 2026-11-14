package requestlog

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/RyanCaudill04/ai-gateway/router/internal/api"
	"github.com/RyanCaudill04/ai-gateway/router/internal/apikeys"
)

// slowDB makes every batch write take `delay`, like a dying SD card.
type slowDB struct {
	DB
	delay time.Duration
}

func (s slowDB) WithTx(ctx context.Context, fn func(*sql.Tx) error) error {
	time.Sleep(s.delay)
	return s.DB.WithTx(ctx, fn)
}

// instantUpstream answers every request at once with a small completion.
func instantUpstream(t testing.TB) *httptest.Server {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{
			"choices": []any{map[string]any{"message": map[string]any{"role": "assistant", "content": "ok"}}},
			"usage":   map[string]any{"prompt_tokens": 5, "completion_tokens": 7},
		})
	}))
	t.Cleanup(up.Close)
	return up
}

// scenario is one way of running the log. start returns the summary hook to
// give the chat handler and a function that stops the log.
type scenario struct {
	name  string
	start func(t testing.TB) (on func(s api.RequestSummary), stop func(), l *Logger)
}

func scenarios() []scenario {
	return []scenario{
		{"logging off", func(testing.TB) (func(api.RequestSummary), func(), *Logger) { return nil, func() {}, nil }},
		{"logging on", func(t testing.TB) (func(api.RequestSummary), func(), *Logger) {
			l := Open(openStore(t), Options{})
			return l.OnSummary, stopQuickly(l), l
		}},
		{"logging on, 50 ms per write, 16-row queue", func(t testing.TB) (func(api.RequestSummary), func(), *Logger) {
			// A tiny queue and a slow disk: the queue fills and rows are dropped.
			l := Open(slowDB{openStore(t), 50 * time.Millisecond}, Options{Buffer: 16, BatchSize: 8})
			return l.OnSummary, stopQuickly(l), l
		}},
	}
}

func stopQuickly(l *Logger) func() {
	return func() {
		ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
		defer cancel()
		l.Close(ctx) // a stuck slow writer is abandoned; the store is closed by Cleanup
	}
}

// oneRequest sends one chat request through the handler and returns how long it took.
func oneRequest(h http.Handler) time.Duration {
	req := httptest.NewRequest("POST", "/v1/chat/completions",
		strings.NewReader(`{"model":"fast","messages":[{"role":"user","content":"hi"}]}`))
	req = req.WithContext(apikeys.ContextWithName(req.Context(), "bench"))
	rec := httptest.NewRecorder()
	start := time.Now()
	h.ServeHTTP(rec, req)
	return time.Since(start)
}

// TestLatencyDoesNotFollowTheWriter measures the chat handler's latency
// against an instant fake upstream with logging off, on, and on with a slow
// database. Run with -v to see the numbers. The slow-writer case is the one
// that matters: a response must not wait for the disk.
func TestLatencyDoesNotFollowTheWriter(t *testing.T) {
	const n = 400
	up := instantUpstream(t)
	p50 := map[string]time.Duration{}
	for _, sc := range scenarios() {
		on, stop, l := sc.start(t)
		h := chatHandler(t, up.URL, on)
		oneRequest(h) // warm up connections
		lat := make([]time.Duration, n)
		for i := range lat {
			lat[i] = oneRequest(h)
		}
		slices.Sort(lat)
		var sum time.Duration
		for _, d := range lat {
			sum += d
		}
		p50[sc.name] = lat[n/2]
		dropped := uint64(0)
		if l != nil {
			dropped = l.Dropped()
		}
		t.Logf("%-45s p50 %-9v p95 %-9v p99 %-9v mean %-9v dropped %d",
			sc.name, lat[n/2], lat[n*95/100], lat[n*99/100], sum/n, dropped)
		stop()
	}
	// Generous bound (timing tests on shared machines are noisy): the slow
	// writer adds 50 ms per batch, so following it would blow far past this.
	off, slow := p50["logging off"], p50["logging on, 50 ms per write, 16-row queue"]
	if slow > 3*off+5*time.Millisecond {
		t.Errorf("median latency with a slow writer = %v, logging off = %v: responses are waiting for the log", slow, off)
	}
}

func BenchmarkChatLatency(b *testing.B) {
	up := instantUpstream(b)
	for _, sc := range scenarios() {
		b.Run(sc.name, func(b *testing.B) {
			on, stop, _ := sc.start(b)
			defer stop()
			h := chatHandler(b, up.URL, on)
			oneRequest(h)
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				oneRequest(h)
			}
		})
	}
}
