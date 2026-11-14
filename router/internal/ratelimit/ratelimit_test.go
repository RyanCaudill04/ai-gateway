package ratelimit

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/RyanCaudill04/ai-gateway/router/internal/config"
	"github.com/RyanCaudill04/ai-gateway/router/internal/store"
	"github.com/RyanCaudill04/ai-gateway/router/internal/upstream"
)

// testConfig names the Groq provider something other than "groq", to show the
// header convention follows the base_url, not the name.
func testConfig() *config.Config {
	return &config.Config{Providers: map[string]*config.Provider{
		"fastcloud":  {Name: "fastcloud", BaseURL: "https://api.groq.com/openai/v1"},
		"openrouter": {Name: "openrouter", BaseURL: "https://openrouter.ai/api/v1"},
		"gemini":     {Name: "gemini", BaseURL: "https://generativelanguage.googleapis.com/v1beta/openai"},
	}}
}

func memTracker(t *testing.T) *Tracker {
	t.Helper()
	tr, err := Open(context.Background(), nil, testConfig())
	if err != nil {
		t.Fatal(err)
	}
	return tr
}

func groqHeaders(remReq, remTok, resetReq, resetTok string) http.Header {
	return hdr(
		"x-ratelimit-limit-requests", "1000",
		"x-ratelimit-remaining-requests", remReq,
		"x-ratelimit-reset-requests", resetReq,
		"x-ratelimit-limit-tokens", "12000",
		"x-ratelimit-remaining-tokens", remTok,
		"x-ratelimit-reset-tokens", resetTok,
	)
}

const llama = "llama-3.3-70b-versatile"

// The issue's "done when": after one Groq response the router knows the
// model's minute and day windows. Verified against Groq's documented headers.
func TestOneGroqResponseGivesMinuteAndDayWindows(t *testing.T) {
	tr := memTracker(t)
	tr.Observe("fastcloud", llama, groqHeaders("999", "11950", "1m26.4s", "7.66s"), t0)

	u, ok := tr.Model("fastcloud", llama)
	if !ok || !u.Known() {
		t.Fatalf("model not known after a response: %+v", u)
	}
	want := []Limit{
		win(Requests, Day, 999, 1000, time.Minute+26400*time.Millisecond),
		win(Tokens, Minute, 11950, 12000, 7660*time.Millisecond),
	}
	if len(u.Limits) != 2 || !sameLimit(u.Limits[0], want[0]) || !sameLimit(u.Limits[1], want[1]) {
		t.Fatalf("limits = %+v\nwant     %+v", u.Limits, want)
	}
	if !u.LastObserved.Equal(t0) {
		t.Errorf("LastObserved = %v, want %v", u.LastObserved, t0)
	}
}

