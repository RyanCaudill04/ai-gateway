// Package requestlog records one row per chat request (#15) without ever
// making a response wait for the disk.
//
// A finished request is offered to Log, which only tries to put the row on a
// buffered channel. One writer goroutine reads the channel and inserts rows in
// batches, one transaction per batch, through the store's single writer
// connection (the same one the rate-limit tracker and API-key counters use).
//
// What is recorded is metadata only: who called, which model was asked for and
// which answered, token counts, latency, status and the error category. The
// Entry type has no field that could hold prompt or completion text, and a
// test fails if one is added (see privacy_test.go).
package requestlog

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"
)

// Unknown marks a token count the provider didn't report. It is stored as
// NULL, so "no counts" is never mistaken for "zero tokens".
const Unknown = -1

// Entry is one request log row. Every field is a fixed, typed piece of
// metadata. Do not add a field that could carry content (a message, a body, a
// map or `any`); the privacy test lists the allowed fields on purpose.
type Entry struct {
	Time             time.Time // when the request arrived
	Service          string    // calling service's key name
	RequestedModel   string    // the client's `model`: "fast", "groq/llama-3.3-70b-versatile"
	Provider         string    // provider used, or last attempted; "" if none
	Model            string    // that provider's own model ID
	FallbackAttempts int       // providers tried after the first; 0 = the first one answered (or none was tried)
	PromptTokens     int       // Unknown when not reported
	CompletionTokens int       // Unknown when not reported
	Latency          time.Duration
	Status           int    // HTTP status sent to the client (499: the client left first)
	ErrorType        string // the router's error category, never an error message
}

// Sizes. Chosen for a Raspberry Pi writing to an SD card.
//
//   - Batch of 100 rows: one transaction costs about the same whether it
//     holds 1 row or 100 (the SD card cost is the commit, not the insert), so
//     bigger batches mean fewer commits. 100 keeps a transaction to a few
//     milliseconds, so the single writer connection is never held long enough
//     to delay the rate-limit and API-key writers that share it.
//   - Flush every second: at the traffic a home gateway sees this is about one
//     small commit a second at most, and it bounds what a crash can lose.
//   - Buffer of 4096 rows (under 1 MiB): a request is one row, so this rides
//     out a disk stall of 40 s at 100 requests a second, far more than this
//     gateway will see on free-tier limits, before anything is dropped.
const (
	DefaultBatchSize  = 100
	DefaultFlushEvery = time.Second
	DefaultBuffer     = 4096
	// DefaultWarnEvery is how often at most the "rows dropped" warning is
	// logged, so a sustained overload gives a line a minute, not one per request.
	DefaultWarnEvery = time.Minute
)

// DB is the part of *store.Store the logger uses.
type DB interface {
	WithTx(ctx context.Context, fn func(*sql.Tx) error) error
}

// Options tune a Logger. The zero value uses the defaults above; the clock
// and ticker are injectable so tests don't sleep.
type Options struct {
	Buffer     int
	BatchSize  int
	FlushEvery time.Duration
	WarnEvery  time.Duration
	// Now is the clock (default time.Now).
	Now func() time.Time
	// Tick makes the flush timer: a channel of ticks and a stop function.
	// Default is a time.Ticker.
	Tick func(d time.Duration) (<-chan time.Time, func())
}

// Logger is a running request log. Create it with Open and stop it with Close.
type Logger struct {
	write      func(ctx context.Context, batch []Entry) error
	batchSize  int
	flushEvery time.Duration
	warnEvery  time.Duration
	now        func() time.Time
	tick       func(time.Duration) (<-chan time.Time, func())

	// mu makes "close the channel" and "send on the channel" exclusive, so a
	// Log that races with Close gets a drop instead of a send-on-closed-channel
	// panic. Log only ever takes the read side and never blocks while holding it.
	mu     sync.RWMutex
	closed bool
	ch     chan Entry

	ctx    context.Context // used by the writer's database calls
	cancel context.CancelFunc
	done   chan struct{}

	dropped   atomic.Uint64 // rows refused: queue full or logger closed
	failed    atomic.Uint64 // rows lost to a failed write
	sinceWarn atomic.Uint64 // dropped since the last warning
	lastWarn  atomic.Int64  // unix nanoseconds of the last warning
}

// Open starts the writer goroutine on db.
func Open(db DB, opts Options) *Logger {
	return newLogger(func(ctx context.Context, batch []Entry) error { return insertBatch(ctx, db, batch) }, opts)
}

