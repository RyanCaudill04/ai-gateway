package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"

	_ "modernc.org/sqlite" // pure-Go driver, same as store.go
)

// ErrNoDatabase means there is no database file at the source path.
var ErrNoDatabase = errors.New("database file does not exist")

// ErrBackupExists means the backup destination is already there. VACUUM INTO
// never overwrites, and neither does Backup.
var ErrBackupExists = errors.New("backup destination already exists")

// Backup writes a consistent copy of the database at dbPath to destPath using
// VACUUM INTO. It is meant to run in a second process while a router serves
// the same database, so it takes no instance lock, runs no migrations, and
// opens the source read-only. The destination is a single self-contained file
// (no -wal needed), and the source's pending WAL writes are included.
func Backup(ctx context.Context, dbPath, destPath string) error {
	// Checked first: opening a missing path would create an empty database.
	if _, err := os.Stat(dbPath); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return fmt.Errorf("%w: %s", ErrNoDatabase, dbPath)
		}
		return fmt.Errorf("stat database %s: %w", dbPath, err)
	}
	if _, err := os.Stat(destPath); err == nil {
		return fmt.Errorf("%w: %s", ErrBackupExists, destPath)
	} else if !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("stat backup destination %s: %w", destPath, err)
	}
	if err := os.MkdirAll(filepath.Dir(destPath), 0o755); err != nil {
		return fmt.Errorf("create backup directory: %w", err)
	}

	// mode=ro is the point of this connection: it cannot write the live file.
	// query_only(1) is not enough, because SQLite counts VACUUM INTO as a write
	// and refuses it. busy_timeout matches the router's connections so a
	// checkpoint in progress is waited out instead of failing the backup.
	q := url.Values{}
	q.Set("mode", "ro")
	q.Add("_pragma", "busy_timeout(5000)")
	db, err := sql.Open("sqlite", "file:"+dbPath+"?"+q.Encode())
	if err != nil {
		return fmt.Errorf("open database %s: %w", dbPath, err)
	}
	defer db.Close()

	if _, err := db.ExecContext(ctx, `VACUUM INTO ?`, destPath); err != nil {
		// Don't leave a partial file that could be mistaken for a good backup.
		os.Remove(destPath)
		return fmt.Errorf("vacuum into %s: %w", destPath, err)
	}
	return nil
}