func TestExhausted(t *testing.T) {
	tests := []struct {
		name       string
		h          http.Header
		at         time.Duration // when to ask, after t0
		wantKind   Kind
		wantWindow Window
		wantUntil  time.Duration
		exhausted  bool
	}{
		{"plenty left", groqHeaders("500", "9000", "10s", "5s"), 0, "", "", 0, false},
		{"requests at 0 before reset", groqHeaders("0", "9000", "2h3m", "5s"), time.Minute, Requests, Day, 2*time.Hour + 3*time.Minute, true},
		{"requests at 0 after reset: full again", groqHeaders("0", "9000", "30s", "5s"), 31 * time.Second, "", "", 0, false},
		{"tokens below MinTokens", groqHeaders("500", "99", "10s", "7.66s"), 0, Tokens, Minute, 7660 * time.Millisecond, true},
		{"tokens at MinTokens is still usable", groqHeaders("500", "100", "10s", "7.66s"), 0, "", "", 0, false},
		{"both exhausted: the longest-lasting wins", groqHeaders("0", "0", "2m", "30s"), 0, Requests, Day, 2 * time.Minute, true},
		{"both exhausted, tokens last longer", groqHeaders("0", "0", "20s", "45s"), 0, Tokens, Minute, 45 * time.Second, true},
		{"longest has reset, shorter... also reset", groqHeaders("0", "0", "20s", "45s"), 50 * time.Second, "", "", 0, false},
		{"one reset, the other still counts", groqHeaders("0", "0", "20s", "45s"), 30 * time.Second, Tokens, Minute, 45 * time.Second, true},
		{"remaining 0 but no reset time: not exhausted", hdr("x-ratelimit-remaining-requests", "0"), 0, "", "", 0, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tr := memTracker(t)
			tr.Observe("fastcloud", llama, tt.h, t0)
			l, ok := tr.Exhausted("fastcloud", llama, t0.Add(tt.at))
			if ok != tt.exhausted {
				t.Fatalf("exhausted = %v (%+v), want %v", ok, l, tt.exhausted)
			}
			if !ok {
				return
			}
			if l.Kind != tt.wantKind || l.Window != tt.wantWindow || !l.ResetsAt.Equal(t0.Add(tt.wantUntil)) {
				t.Errorf("got %s/%s until %v, want %s/%s until %v",
					l.Kind, l.Window, l.ResetsAt, tt.wantKind, tt.wantWindow, t0.Add(tt.wantUntil))
			}
		})
	}
}

func TestUnknownModels(t *testing.T) {
	tr := memTracker(t)
	// Gemini answers without rate-limit headers.
	tr.Observe("gemini", "gemini-2.5-flash", hdr("Content-Type", "application/json"), t0)

	if _, ok := tr.Exhausted("gemini", "gemini-2.5-flash", t0); ok {
		t.Error("a model with no headers must never be skipped ahead of a 429")
	}
	u, ok := tr.Model("gemini", "gemini-2.5-flash")
	if !ok || u.Known() || !u.LastObserved.Equal(t0) {
		t.Errorf("gemini usage = %+v, ok=%v; want observed but unknown", u, ok)
	}
	if u, ok := tr.Model("nobody", "nothing"); ok || u.Known() {
		t.Errorf("never-observed model: %+v, ok=%v", u, ok)
	}
	if _, ok := tr.Exhausted("nobody", "nothing", t0); ok {
		t.Error("never-observed model reported exhausted")
	}
}

func rateLimited(provider, model string, retry time.Duration, h http.Header) error {
	// Wrapped, as the chat handler might return it.
	return fmt.Errorf("attempt 1: %w", &upstream.Error{
		Kind: upstream.KindRateLimited, Provider: provider, Model: model,
		Status: http.StatusTooManyRequests, RetryAfter: retry, Header: h,
	})
}

