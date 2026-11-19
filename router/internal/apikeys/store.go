package apikeys

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"sync"
	"time"
)

// DB is the part of *store.Store the key store uses: writes go through the
// single writer, the startup load through the reader pool.
type DB interface {
	WithTx(ctx context.Context, fn func(*sql.Tx) error) error
	Reader() *sql.DB
}

var (
	// ErrExists: an active key already has this name. Revoke it first.
	ErrExists = errors.New("an active key with this name already exists")
	// ErrNotFound: no key, active or revoked, has this name.
	ErrNotFound = errors.New("no key with this name")
	// ErrInvalid is wrapped by Create's errors for a malformed request.
	ErrInvalid = errors.New("invalid key")
)

// timeFormat is the schema's timestamp convention (UTC, whole seconds).
const timeFormat = "2006-01-02T15:04:05Z"

// dayFormat is api_key_usage.day.
const dayFormat = "2006-01-02"

// Info describes a key without revealing it: never the key, never its hash.
type Info struct {
	Name   string
	Prefix string // e.g. "sk-mc-bot-abcd"
	// DailyCap is the most requests the key may make per UTC day; 0 = no cap.
	DailyCap int64
	// UsedToday is how many requests the key made today (UTC).
	UsedToday int64
	CreatedAt string // UTC, e.g. 2026-11-13T12:00:00Z
	RevokedAt string // "" = active
}

// Usage is a key's daily cap and how much of it is used, for GET /v1/usage
// (#17).
type Usage struct {
	DailyCap int64 // 0 = no cap
	// Used is the requests admitted today (UTC), including the one asking.
	Used int64
	// ResetsAt is the next midnight UTC, when Used goes back to 0.
	ResetsAt time.Time
}

// NewKey is what Create stores. The CLI builds it with Generate; the plain key
// is not part of it.
type NewKey struct {
	Name     string
	Prefix   string
	Hash     [HashSize]byte
	DailyCap int64 // 0 = no cap
}

type entry struct {
	id   int64
	hash [HashSize]byte
	info Info // UsedToday is not kept here; see used/day
	// used is the count for day. When a request arrives on a later day the
	// count starts again from 0: that is the whole daily reset.
	day  string
	used int64
}

type pendingKey struct {
	id  int64
	day string
}

// Store holds every key (active and revoked) and today's counts in memory.
//
// One mutex guards the keys and the counters. Checking the cap and counting
// the request happen under it in one step, so two requests racing for the
// last slot can't both get it. The critical section is a map lookup and an
// increment; no I/O ever happens while it is held.
type Store struct {
	db  DB
	now func() time.Time

	// writeMu serializes Create and Revoke so memory always ends in the same
	// state as the database. It's separate from mu so a slow write never
	// blocks request-time lookups.
	writeMu sync.Mutex

	mu     sync.Mutex
	byName map[string]*entry
	// byHash holds revoked keys too, so a request with one can be logged as
	// "revoked" (by name) rather than "unknown". It's still refused.
	byHash  map[[HashSize]byte]*entry
	pending map[pendingKey]int64 // counts not yet written; latest wins

	flushMu   sync.Mutex // one Flush at a time
	wake      chan struct{}
	stop      chan struct{}
	done      chan struct{}
	closeOnce sync.Once
}

// Open loads every key and today's counts, and starts the background writer
// that saves counts. Call Close on shutdown to write what's still pending.
func Open(ctx context.Context, db DB) (*Store, error) { return open(ctx, db, time.Now) }

func open(ctx context.Context, db DB, now func() time.Time) (*Store, error) {
	s := &Store{
		db: db, now: now,
		byName:  map[string]*entry{},
		byHash:  map[[HashSize]byte]*entry{},
		pending: map[pendingKey]int64{},
		wake:    make(chan struct{}, 1),
		stop:    make(chan struct{}),
		done:    make(chan struct{}),
	}
	if err := s.load(ctx); err != nil {
		return nil, fmt.Errorf("load api keys: %w", err)
	}
	go s.writer()
	return s, nil
}

