// Package store owns the router's SQLite database.
//
// SQLite allows many readers but only one writer at a time. To avoid
// SQLITE_BUSY ("database is locked") the store opens two pools on the same
// file: a writer pool limited to one connection (writes queue in Go, not in
// SQLite) and a reader pool with several connections (WAL lets readers run
// alongside the writer).
package store

import (
	"context"
	"database/sql"
	"fmt"
	"net/url"

	_ "modernc.org/sqlite" // pure-Go driver, so the image can build with CGO_ENABLED=0
)

// Store holds the writer and reader pools.
type Store struct {
	writer *sql.DB
	reader *sql.DB
}

// Open opens (creating if needed) the database at path and applies any
// pending migrations. Callers should hold the instance lock first (see
// AcquireLock) so two routers never share one database.
func Open(ctx context.Context, path string) (*Store, error) {
	// Pragmas go in the DSN so every connection the pool opens gets them;
	// a one-off db.Exec("PRAGMA ...") would only reach one connection.
	w, err := sql.Open("sqlite", dsn(path, true))
	if err != nil {
		return nil, fmt.Errorf("open writer: %w", err)
	}
	w.SetMaxOpenConns(1)

	r, err := sql.Open("sqlite", dsn(path, false))
	if err != nil {
		w.Close()
		return nil, fmt.Errorf("open reader: %w", err)
	}
	r.SetMaxOpenConns(4)

	s := &Store{writer: w, reader: r}
	if err := w.PingContext(ctx); err != nil {
		s.Close()
		return nil, fmt.Errorf("open database %s: %w", path, err)
	}
	if err := migrate(ctx, w); err != nil {
		s.Close()
		return nil, fmt.Errorf("migrate: %w", err)
	}
	return s, nil
}

// dsn builds the connection string. The writer uses _txlock=immediate so
// db.BeginTx issues BEGIN IMMEDIATE: it takes the write lock up front, so a
// transaction never fails halfway when upgrading from a read to a write.
func dsn(path string, writer bool) string {
	q := url.Values{}
	q.Add("_pragma", "journal_mode(WAL)")
	q.Add("_pragma", "busy_timeout(5000)")
	q.Add("_pragma", "synchronous(NORMAL)")
	q.Add("_pragma", "foreign_keys(1)")
	if writer {
		q.Set("_txlock", "immediate")
	} else {
		q.Add("_pragma", "query_only(1)") // a stray write on the reader fails loudly
	}
	return "file:" + path + "?" + q.Encode()
}

// Writer returns the single-connection pool. All writes go through it.
func (s *Store) Writer() *sql.DB { return s.writer }

// Reader returns the multi-connection, read-only pool.
func (s *Store) Reader() *sql.DB { return s.reader }

// WithTx runs fn in a BEGIN IMMEDIATE transaction on the writer, committing
// if fn returns nil and rolling back otherwise.
func (s *Store) WithTx(ctx context.Context, fn func(*sql.Tx) error) error {
	tx, err := s.writer.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	if err := fn(tx); err != nil {
		tx.Rollback()
		return err
	}
	return tx.Commit()
}

// Ping checks the database can be read. Used by /healthz.
func (s *Store) Ping(ctx context.Context) error {
	var n int
	if err := s.reader.QueryRowContext(ctx, `SELECT COUNT(*) FROM schema_migrations`).Scan(&n); err != nil {
		return fmt.Errorf("database not readable: %w", err)
	}
	return nil
}

// Close closes both pools.
func (s *Store) Close() error {
	werr := s.writer.Close()
	rerr := s.reader.Close()
	if werr != nil {
		return werr
	}
	return rerr
}