func TestObserveError(t *testing.T) {
	tests := []struct {
		name       string
		err        error
		wantWindow Window
		wantKind   Kind
		wantUntil  time.Duration
		wantMax    int64
		exhausted  bool
	}{
		{"429 with Retry-After, no headers (gemini)",
			rateLimited("gemini", "m", 37*time.Second, nil), Minute, Requests, 37 * time.Second, Unknown, true},
		{"429 without RetryAfter: default back-off",
			rateLimited("gemini", "m", 0, nil), Minute, Requests, DefaultBackoff, Unknown, true},
		{"429 with a long wait is a day window",
			rateLimited("gemini", "m", 5*time.Hour, nil), Day, Requests, 5 * time.Hour, Unknown, true},
		{"openrouter 429 headers explain it",
			rateLimited("openrouter", "m:free", 40*time.Second, hdr(
				"X-RateLimit-Limit", "20", "X-RateLimit-Remaining", "0",
				"X-RateLimit-Reset", fmt.Sprint(t0.Add(40*time.Second).UnixMilli()))),
			Minute, Requests, 40 * time.Second, 20, true},
		{"groq 429 on an unreported limit (requests per minute)",
			rateLimited("fastcloud", "m", 2*time.Second, groqHeaders("900", "11000", "10m", "3s")),
			Minute, Requests, 2 * time.Second, Unknown, true},
		{"groq 429 explained by the token header",
			rateLimited("fastcloud", "m", 7*time.Second, groqHeaders("900", "10", "10m", "7s")),
			Minute, Tokens, 7 * time.Second, 12000, true},
		{"a 5xx doesn't block", &upstream.Error{Kind: upstream.KindProvider, Provider: "gemini", Model: "m", Status: 503},
			"", "", 0, 0, false},
		{"not an upstream error", errors.New("boom"), "", "", 0, 0, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tr := memTracker(t)
			tr.ObserveError(tt.err, t0)
			var ue *upstream.Error
			if !errors.As(tt.err, &ue) {
				if len(tr.Snapshot()) != 0 {
					t.Fatal("a non-upstream error was recorded")
				}
				return
			}
			l, ok := tr.Exhausted(ue.Provider, ue.Model, t0)
			if ok != tt.exhausted {
				t.Fatalf("exhausted = %v (%+v), want %v", ok, l, tt.exhausted)
			}
			if !ok {
				return
			}
			if l.Kind != tt.wantKind || l.Window != tt.wantWindow || !l.ResetsAt.Equal(t0.Add(tt.wantUntil)) || l.Max != tt.wantMax {
				t.Errorf("got %+v\nwant %s/%s until +%v, max %d", l, tt.wantKind, tt.wantWindow, tt.wantUntil, tt.wantMax)
			}
			if _, ok := tr.Exhausted(ue.Provider, ue.Model, t0.Add(tt.wantUntil)); ok {
				t.Error("still exhausted at the reset time")
			}
		})
	}
}

// A 429 keeps the window size learned earlier, and a later response with
// fresh headers replaces the 429's guess.
func TestRateLimitKeepsKnownSize(t *testing.T) {
	tr := memTracker(t)
	tr.ObserveError(rateLimited("openrouter", "m:free", 0, hdr(
		"X-RateLimit-Limit", "20", "X-RateLimit-Remaining", "3",
		"X-RateLimit-Reset", fmt.Sprint(t0.Add(30*time.Second).UnixMilli()))), t0)
	l, ok := tr.Exhausted("openrouter", "m:free", t0)
	if !ok || l.Max != 20 || l.Remaining != 0 || !l.ResetsAt.Equal(t0.Add(DefaultBackoff)) {
		t.Fatalf("got %+v ok=%v; want 0/20 until +%v", l, ok, DefaultBackoff)
	}
}

func TestOlderObservationDoesNotOverwriteNewer(t *testing.T) {
	tr := memTracker(t)
	tr.Observe("fastcloud", llama, groqHeaders("10", "5000", "1m", "10s"), t0.Add(time.Second))
	tr.Observe("fastcloud", llama, groqHeaders("0", "0", "1m", "10s"), t0) // finished late
	if l, ok := tr.Exhausted("fastcloud", llama, t0.Add(time.Second)); ok {
		t.Fatalf("stale reading won: %+v", l)
	}
}

func TestSnapshotIsSortedCopy(t *testing.T) {
	tr := memTracker(t)
	tr.Observe("openrouter", "b", nil, t0)
	tr.Observe("fastcloud", "z", groqHeaders("1", "1000", "1s", "1s"), t0)
	tr.Observe("fastcloud", "a", nil, t0)
	snap := tr.Snapshot()
	var got []string
	for _, u := range snap {
		got = append(got, u.Provider+"/"+u.Model)
	}
	if fmt.Sprint(got) != "[fastcloud/a fastcloud/z openrouter/b]" {
		t.Fatalf("order = %v", got)
	}
	if snap[1].Limits[0].Kind != Requests || snap[1].Limits[1].Kind != Tokens {
		t.Errorf("limits not sorted: %+v", snap[1].Limits)
	}
	snap[1].Limits[0].Remaining = 12345
	if u, _ := tr.Model("fastcloud", "z"); u.Limits[0].Remaining == 12345 {
		t.Error("snapshot shares memory with the tracker")
	}
}

