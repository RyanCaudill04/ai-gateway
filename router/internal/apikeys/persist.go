package apikeys

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"
)

// Why counts are written in the background rather than on each request:
//
// Memory is the source of truth for today's counts. That is safe because the
// router is the only process that writes the database (store.AcquireLock),
// so nothing else can change a count behind its back. Admitting a request is
// then a lock, a compare and an increment: exact under concurrency, and it
// never waits on the single writer connection, which the request log and the
// rate-limit tracker also use.
//
// The writer saves counts as soon as there are new ones, batching whatever
// piled up while the previous write ran, and Close writes the rest on a
// normal shutdown. What a crash (kill -9, power loss) can lose is the counts
// admitted since the last write: normally a few milliseconds' worth, so after
// such a crash a key can make that many extra requests that day. For a cap
// that shares free-tier quota between my own services, that is a fair price
// for never putting a disk write in front of a response.

// writer saves pending counts whenever there are some.
func (s *Store) writer() {
	defer close(s.done)
	for {
		select {
		case <-s.stop:
			return
		case <-s.wake:
		}
		if err := s.Flush(context.Background()); err != nil {
			slog.Warn("api keys: saving usage counts failed; will retry on the next request", "err", err)
		}
	}
}

// Flush writes every pending count now. On failure the unwritten counts go
// back to pending (unless a newer count arrived meanwhile).
func (s *Store) Flush(ctx context.Context) error {
	s.flushMu.Lock()
	defer s.flushMu.Unlock()

	s.mu.Lock()
	batch := s.pending
	s.pending = map[pendingKey]int64{}
	s.mu.Unlock()
	if len(batch) == 0 {
		return nil
	}

	err := s.db.WithTx(ctx, func(tx *sql.Tx) error {
		// MAX keeps a stored count from ever going down, whatever order
		// writes land in.
		stmt, err := tx.PrepareContext(ctx, `
			INSERT INTO api_key_usage (key_id, day, requests) VALUES (?, ?, ?)
			ON CONFLICT (key_id, day) DO UPDATE SET requests = MAX(requests, excluded.requests)`)
		if err != nil {
			return err
		}
		defer stmt.Close()
		for k, n := range batch {
			if _, err := stmt.ExecContext(ctx, k.id, k.day, n); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		s.mu.Lock()
		for k, n := range batch {
			if _, newer := s.pending[k]; !newer {
				s.pending[k] = n
			}
		}
		s.mu.Unlock()
	}
	return err
}

// Close stops the background writer and writes what's still pending. Call it
// after the HTTP server has stopped; requests admitted after Close are
// counted in memory only.
func (s *Store) Close(ctx context.Context) error {
	s.closeOnce.Do(func() { close(s.stop) })
	select {
	case <-s.done:
	case <-ctx.Done():
		return errors.Join(errors.New("api keys: writer did not stop in time"), ctx.Err())
	}
	return s.Flush(ctx)
}