func newLogger(write func(context.Context, []Entry) error, o Options) *Logger {
	if o.Buffer <= 0 {
		o.Buffer = DefaultBuffer
	}
	if o.BatchSize <= 0 {
		o.BatchSize = DefaultBatchSize
	}
	if o.FlushEvery <= 0 {
		o.FlushEvery = DefaultFlushEvery
	}
	if o.WarnEvery <= 0 {
		o.WarnEvery = DefaultWarnEvery
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.Tick == nil {
		o.Tick = func(d time.Duration) (<-chan time.Time, func()) {
			t := time.NewTicker(d)
			return t.C, t.Stop
		}
	}
	l := &Logger{
		write: write, batchSize: o.BatchSize, flushEvery: o.FlushEvery, warnEvery: o.WarnEvery,
		now: o.Now, tick: o.Tick,
		ch:   make(chan Entry, o.Buffer),
		done: make(chan struct{}),
	}
	l.ctx, l.cancel = context.WithCancel(context.Background())
	go l.run()
	return l
}

// Log queues one row. It never blocks and never fails: when the queue is full
// (the disk can't keep up) or the logger is closed, the row is dropped and
// counted, because a chat response must never wait for a log row.
func (l *Logger) Log(e Entry) {
	l.mu.RLock()
	if !l.closed {
		select {
		case l.ch <- e:
			l.mu.RUnlock()
			return
		default:
		}
	}
	l.mu.RUnlock()
	l.drop()
}

// Dropped is how many rows were refused because the queue was full or the
// logger already closed.
func (l *Logger) Dropped() uint64 { return l.dropped.Load() }

// WriteFailed is how many rows were lost because their batch could not be
// written even after one retry.
func (l *Logger) WriteFailed() uint64 { return l.failed.Load() }

// drop counts a refused row and, at most once per warnEvery, says so. The
// warning is emitted from here rather than from the writer because the writer
// is exactly what is stuck when rows are being dropped.
func (l *Logger) drop() {
	l.dropped.Add(1)
	l.sinceWarn.Add(1)
	l.reportDrops(false)
}

func (l *Logger) reportDrops(force bool) {
	if l.sinceWarn.Load() == 0 {
		return // nothing new to say
	}
	now := l.now().UnixNano()
	last := l.lastWarn.Load()
	if !force && now-last < int64(l.warnEvery) {
		return
	}
	if !l.lastWarn.CompareAndSwap(last, now) {
		return // another goroutine is reporting
	}
	if n := l.sinceWarn.Swap(0); n > 0 {
		slog.Warn("request log: queue full, rows dropped (the disk can't keep up)",
			"dropped_since_last_warning", n, "dropped_total", l.dropped.Load())
	}
}

// run is the writer goroutine: collect rows, write when the batch is full or
// the timer ticks, and on Close write whatever is left.
func (l *Logger) run() {
	defer close(l.done)
	tick, stop := l.tick(l.flushEvery)
	defer stop()
	batch := make([]Entry, 0, l.batchSize)
	for {
		select {
		case e, ok := <-l.ch:
			if !ok { // closed and drained
				l.flush(batch)
				l.reportDrops(true)
				return
			}
			batch = append(batch, e)
			if len(batch) >= l.batchSize {
				l.flush(batch)
				batch = batch[:0]
			}
		case <-tick:
			l.flush(batch)
			batch = batch[:0]
			l.reportDrops(false)
		}
	}
}

// flush writes a batch. A failed write is retried once and then the rows are
// dropped and counted: the 5 s busy_timeout has already waited out ordinary
// lock contention, so a second failure means something lasting (disk full,
// read-only volume), and holding the rows would only back the queue up until
// newer rows are dropped instead. One retry covers a one-off glitch.
func (l *Logger) flush(batch []Entry) {
	if len(batch) == 0 {
		return
	}
	err := l.write(l.ctx, batch)
	if err != nil && l.ctx.Err() == nil {
		err = l.write(l.ctx, batch)
	}
	if err != nil {
		l.failed.Add(uint64(len(batch)))
		// The error comes from the database or the context, never from a row's content.
		slog.Error("request log: write failed, rows lost", "rows", len(batch), "err", err)
	}
}

// Close stops accepting rows, writes every queued row, and returns once the
// writer has finished or ctx is done. It must run after the HTTP server has
// stopped (so no request is still producing rows) and before the database
// closes. If ctx runs out first, the unwritten rows are abandoned and counted
// in WriteFailed. Calling Close again, or Log after Close, is safe.
func (l *Logger) Close(ctx context.Context) error {
	l.mu.Lock()
	if !l.closed {
		l.closed = true
		close(l.ch)
	}
	l.mu.Unlock()

	select {
	case <-l.done:
		return nil
	case <-ctx.Done():
		l.cancel() // make a stuck database call return so the writer can stop
		return errors.Join(errors.New("request log: writer did not finish in time"), ctx.Err())
	}
}

// insertBatch writes the rows in one transaction.
func insertBatch(ctx context.Context, db DB, batch []Entry) error {
	return db.WithTx(ctx, func(tx *sql.Tx) error {
		stmt, err := tx.PrepareContext(ctx, `INSERT INTO request_log
			(ts, service, requested_model, provider, model, fallback_attempts,
			 prompt_tokens, completion_tokens, latency_ms, status, error_type)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`)
		if err != nil {
			return err
		}
		defer stmt.Close()
		for _, e := range batch {
			if _, err := stmt.ExecContext(ctx,
				e.Time.UTC().Format(timeFormat), e.Service, e.RequestedModel,
				nullText(e.Provider), nullText(e.Model), e.FallbackAttempts,
				nullCount(e.PromptTokens), nullCount(e.CompletionTokens),
				e.Latency.Milliseconds(), e.Status, nullText(e.ErrorType)); err != nil {
				return fmt.Errorf("insert: %w", err)
			}
		}
		return nil
	})
}

// timeFormat is the schema's timestamp convention (UTC, whole seconds).
const timeFormat = "2006-01-02T15:04:05Z"

func nullText(s string) sql.NullString { return sql.NullString{String: s, Valid: s != ""} }

func nullCount(n int) sql.NullInt64 { return sql.NullInt64{Int64: int64(n), Valid: n >= 0} }