func TestLimitHasReset(t *testing.T) {
	l := win(Requests, Day, 0, 10, time.Minute)
	if l.HasReset(t0) || !l.HasReset(t0.Add(time.Minute)) {
		t.Error("HasReset wrong around ResetsAt")
	}
	if (Limit{Remaining: 0}).HasReset(t0) {
		t.Error("no reset time can't have passed")
	}
}

// --- persistence ---

func openStore(t *testing.T, path string) *store.Store {
	t.Helper()
	s, err := store.Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestPersistenceRoundTrip(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "test.db")

	s := openStore(t, path)
	tr, err := Open(ctx, s, testConfig())
	if err != nil {
		t.Fatal(err)
	}
	tr.Observe("fastcloud", llama, groqHeaders("0", "11000", "2h3m", "7.66s"), t0)
	tr.ObserveError(rateLimited("gemini", "gemini-2.5-flash", 37*time.Second, nil), t0)
	tr.Observe("gemini", "unknown-model", nil, t0) // nothing to save
	if err := tr.Close(ctx); err != nil {
		t.Fatal(err)
	}
	s.Close()

	s = openStore(t, path)
	defer s.Close()
	tr, err = Open(ctx, s, testConfig())
	if err != nil {
		t.Fatal(err)
	}
	defer tr.Close(ctx)

	// The day window survives the restart.
	l, ok := tr.Exhausted("fastcloud", llama, t0.Add(time.Hour))
	if !ok || l.Window != Day || l.Kind != Requests || l.Max != 1000 {
		t.Fatalf("after restart: %+v ok=%v", l, ok)
	}
	if want := t0.Add(2*time.Hour + 3*time.Minute); !l.ResetsAt.Equal(want) {
		t.Errorf("ResetsAt = %v, want %v", l.ResetsAt, want)
	}
	u, _ := tr.Model("fastcloud", llama)
	if len(u.Limits) != 2 || !u.LastObserved.Equal(t0) {
		t.Fatalf("usage after restart = %+v", u)
	}
	// Sub-second resets are rounded up, never down.
	if tok := u.Limits[1]; !tok.ResetsAt.Equal(t0.Add(8*time.Second)) || tok.Remaining != 11000 || tok.Max != 12000 {
		t.Errorf("token window after restart = %+v", tok)
	}
	g, ok := tr.Exhausted("gemini", "gemini-2.5-flash", t0)
	if !ok || g.Max != Unknown || !g.ResetsAt.Equal(t0.Add(37*time.Second)) {
		t.Errorf("gemini 429 after restart = %+v ok=%v", g, ok)
	}
	if _, ok := tr.Model("gemini", "unknown-model"); ok {
		t.Error("a model with no windows should not be in the table")
	}
}

// blockingDB wraps a real store and holds every write until released.
type blockingDB struct {
	*store.Store
	release chan struct{}
	started chan struct{}
	once    sync.Once
}

func (b *blockingDB) WithTx(ctx context.Context, fn func(*sql.Tx) error) error {
	b.once.Do(func() { close(b.started) })
	select {
	case <-b.release:
	case <-ctx.Done():
		return ctx.Err()
	}
	return b.Store.WithTx(ctx, fn)
}