func (s *Store) load(ctx context.Context) error {
	today := s.now().UTC().Format(dayFormat)
	rows, err := s.db.Reader().QueryContext(ctx, `
		SELECT k.id, k.name, k.prefix, k.key_hash, k.daily_cap, k.created_at, k.revoked_at, u.requests
		FROM api_keys k
		LEFT JOIN api_key_usage u ON u.key_id = k.id AND u.day = ?`, today)
	if err != nil {
		return err
	}
	defer rows.Close()
	s.mu.Lock()
	defer s.mu.Unlock()
	for rows.Next() {
		var (
			e        = &entry{day: today}
			hash     []byte
			dailyCap sql.NullInt64
			revoked  sql.NullString
			used     sql.NullInt64
		)
		if err := rows.Scan(&e.id, &e.info.Name, &e.info.Prefix, &hash, &dailyCap, &e.info.CreatedAt, &revoked, &used); err != nil {
			return err
		}
		if len(hash) != HashSize {
			// Never valid; skip rather than refuse to start. It can't match any key.
			slog.Error("api keys: skipping a key with a malformed hash", "name", e.info.Name)
			continue
		}
		copy(e.hash[:], hash)
		e.info.DailyCap = dailyCap.Int64
		e.info.RevokedAt = revoked.String
		e.used = used.Int64
		s.byName[e.info.Name] = e
		s.byHash[e.hash] = e
	}
	return rows.Err()
}

// today rolls e's counter over to the current UTC day if a new one started.
// Callers hold mu.
func (e *entry) today(day string) {
	if e.day != day {
		e.day, e.used = day, 0
	}
}

func nextMidnight(now time.Time) time.Time {
	y, m, d := now.UTC().Date()
	return time.Date(y, m, d+1, 0, 0, 0, 0, time.UTC)
}

// verdict is what admit decided about a request.
type verdict int

const (
	unknownKey verdict = iota
	revokedKey
	overCap
	admitted
)

// admit looks up a presented key's hash and, if it is active and under its
// cap, counts the request. Checking and counting are one step under mu.
func (s *Store) admit(hash [HashSize]byte) (verdict, Info, time.Time) {
	now := s.now()
	day := now.UTC().Format(dayFormat)

	s.mu.Lock()
	e, ok := s.byHash[hash]
	if !ok {
		s.mu.Unlock()
		return unknownKey, Info{}, time.Time{}
	}
	if e.info.RevokedAt != "" {
		info := e.info
		s.mu.Unlock()
		return revokedKey, info, time.Time{}
	}
	e.today(day)
	if e.info.DailyCap > 0 && e.used >= e.info.DailyCap {
		info := e.info
		info.UsedToday = e.used
		s.mu.Unlock()
		return overCap, info, nextMidnight(now)
	}
	e.used++
	s.pending[pendingKey{e.id, day}] = e.used
	info := e.info
	info.UsedToday = e.used
	s.mu.Unlock()

	// Wake the writer without ever blocking: if it is already due to run,
	// it will pick this count up too.
	select {
	case s.wake <- struct{}{}:
	default:
	}
	return admitted, info, nextMidnight(now)
}

// Usage returns name's cap and today's count, for GET /v1/usage (#17). ok is
// false for an unknown name. A revoked key still reports (its count stops).
func (s *Store) Usage(name string) (u Usage, ok bool) {
	now := s.now()
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok := s.byName[name]
	if !ok {
		return Usage{}, false
	}
	e.today(now.UTC().Format(dayFormat))
	return Usage{DailyCap: e.info.DailyCap, Used: e.used, ResetsAt: nextMidnight(now)}, true
}

