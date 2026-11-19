package requestlog

import (
	"context"
	"errors"
	"log/slog"
	"path/filepath"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/RyanCaudill04/ai-gateway/router/internal/api"
	"github.com/RyanCaudill04/ai-gateway/router/internal/apierror"
	"github.com/RyanCaudill04/ai-gateway/router/internal/store"
)

func openStore(t testing.TB) *store.Store {
	t.Helper()
	s, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func countRows(t *testing.T, s *store.Store) int {
	t.Helper()
	var n int
	if err := s.Reader().QueryRow(`SELECT COUNT(*) FROM request_log`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func closeNow(t *testing.T, l *Logger) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := l.Close(ctx); err != nil {
		t.Fatal(err)
	}
}

// manualTicker lets a test fire the flush timer itself.
type manualTicker struct{ c chan time.Time }

func newManualTicker() *manualTicker { return &manualTicker{c: make(chan time.Time)} }
func (m *manualTicker) tick(time.Duration) (<-chan time.Time, func()) {
	return m.c, func() {}
}
func (m *manualTicker) fire() { m.c <- time.Time{} }

// recorder is a fake writer that remembers each batch it was given.
type recorder struct {
	batches chan []Entry
}

func newRecorder() *recorder { return &recorder{batches: make(chan []Entry, 100)} }
func (r *recorder) write(_ context.Context, b []Entry) error {
	r.batches <- append([]Entry(nil), b...) // the logger reuses its slice
	return nil
}
func (r *recorder) next(t *testing.T) []Entry {
	t.Helper()
	select {
	case b := <-r.batches:
		return b
	case <-time.After(3 * time.Second):
		t.Fatal("no batch written")
		return nil
	}
}
func (r *recorder) none(t *testing.T) {
	t.Helper()
	select {
	case b := <-r.batches:
		t.Fatalf("unexpected batch of %d", len(b))
	case <-time.After(20 * time.Millisecond):
	}
}

func entry(i int) Entry {
	return Entry{Service: "svc", RequestedModel: "fast", Status: 200, PromptTokens: Unknown, CompletionTokens: Unknown, Latency: time.Duration(i) * time.Millisecond}
}

func TestRowsRoundTripThroughDatabase(t *testing.T) {
	arrived := time.Date(2026, 11, 14, 9, 30, 15, 900, time.UTC)
	tests := []struct {
		name string
		in   Entry
		want [11]any // ts, service, requested, provider, model, fallback, prompt, completion, latency, status, error
	}{
		{"success with tokens",
			Entry{Time: arrived, Service: "digest", RequestedModel: "fast", Provider: "groq", Model: "big",
				PromptTokens: 12, CompletionTokens: 34, Latency: 250 * time.Millisecond, Status: 200},
			[11]any{"2026-11-14T09:30:15Z", "digest", "fast", "groq", "big", int64(0), int64(12), int64(34), int64(250), int64(200), nil}},
		{"fell back, no token counts",
			Entry{Time: arrived, Service: "ui", RequestedModel: "fast", Provider: "ollama", Model: "tiny", FallbackAttempts: 2,
				PromptTokens: Unknown, CompletionTokens: Unknown, Latency: 1500 * time.Millisecond, Status: 200},
			[11]any{"2026-11-14T09:30:15Z", "ui", "fast", "ollama", "tiny", int64(2), nil, nil, int64(1500), int64(200), nil}},
		{"failed before any provider",
			Entry{Time: arrived, Service: "ui", RequestedModel: "nope", PromptTokens: Unknown, CompletionTokens: Unknown,
				Status: 404, ErrorType: "model_not_found"},
			[11]any{"2026-11-14T09:30:15Z", "ui", "nope", nil, nil, int64(0), nil, nil, int64(0), int64(404), "model_not_found"}},
		{"zero tokens is not unknown",
			Entry{Time: arrived, Service: "s", RequestedModel: "fast", Provider: "groq", Model: "big", Status: 200},
			[11]any{"2026-11-14T09:30:15Z", "s", "fast", "groq", "big", int64(0), int64(0), int64(0), int64(0), int64(200), nil}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s := openStore(t)
			l := Open(s, Options{})
			l.Log(tc.in)
			closeNow(t, l)

			var got [11]any
			dest := make([]any, 11)
			for i := range dest {
				dest[i] = &got[i]
			}
			err := s.Reader().QueryRow(`SELECT ts, service, requested_model, provider, model, fallback_attempts,
				prompt_tokens, completion_tokens, latency_ms, status, error_type FROM request_log`).Scan(dest...)
			if err != nil {
				t.Fatal(err)
			}
			if got != tc.want {
				t.Errorf("row = %#v\nwant  %#v", got, tc.want)
			}
		})
	}
}

func TestEntryFromSummary(t *testing.T) {
	finished := time.Date(2026, 11, 14, 12, 0, 10, 0, time.UTC)
	tests := []struct {
		name string
		in   api.RequestSummary
		want Entry
	}{
		{"no fallback", api.RequestSummary{Service: "a", RequestedModel: "fast", Provider: "groq", Model: "big", Attempts: 1, Status: 200,
			Latency: 2 * time.Second, Usage: &api.Usage{PromptTokens: 3, CompletionTokens: 4}},
			Entry{Time: finished.Add(-2 * time.Second), Service: "a", RequestedModel: "fast", Provider: "groq", Model: "big", FallbackAttempts: 0,
				PromptTokens: 3, CompletionTokens: 4, Latency: 2 * time.Second, Status: 200}},
		{"two fallbacks", api.RequestSummary{Attempts: 3, Status: 200},
			Entry{Time: finished, FallbackAttempts: 2, PromptTokens: Unknown, CompletionTokens: Unknown, Status: 200}},
		{"nothing tried", api.RequestSummary{Attempts: 0, Status: 400, ErrorType: apierror.TypeRateLimited},
			Entry{Time: finished, FallbackAttempts: 0, PromptTokens: Unknown, CompletionTokens: Unknown, Status: 400, ErrorType: "rate_limited"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := entryFrom(tc.in, finished); got != tc.want {
				t.Errorf("entry = %+v\nwant   %+v", got, tc.want)
			}
		})
	}
}

func TestFlushBySizeThenByTime(t *testing.T) {
	rec, tk := newRecorder(), newManualTicker()
	l := newLogger(rec.write, Options{BatchSize: 3, Tick: tk.tick})
	defer closeNow(t, l)

	for i := 0; i < 7; i++ {
		l.Log(entry(i))
	}
	if b := rec.next(t); len(b) != 3 || b[0].Latency != 0 {
		t.Fatalf("first batch = %d rows (first latency %v), want 3 starting at 0", len(b), b[0].Latency)
	}
	if b := rec.next(t); len(b) != 3 || b[0].Latency != 3*time.Millisecond {
		t.Fatalf("second batch = %d rows, want 3 starting at 3", len(b))
	}
	rec.none(t) // the 7th row waits for the timer

	tk.fire()
	if b := rec.next(t); len(b) != 1 || b[0].Latency != 6*time.Millisecond {
		t.Fatalf("timer batch = %d rows, want the 1 leftover", len(b))
	}
	tk.fire() // an empty batch writes nothing
	rec.none(t)
}

func TestFullQueueDropsWithoutBlocking(t *testing.T) {
	started, release := make(chan struct{}, 16), make(chan struct{})
	written := atomic.Int64{}
	write := func(_ context.Context, b []Entry) error {
		started <- struct{}{}
		<-release
		written.Add(int64(len(b)))
		return nil
	}
	l := newLogger(write, Options{Buffer: 2, BatchSize: 1, Tick: newManualTicker().tick})

	l.Log(entry(0))
	<-started // the writer now holds row 0 and is stuck in the database

	done := make(chan struct{})
	go func() {
		for i := 1; i <= 10; i++ {
			l.Log(entry(i))
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Log blocked while the writer was stuck")
	}
	// Row 0 is in the writer, rows 1 and 2 fill the buffer, 3..10 are dropped.
	if got := l.Dropped(); got != 8 {
		t.Errorf("Dropped = %d, want 8", got)
	}

	close(release)
	closeNow(t, l)
	if got := written.Load(); got != 3 {
		t.Errorf("written = %d, want the 3 that were accepted", got)
	}
}

// captureWarnings swaps the default slog logger for one that records the
// "dropped" counts of warnings.
func captureWarnings(t *testing.T) *[]uint64 {
	t.Helper()
	var mu sync.Mutex
	var counts []uint64
	old := slog.Default()
	slog.SetDefault(slog.New(handlerFunc(func(r slog.Record) {
		if r.Level != slog.LevelWarn {
			return
		}
		r.Attrs(func(a slog.Attr) bool {
			if a.Key == "dropped_since_last_warning" {
				mu.Lock()
				counts = append(counts, a.Value.Uint64())
				mu.Unlock()
			}
			return true
		})
	})))
	t.Cleanup(func() { slog.SetDefault(old) })
	return &counts
}

type handlerFunc func(slog.Record)

func (handlerFunc) Enabled(context.Context, slog.Level) bool { return true }
func (h handlerFunc) Handle(_ context.Context, r slog.Record) error {
	h(r)
	return nil
}
func (h handlerFunc) WithAttrs([]slog.Attr) slog.Handler { return h }
func (h handlerFunc) WithGroup(string) slog.Handler      { return h }

func TestDropWarningIsRateLimited(t *testing.T) {
	counts := captureWarnings(t)
	clock := time.Date(2026, 11, 14, 12, 0, 0, 0, time.UTC)
	var mu sync.Mutex
	now := func() time.Time { mu.Lock(); defer mu.Unlock(); return clock }
	advance := func(d time.Duration) { mu.Lock(); clock = clock.Add(d); mu.Unlock() }

	l := newLogger(newRecorder().write, Options{Now: now, WarnEvery: time.Minute, Tick: newManualTicker().tick})
	closeNow(t, l) // every row offered from here on is dropped

	for i := 0; i < 1000; i++ {
		l.Log(entry(i))
	}
	if len(*counts) != 1 || (*counts)[0] != 1 {
		t.Fatalf("after 1000 drops in one instant: warnings %v, want one (for the first drop)", *counts)
	}
	advance(30 * time.Second)
	for i := 0; i < 1000; i++ {
		l.Log(entry(i))
	}
	if len(*counts) != 1 {
		t.Fatalf("within the interval: warnings %v, want still one", *counts)
	}
	advance(31 * time.Second)
	l.Log(entry(0))
	if want := []uint64{1, 2000}; !reflect.DeepEqual(*counts, want) {
		t.Fatalf("warnings = %v, want %v (the second reports everything since the first)", *counts, want)
	}
	if got := l.Dropped(); got != 2001 {
		t.Errorf("Dropped = %d, want 2001", got)
	}
}

func TestCloseWritesEveryQueuedRow(t *testing.T) {
	s := openStore(t)
	l := Open(s, Options{BatchSize: 64})
	const n = 1000
	for i := 0; i < n; i++ {
		l.Log(entry(i))
	}
	closeNow(t, l)
	if got := countRows(t, s); got != n {
		t.Fatalf("rows after Close = %d, want %d", got, n)
	}
	if l.Dropped() != 0 || l.WriteFailed() != 0 {
		t.Errorf("dropped %d, failed %d; want none", l.Dropped(), l.WriteFailed())
	}
}

func TestLogAfterCloseIsSafe(t *testing.T) {
	s := openStore(t)
	l := Open(s, Options{})
	l.Log(entry(1))
	closeNow(t, l)
	s.Close() // the database is gone too

	l.Log(entry(2)) // must not panic or block
	closeNow(t, l)  // closing twice is fine
	if l.Dropped() != 1 {
		t.Errorf("Dropped = %d, want 1 (the row offered after Close)", l.Dropped())
	}
}

func TestFailedBatchWrite(t *testing.T) {
	boom := errors.New("disk I/O error")
	tests := []struct {
		name       string
		failures   int // how many calls fail before one succeeds
		wantCalls  int
		wantFailed uint64
	}{
		{"one glitch is retried", 1, 2, 0},
		{"two failures lose the batch", 2, 2, 3},
		{"healthy", 0, 1, 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var calls atomic.Int64
			write := func(context.Context, []Entry) error {
				if int(calls.Add(1)) <= tc.failures {
					return boom
				}
				return nil
			}
			l := newLogger(write, Options{Tick: newManualTicker().tick})
			for i := 0; i < 3; i++ {
				l.Log(entry(i))
			}
			closeNow(t, l)
			if int(calls.Load()) != tc.wantCalls || l.WriteFailed() != tc.wantFailed {
				t.Errorf("calls = %d, failed = %d; want %d, %d", calls.Load(), l.WriteFailed(), tc.wantCalls, tc.wantFailed)
			}
		})
	}
}

func TestFailedBatchDoesNotStopTheWriter(t *testing.T) {
	var calls atomic.Int64
	rec := newRecorder()
	write := func(ctx context.Context, b []Entry) error {
		if calls.Add(1) <= 2 { // first batch: both tries fail
			return errors.New("disk full")
		}
		return rec.write(ctx, b)
	}
	tk := newManualTicker()
	l := newLogger(write, Options{BatchSize: 1, Tick: tk.tick})
	defer closeNow(t, l)
	l.Log(entry(1)) // lost
	l.Log(entry(2)) // written
	if b := rec.next(t); len(b) != 1 || b[0].Latency != 2*time.Millisecond {
		t.Fatalf("after a failed batch got %v, want the second row", b)
	}
}

func TestCloseGivesUpWhenWriterIsStuck(t *testing.T) {
	write := func(ctx context.Context, _ []Entry) error { <-ctx.Done(); return ctx.Err() }
	l := newLogger(write, Options{BatchSize: 1, Tick: newManualTicker().tick})
	l.Log(entry(1))

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if err := l.Close(ctx); err == nil {
		t.Fatal("Close returned nil with a stuck writer")
	}
	select { // the cancelled context frees the writer
	case <-l.done:
	case <-time.After(2 * time.Second):
		t.Fatal("writer still running after Close gave up")
	}
}

func TestManyGoroutinesLogging(t *testing.T) {
	s := openStore(t)
	l := Open(s, Options{BatchSize: 50})
	const workers, each = 50, 200
	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < each; i++ {
				l.Log(entry(i))
			}
		}()
	}
	// Close races with the loggers on purpose: every row is either written or counted.
	wg.Wait()
	closeNow(t, l)
	got := uint64(countRows(t, s))
	if got+l.Dropped()+l.WriteFailed() != workers*each {
		t.Fatalf("written %d + dropped %d + failed %d != %d", got, l.Dropped(), l.WriteFailed(), workers*each)
	}
	if got == 0 {
		t.Fatal("nothing written")
	}
}

func TestLogRacingWithClose(t *testing.T) {
	s := openStore(t)
	l := Open(s, Options{})
	var wg sync.WaitGroup
	for w := 0; w < 20; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 500; i++ {
				l.Log(entry(i))
			}
		}()
	}
	closeNow(t, l)
	wg.Wait()
	if got := uint64(countRows(t, s)); got+l.Dropped()+l.WriteFailed() != 20*500 {
		t.Fatalf("written %d + dropped %d + failed %d != %d", got, l.Dropped(), l.WriteFailed(), 20*500)
	}
}

var _ DB = (*store.Store)(nil)