func TestObserveDoesNotWaitForTheDatabase(t *testing.T) {
	ctx := context.Background()
	s := openStore(t, filepath.Join(t.TempDir(), "test.db"))
	defer s.Close()
	db := &blockingDB{Store: s, release: make(chan struct{}), started: make(chan struct{})}
	tr, err := Open(ctx, db, testConfig())
	if err != nil {
		t.Fatal(err)
	}

	tr.Observe("fastcloud", llama, groqHeaders("999", "11000", "1m", "5s"), t0)
	<-db.started // the writer is now stuck inside a write

	done := make(chan struct{})
	go func() {
		for i := 0; i < 1000; i++ {
			rem := fmt.Sprint(998 - i%999)
			tr.Observe("fastcloud", llama, groqHeaders(rem, "11000", "1m", "5s"), t0.Add(time.Duration(i+1)*time.Millisecond))
			tr.Exhausted("fastcloud", llama, t0)
		}
		tr.Observe("fastcloud", llama, groqHeaders("0", "11000", "1m", "5s"), t0.Add(2*time.Second))
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Observe blocked on a stuck database write")
	}
	if l, ok := tr.Exhausted("fastcloud", llama, t0.Add(2*time.Second)); !ok || l.Remaining != 0 {
		t.Fatalf("memory not updated while the writer was stuck: %+v", l)
	}

	// Release the writer; Close must persist the latest value.
	close(db.release)
	if err := tr.Close(ctx); err != nil {
		t.Fatal(err)
	}
	var remaining int64
	if err := s.Reader().QueryRow(`SELECT remaining FROM rate_limits WHERE provider = 'fastcloud' AND kind = 'requests'`).Scan(&remaining); err != nil {
		t.Fatal(err)
	}
	if remaining != 0 {
		t.Errorf("stored remaining = %d, want the latest value 0", remaining)
	}
}

func TestCloseGivesUpOnAStuckDatabase(t *testing.T) {
	s := openStore(t, filepath.Join(t.TempDir(), "test.db"))
	defer s.Close()
	db := &blockingDB{Store: s, release: make(chan struct{}), started: make(chan struct{})}
	tr, err := Open(context.Background(), db, testConfig())
	if err != nil {
		t.Fatal(err)
	}
	tr.Observe("fastcloud", llama, groqHeaders("1", "1000", "1m", "5s"), t0)
	<-db.started
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if err := tr.Close(ctx); err == nil {
		t.Error("Close returned nil while the database was stuck")
	}
	close(db.release) // let the writer finish before the store closes
	if err := tr.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
}

// failingDB fails writes until told otherwise.
type failingDB struct {
	*store.Store
	mu   sync.Mutex
	fail bool
}

func (f *failingDB) WithTx(ctx context.Context, fn func(*sql.Tx) error) error {
	f.mu.Lock()
	fail := f.fail
	f.mu.Unlock()
	if fail {
		return errors.New("disk full")
	}
	return f.Store.WithTx(ctx, fn)
}

func TestFailedWriteIsRetried(t *testing.T) {
	ctx := context.Background()
	s := openStore(t, filepath.Join(t.TempDir(), "test.db"))
	defer s.Close()
	db := &failingDB{Store: s, fail: true}
	tr := newTracker(db, testConfig()) // no background writer: flush by hand
	tr.Observe("fastcloud", llama, groqHeaders("5", "1000", "1m", "5s"), t0)
	if err := tr.Flush(ctx); err == nil {
		t.Fatal("expected the write to fail")
	}
	db.mu.Lock()
	db.fail = false
	db.mu.Unlock()
	if err := tr.Flush(ctx); err != nil {
		t.Fatal(err)
	}
	var n int
	s.Reader().QueryRow(`SELECT COUNT(*) FROM rate_limits`).Scan(&n)
	if n != 2 {
		t.Errorf("rows = %d, want 2 after the retry", n)
	}
}