// List describes every key, active and revoked, sorted by name.
func (s *Store) List() []Info {
	day := s.now().UTC().Format(dayFormat)
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Info, 0, len(s.byName))
	for _, e := range s.byName {
		e.today(day)
		info := e.info
		info.UsedToday = e.used
		out = append(out, info)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// Info describes the key called name.
func (s *Store) Info(name string) (Info, bool) {
	day := s.now().UTC().Format(dayFormat)
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok := s.byName[name]
	if !ok {
		return Info{}, false
	}
	e.today(day)
	info := e.info
	info.UsedToday = e.used
	return info, true
}

// Create stores a new key and makes it usable on the very next request.
//
//   - An active key with the same name: ErrExists. Two live keys for one
//     service would split its attribution, and silently replacing one would
//     break whatever uses it. Revoke the old key first.
//   - A revoked key with the same name: its row is reused with the new hash,
//     prefix and cap, and a new created_at. This is how a key is rotated
//     (revoke, then create). The service keeps one row, so today's count
//     carries over: rotating a key doesn't reset its cap. The old key's hash
//     is gone, so the old key can never work again.
func (s *Store) Create(ctx context.Context, k NewKey) (Info, error) {
	if err := ValidateName(k.Name); err != nil {
		return Info{}, err
	}
	if !validPrefix(k.Name, k.Prefix) {
		return Info{}, fmt.Errorf("%w: the prefix doesn't match the name", ErrInvalid)
	}
	if k.DailyCap < 0 {
		return Info{}, fmt.Errorf("%w: the daily cap can't be negative", ErrInvalid)
	}
	var zero [HashSize]byte
	if k.Hash == zero {
		return Info{}, fmt.Errorf("%w: missing key hash", ErrInvalid)
	}

	s.writeMu.Lock()
	defer s.writeMu.Unlock()

	s.mu.Lock()
	old, exists := s.byName[k.Name]
	if exists && old.info.RevokedAt == "" {
		s.mu.Unlock()
		return Info{}, ErrExists
	}
	if _, clash := s.byHash[k.Hash]; clash {
		// Impossible for a key from Generate (256 random bits); refuse anyway.
		s.mu.Unlock()
		return Info{}, fmt.Errorf("%w: this key is already registered", ErrInvalid)
	}
	s.mu.Unlock()

	info := Info{Name: k.Name, Prefix: k.Prefix, DailyCap: k.DailyCap, CreatedAt: s.now().UTC().Format(timeFormat)}
	var id int64
	err := s.db.WithTx(ctx, func(tx *sql.Tx) error {
		if exists {
			id = old.id
			_, err := tx.ExecContext(ctx, `
				UPDATE api_keys SET prefix = ?, key_hash = ?, daily_cap = ?, created_at = ?, revoked_at = NULL
				WHERE id = ?`,
				k.Prefix, k.Hash[:], nullCap(k.DailyCap), info.CreatedAt, id)
			return err
		}
		return tx.QueryRowContext(ctx, `
			INSERT INTO api_keys (name, prefix, key_hash, daily_cap, created_at)
			VALUES (?, ?, ?, ?, ?) RETURNING id`,
			k.Name, k.Prefix, k.Hash[:], nullCap(k.DailyCap), info.CreatedAt).Scan(&id)
	})
	if err != nil {
		return Info{}, fmt.Errorf("save key %s: %w", k.Name, err)
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	e := &entry{id: id, hash: k.Hash, info: info}
	if exists {
		delete(s.byHash, old.hash)
		e.day, e.used = old.day, old.used // the count carries over (see above)
	}
	s.byName[k.Name] = e
	s.byHash[k.Hash] = e
	e.today(s.now().UTC().Format(dayFormat))
	out := e.info
	out.UsedToday = e.used
	return out, nil
}

// Revoke stops name's key from working, from the very next request. Its row
// and usage stay, so `keys list` still shows it. Revoking a key that is
// already revoked changes nothing and reports changed = false: the state the
// caller asked for already holds.
func (s *Store) Revoke(ctx context.Context, name string) (info Info, changed bool, err error) {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()

	s.mu.Lock()
	e, ok := s.byName[name]
	var id int64
	if ok {
		id, info = e.id, e.info
	}
	s.mu.Unlock()
	if !ok {
		return Info{}, false, ErrNotFound
	}
	if info.RevokedAt != "" {
		info, _ = s.Info(name)
		return info, false, nil
	}

	at := s.now().UTC().Format(timeFormat)
	err = s.db.WithTx(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `UPDATE api_keys SET revoked_at = ? WHERE id = ? AND revoked_at IS NULL`, at, id)
		return err
	})
	if err != nil {
		return Info{}, false, fmt.Errorf("revoke key %s: %w", name, err)
	}
	s.mu.Lock()
	e.info.RevokedAt = at
	s.mu.Unlock()
	info, _ = s.Info(name)
	return info, true, nil
}

func nullCap(c int64) sql.NullInt64 { return sql.NullInt64{Int64: c, Valid: c > 0} }
