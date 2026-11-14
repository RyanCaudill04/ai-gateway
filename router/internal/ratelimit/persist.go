package ratelimit

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/RyanCaudill04/ai-gateway/router/internal/config"
)

// DB is the part of *store.Store the tracker uses: writes go through the
// single writer, the startup load through the reader pool.
type DB interface {
	WithTx(ctx context.Context, fn func(*sql.Tx) error) error
	Reader() *sql.DB
}

// timeFormat is the schema's timestamp convention (UTC, whole seconds).
const timeFormat = "2006-01-02T15:04:05Z"

// Open loads the saved windows from db and starts the background writer.
// cfg tells it how to read each provider's headers (by base_url); nil reads
// every provider the OpenAI way. A nil db gives a memory-only tracker, for
// tests. Call Close on shutdown to write what's still pending.
func Open(ctx context.Context, db DB, cfg *config.Config) (*Tracker, error) {
	t := newTracker(db, cfg)
	if db == nil {
		close(t.done)
		return t, nil
	}
	if err := t.load(ctx); err != nil {
		return nil, fmt.Errorf("load rate limits: %w", err)
	}
	go t.writer()
	return t, nil
}

func (t *Tracker) load(ctx context.Context) error {
	rows, err := t.db.Reader().QueryContext(ctx,
		`SELECT provider, model, kind, window, remaining, "limit", resets_at, checked_at FROM rate_limits`)
	if err != nil {
		return err
	}
	defer rows.Close()
	t.mu.Lock()
	defer t.mu.Unlock()
	for rows.Next() {
		var (
			provider, model, kind, window, checked string
			remaining, size                        sql.NullInt64
			resets                                 sql.NullString
		)
		if err := rows.Scan(&provider, &model, &kind, &window, &remaining, &size, &resets, &checked); err != nil {
			return err
		}
		l := Limit{Kind: Kind(kind), Window: Window(window), Remaining: Unknown, Max: Unknown}
		if remaining.Valid {
			l.Remaining = remaining.Int64
		}
		if size.Valid {
			l.Max = size.Int64
		}
		var perr error
		if l.CheckedAt, perr = time.Parse(timeFormat, checked); perr == nil && resets.Valid {
			l.ResetsAt, perr = time.Parse(timeFormat, resets.String)
		}
		if perr != nil {
			slog.Warn("rate limits: skipping unreadable row", "provider", provider, "model", model, "err", perr)
			continue
		}
		st := t.stateLocked(provider, model, l.CheckedAt)
		st.limits[slot{l.Kind, l.Window}] = l
	}
	return rows.Err()
}

// writer saves pending windows whenever there are some. Updates that arrive
// while a write is in progress pile up in pending (one entry per window, the
// latest value) and go out together in the next transaction, so under load
// the number of writes stays small.
func (t *Tracker) writer() {
	defer close(t.done)
	for {
		select {
		case <-t.stop:
			return
		case <-t.wake:
		}
		if err := t.Flush(context.Background()); err != nil {
			slog.Warn("rate limits: write failed; will retry on the next update", "err", err)
		}
	}
}

// Flush writes every pending window now. On failure the unwritten values go
// back to pending (unless a newer value arrived meanwhile).
func (t *Tracker) Flush(ctx context.Context) error {
	if t.db == nil {
		return nil
	}
	t.flushMu.Lock()
	defer t.flushMu.Unlock()

	t.pmu.Lock()
	batch := t.pending
	t.pending = map[pendingKey]Limit{}
	t.pmu.Unlock()
	if len(batch) == 0 {
		return nil
	}

	err := t.db.WithTx(ctx, func(tx *sql.Tx) error {
		stmt, err := tx.PrepareContext(ctx, `INSERT INTO rate_limits
			(provider, model, kind, window, remaining, "limit", resets_at, checked_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT (provider, model, kind, window) DO UPDATE SET
				remaining = excluded.remaining, "limit" = excluded."limit",
				resets_at = excluded.resets_at, checked_at = excluded.checked_at`)
		if err != nil {
			return err
		}
		defer stmt.Close()
		for k, l := range batch {
			if _, err := stmt.ExecContext(ctx, k.provider, k.model, string(k.kind), string(k.window),
				nullCount(l.Remaining), nullCount(l.Max), resetText(l.ResetsAt),
				l.CheckedAt.UTC().Truncate(time.Second).Format(timeFormat)); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.pmu.Lock()
		for k, l := range batch {
			if _, newer := t.pending[k]; !newer {
				t.pending[k] = l
			}
		}
		t.pmu.Unlock()
	}
	return err
}

// Close stops the background writer and writes what's still pending. Updates
// made after Close are kept in memory only. If the process is killed instead
// of stopped, the unwritten updates (normally a few milliseconds' worth) are
// lost; the next response from each model reports them again.
func (t *Tracker) Close(ctx context.Context) error {
	t.closeOnce.Do(func() { close(t.stop) })
	select {
	case <-t.done:
	case <-ctx.Done():
		return errors.Join(errors.New("rate limits: writer did not stop in time"), ctx.Err())
	}
	return t.Flush(ctx)
}

func nullCount(n int64) sql.NullInt64 {
	return sql.NullInt64{Int64: n, Valid: n != Unknown}
}

// resetText rounds a reset time up to the whole second, so the stored copy
// never says a window is full again before it really is.
func resetText(at time.Time) sql.NullString {
	if at.IsZero() {
		return sql.NullString{}
	}
	at = at.UTC()
	if r := at.Truncate(time.Second); !r.Equal(at) {
		at = r.Add(time.Second)
	}
	return sql.NullString{String: at.Format(timeFormat), Valid: true}
}