// Run with -race: many goroutines observing, checking and snapshotting at
// once, with the real background writer.
func TestConcurrentUse(t *testing.T) {
	ctx := context.Background()
	s := openStore(t, filepath.Join(t.TempDir(), "test.db"))
	defer s.Close()
	tr, err := Open(ctx, s, testConfig())
	if err != nil {
		t.Fatal(err)
	}

	var wg sync.WaitGroup
	for g := 0; g < 16; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			model := fmt.Sprintf("model-%d", g%4)
			for i := 0; i < 200; i++ {
				now := t0.Add(time.Duration(i) * time.Millisecond)
				switch i % 4 {
				case 0:
					tr.Observe("fastcloud", model, groqHeaders(fmt.Sprint(i), "5000", "1m", "5s"), now)
				case 1:
					tr.ObserveError(rateLimited("gemini", model, 0, nil), now)
				case 2:
					tr.Exhausted("fastcloud", model, now)
					tr.Exhausted("gemini", model, now)
				case 3:
					tr.Snapshot()
					tr.Model("fastcloud", model)
				}
			}
		}()
	}
	wg.Wait()
	if err := tr.Close(ctx); err != nil {
		t.Fatal(err)
	}
	var n int
	s.Reader().QueryRow(`SELECT COUNT(*) FROM rate_limits`).Scan(&n)
	if n != 4*3 { // 4 models × (groq requests/day + tokens/minute + gemini requests/minute)
		t.Errorf("rows = %d, want 12", n)
	}
}

// A window read from a usage endpoint lands like a header reading: stamped
// with the check time, counted as an observation, and able to exhaust the model.
func TestObserveWindow(t *testing.T) {
	tr := memTracker(t)
	reset := t0.Add(6 * time.Hour)
	tr.ObserveWindow("openrouter", "m:free", Limit{Kind: Requests, Window: Day, Remaining: 0, Max: 50, ResetsAt: reset, CheckedAt: t0.Add(-time.Hour)}, t0)

	u, ok := tr.Model("openrouter", "m:free")
	if !ok || len(u.Limits) != 1 || !u.LastObserved.Equal(t0) {
		t.Fatalf("usage = %+v ok=%v", u, ok)
	}
	if l := u.Limits[0]; !l.CheckedAt.Equal(t0) || l.Max != 50 || l.Window != Day {
		t.Errorf("window = %+v; want checked_at %v (the caller's CheckedAt is ignored)", l, t0)
	}
	if _, ok := tr.Exhausted("openrouter", "m:free", t0); !ok {
		t.Error("0 of 50 with a reset ahead should be exhausted")
	}

	// An older reading arriving late doesn't overwrite a newer one.
	tr.ObserveWindow("openrouter", "m:free", Limit{Kind: Requests, Window: Day, Remaining: 40, Max: 50, ResetsAt: reset}, t0.Add(-time.Minute))
	if _, ok := tr.Exhausted("openrouter", "m:free", t0); !ok {
		t.Error("an older reading replaced a newer one")
	}
	tr.ObserveWindow("openrouter", "m:free", Limit{Kind: Requests, Window: Day, Remaining: 40, Max: 50, ResetsAt: reset}, t0.Add(time.Minute))
	if _, ok := tr.Exhausted("openrouter", "m:free", t0.Add(time.Minute)); ok {
		t.Error("a newer reading should replace the old one")
	}
}

func TestObserveWindowIsPersisted(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "test.db")
	s := openStore(t, path)
	tr, err := Open(ctx, s, testConfig())
	if err != nil {
		t.Fatal(err)
	}
	tr.ObserveWindow("openrouter", "m:free", Limit{Kind: Requests, Window: Day, Remaining: 7, Max: 50, ResetsAt: t0.Add(time.Hour)}, t0)
	if err := tr.Close(ctx); err != nil {
		t.Fatal(err)
	}
	s.Close()

	s = openStore(t, path)
	defer s.Close()
	tr, err = Open(ctx, s, testConfig())
	if err != nil {
		t.Fatal(err)
	}
	defer tr.Close(ctx)
	u, ok := tr.Model("openrouter", "m:free")
	if !ok || len(u.Limits) != 1 || u.Limits[0].Remaining != 7 || !u.Limits[0].CheckedAt.Equal(t0) {
		t.Fatalf("after restart: %+v ok=%v", u, ok)
	}
}
