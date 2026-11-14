package store

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"sync"
	"testing"
)

func openTemp(t *testing.T) *Store {
	t.Helper()
	s, err := Open(context.Background(), filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

// migrationCount is how many migrations ship, so these tests don't need
// editing every time an issue adds one.
func migrationCount(t *testing.T) int {
	t.Helper()
	migs, err := loadMigrations()
	if err != nil {
		t.Fatal(err)
	}
	return len(migs)
}

func TestMigrationsApplyOnceAndPing(t *testing.T) {
	path := filepath.Join(t.TempDir(), "test.db")
	ctx := context.Background()
	for i := 0; i < 2; i++ { // second open must be a no-op
		s, err := Open(ctx, path)
		if err != nil {
			t.Fatal(err)
		}
		if err := s.Ping(ctx); err != nil {
			t.Fatal(err)
		}
		var n int
		if err := s.Reader().QueryRow(`SELECT COUNT(*) FROM schema_migrations`).Scan(&n); err != nil {
			t.Fatal(err)
		}
		if want := migrationCount(t); n != want {
			t.Fatalf("schema_migrations rows = %d, want %d", n, want)
		}
		s.Close()
	}
}

func TestPragmasOnEveryConnection(t *testing.T) {
	s := openTemp(t)
	ctx := context.Background()
	// Hold several reader connections open at once so the pool must create more than one.
	var conns []interface{ Close() error }
	for i := 0; i < 3; i++ {
		c, err := s.Reader().Conn(ctx)
		if err != nil {
			t.Fatal(err)
		}
		conns = append(conns, c)
		var fk, bt int
		var jm string
		c.QueryRowContext(ctx, `PRAGMA foreign_keys`).Scan(&fk)
		c.QueryRowContext(ctx, `PRAGMA busy_timeout`).Scan(&bt)
		c.QueryRowContext(ctx, `PRAGMA journal_mode`).Scan(&jm)
		if fk != 1 || bt != 5000 || jm != "wal" {
			t.Fatalf("conn %d: foreign_keys=%d busy_timeout=%d journal_mode=%s", i, fk, bt, jm)
		}
	}
	for _, c := range conns {
		c.Close()
	}
}

func TestReaderRejectsWrites(t *testing.T) {
	s := openTemp(t)
	if _, err := s.Reader().Exec(`INSERT INTO meta (key, value) VALUES ('x','y')`); err == nil {
		t.Fatal("write on reader succeeded")
	}
}

func TestConcurrentWritesNeverLock(t *testing.T) {
	s := openTemp(t)
	ctx := context.Background()
	if _, err := s.Writer().Exec(`CREATE TABLE t (id INTEGER PRIMARY KEY, n INTEGER)`); err != nil {
		t.Fatal(err)
	}

	const goroutines, each = 50, 40
	var wg sync.WaitGroup
	errs := make(chan error, goroutines*each)
	for g := 0; g < goroutines; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < each; i++ {
				errs <- s.WithTx(ctx, func(tx *sql.Tx) error {
					_, err := tx.ExecContext(ctx, `INSERT INTO t (n) VALUES (?)`, i)
					return err
				})
			}
		}()
	}
	// Read while the writers run: WAL lets readers proceed alongside the writer.
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 200; i++ {
			var n int
			if err := s.Reader().QueryRow(`SELECT COUNT(*) FROM t`).Scan(&n); err != nil {
				errs <- err
			}
		}
	}()
	wg.Wait()
	close(errs)

	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent access failed: %v", err)
		}
	}
	var n int
	if err := s.Reader().QueryRow(`SELECT COUNT(*) FROM t`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != goroutines*each {
		t.Fatalf("rows = %d, want %d", n, goroutines*each)
	}
}

func TestWithTxRollsBackOnError(t *testing.T) {
	s := openTemp(t)
	boom := errors.New("boom")
	err := s.WithTx(context.Background(), func(tx *sql.Tx) error {
		if _, err := tx.Exec(`INSERT INTO meta (key, value) VALUES ('k','v')`); err != nil {
			return err
		}
		return boom
	})
	if !errors.Is(err, boom) {
		t.Fatalf("err = %v, want boom", err)
	}
	var n int
	s.Reader().QueryRow(`SELECT COUNT(*) FROM meta WHERE key = 'k'`).Scan(&n)
	if n != 0 {
		t.Fatal("row survived rollback")
	}
}

func TestSecondLockFailsUntilReleased(t *testing.T) {
	path := filepath.Join(t.TempDir(), "router.lock")
	first, err := AcquireLock(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := AcquireLock(path); !errors.Is(err, ErrLocked) {
		t.Fatalf("second lock err = %v, want ErrLocked", err)
	}
	if err := first.Release(); err != nil {
		t.Fatal(err)
	}
	again, err := AcquireLock(path)
	if err != nil {
		t.Fatalf("lock after release: %v", err)
	}
	again.Release()
}
