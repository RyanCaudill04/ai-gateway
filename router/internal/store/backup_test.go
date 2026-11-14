package store

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

func TestBackupDuringConcurrentWrites(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "gateway.db")
	ctx := context.Background()
	s, err := Open(ctx, dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if _, err := s.Writer().Exec(`CREATE TABLE t (id INTEGER PRIMARY KEY, n INTEGER)`); err != nil {
		t.Fatal(err)
	}

	// Writers keep going for the whole backup, so the copy is taken mid-stream.
	const goroutines, each = 20, 100
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

	dest := filepath.Join(dir, "backups", "gateway-test.db")
	backupErr := make(chan error, 1)
	go func() {
		backupErr <- Backup(ctx, dbPath, dest)
	}()
	if err := <-backupErr; err != nil {
		t.Fatalf("backup during writes: %v", err)
	}

	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("write failed while backup ran: %v", err)
		}
	}

	// The backup must open cleanly and be internally consistent.
	b, err := sql.Open("sqlite", "file:"+dest+"?mode=ro")
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()

	var check string
	if err := b.QueryRow(`PRAGMA integrity_check`).Scan(&check); err != nil {
		t.Fatal(err)
	}
	if check != "ok" {
		t.Fatalf("integrity_check = %q, want ok", check)
	}

	var migrations int
	if err := b.QueryRow(`SELECT COUNT(*) FROM schema_migrations`).Scan(&migrations); err != nil {
		t.Fatalf("schema_migrations missing from backup: %v", err)
	}
	if migrations != 1 {
		t.Fatalf("schema_migrations rows = %d, want 1", migrations)
	}

	// Every row in the backup must be a row the writers committed; the backup
	// can have fewer rows than the final count but never more.
	var backedUp int
	if err := b.QueryRow(`SELECT COUNT(*) FROM t`).Scan(&backedUp); err != nil {
		t.Fatal(err)
	}
	if backedUp > goroutines*each {
		t.Fatalf("backup has %d rows, more than the %d written", backedUp, goroutines*each)
	}
}

func TestBackupRefusesExistingDestination(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "gateway.db")
	s, err := Open(context.Background(), dbPath)
	if err != nil {
		t.Fatal(err)
	}
	s.Close()

	dest := filepath.Join(dir, "existing.db")
	original := []byte("do not overwrite me")
	if err := os.WriteFile(dest, original, 0o600); err != nil {
		t.Fatal(err)
	}

	err = Backup(context.Background(), dbPath, dest)
	if !errors.Is(err, ErrBackupExists) {
		t.Fatalf("err = %v, want ErrBackupExists", err)
	}
	got, err := os.ReadFile(dest)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(original) {
		t.Fatal("existing destination was overwritten")
	}
}

func TestBackupMissingSourceCreatesNothing(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "missing.db")
	dest := filepath.Join(dir, "backups", "out.db")

	err := Backup(context.Background(), dbPath, dest)
	if !errors.Is(err, ErrNoDatabase) {
		t.Fatalf("err = %v, want ErrNoDatabase", err)
	}
	if _, err := os.Stat(dbPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("source database was created: stat err = %v", err)
	}
	if _, err := os.Stat(dest); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("destination was created: stat err = %v", err)
	}
	if _, err := os.Stat(filepath.Dir(dest)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("backup directory was created: stat err = %v", err)
	}
}

func TestBackupCreatesMissingDirectory(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "gateway.db")
	s, err := Open(context.Background(), dbPath)
	if err != nil {
		t.Fatal(err)
	}
	s.Close()

	dest := filepath.Join(dir, "a", "b", "copy.db")
	if err := Backup(context.Background(), dbPath, dest); err != nil {
		t.Fatalf("backup into new directory: %v", err)
	}
	if _, err := os.Stat(dest); err != nil {
		t.Fatalf("backup file missing: %v", err)
	}
}
