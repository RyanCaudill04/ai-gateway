package apikeys

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/RyanCaudill04/ai-gateway/router/internal/store"
)

var (
	// ErrExists: an active key with that name already exists.
	ErrExists = errors.New("an active key with that name already exists; revoke it first to replace it")
	// ErrNotFound: no active key with that name.
	ErrNotFound = errors.New("no active key with that name")
	// ErrInvalid: the key is unknown or revoked.
	ErrInvalid = errors.New("invalid api key")
)

// Key is a stored key. The key itself is never kept, only its hash.
type Key struct {
	Name      string
	Prefix    string
	DailyCap  *int // nil = no cap
	CreatedAt time.Time
	RevokedAt *time.Time // nil = active
}

// Store reads and writes keys. Every check goes to the database, so a new or
// revoked key applies on the next request with no restart.
type Store struct{ db *store.Store }

func NewStore(db *store.Store) *Store { return &Store{db: db} }

func now() string { return time.Now().UTC().Format(time.RFC3339) }

// Create makes a key for name and returns it. This is the only time the key
// exists in plain text. Creating a name whose key was revoked replaces that
// key (rotation); creating an active name is an error.
func (s *Store) Create(ctx context.Context, name string, dailyCap *int) (string, error) {
	key, err := Generate(name)
	if err != nil {
		return "", err
	}
	err = s.db.WithTx(ctx, func(tx *sql.Tx) error {
		var revoked sql.NullString
		err := tx.QueryRowContext(ctx, `SELECT revoked_at FROM api_keys WHERE name = ?`, name).Scan(&revoked)
		switch {
		case errors.Is(err, sql.ErrNoRows):
			_, err = tx.ExecContext(ctx,
				`INSERT INTO api_keys (name, prefix, key_hash, daily_cap, created_at) VALUES (?, ?, ?, ?, ?)`,
				name, displayPrefix(name, key), Hash(key), dailyCap, now())
			return err
		case err != nil:
			return err
		case !revoked.Valid:
			return ErrExists
		}
		_, err = tx.ExecContext(ctx,
			`UPDATE api_keys SET prefix = ?, key_hash = ?, daily_cap = ?, created_at = ?, revoked_at = NULL WHERE name = ?`,
			displayPrefix(name, key), Hash(key), dailyCap, now(), name)
		return err
	})
	if err != nil {
		return "", err
	}
	return key, nil
}

// List returns every key, active and revoked, by name.
func (s *Store) List(ctx context.Context) ([]Key, error) {
	rows, err := s.db.Reader().QueryContext(ctx,
		`SELECT name, prefix, daily_cap, created_at, revoked_at FROM api_keys ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []Key
	for rows.Next() {
		var (
			k       Key
			cap     sql.NullInt64
			created string
			revoked sql.NullString
		)
		if err := rows.Scan(&k.Name, &k.Prefix, &cap, &created, &revoked); err != nil {
			return nil, err
		}
		if cap.Valid {
			n := int(cap.Int64)
			k.DailyCap = &n
		}
		k.CreatedAt, _ = time.Parse(time.RFC3339, created)
		if revoked.Valid {
			t, _ := time.Parse(time.RFC3339, revoked.String)
			k.RevokedAt = &t
		}
		out = append(out, k)
	}
	return out, rows.Err()
}

// Revoke stops a key from working. It takes effect on the next request.
func (s *Store) Revoke(ctx context.Context, name string) error {
	res, err := s.db.Writer().ExecContext(ctx,
		`UPDATE api_keys SET revoked_at = ? WHERE name = ? AND revoked_at IS NULL`, now(), name)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// Verify returns the name of the service a key belongs to, or ErrInvalid if
// the key is unknown or revoked.
func (s *Store) Verify(ctx context.Context, key string) (string, error) {
	var name string
	err := s.db.Reader().QueryRowContext(ctx,
		`SELECT name FROM api_keys WHERE key_hash = ? AND revoked_at IS NULL`, Hash(key)).Scan(&name)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrInvalid
	}
	return name, err
}
