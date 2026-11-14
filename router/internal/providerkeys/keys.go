package providerkeys

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/RyanCaudill04/ai-gateway/router/internal/store"
)

// Key length limits. The minimum keeps Last4 from showing most of a key (real
// provider keys are 30+ characters); the maximum stops a pasted file from
// being stored as a "key".
const (
	MinKeyLength = 12
	MaxKeyLength = 4096
)

// ErrNoKey means no key is stored for the provider.
var ErrNoKey = errors.New("no key registered for this provider")

// Info describes a stored key without revealing it.
type Info struct {
	Provider  string
	Last4     string
	CreatedAt string // UTC, e.g. 2026-11-11T12:00:00Z
	UpdatedAt string
	// Unreadable means the row exists but could not be decrypted at startup
	// (see ErrDecrypt). The provider stays disabled until its key is set again.
	Unreadable bool
}

type entry struct {
	key  string // decrypted; "" when unreadable
	info Info
}

// Keys holds every provider key, decrypted, in memory. Lookups never touch
// the database, so they're cheap enough for every request. Set and Remove
// write through the store's single writer and then update memory, so a key
// registered while the router runs is used by the very next request.
type Keys struct {
	db     *store.Store
	cipher *Cipher
	now    func() time.Time

	// writeMu serializes Set and Remove so memory always ends in the same
	// state as the database. It's separate from mu so a slow write never
	// blocks request-time lookups.
	writeMu sync.Mutex
	mu      sync.RWMutex
	keys    map[string]entry
}

// Load reads and decrypts every stored key. A row that fails to decrypt
// doesn't stop the router: it's logged (provider name only), its provider
// stays disabled, and `providers set-key` or `remove-key` fixes it. Refusing
// to start instead would leave no way to re-register keys after losing the
// master key.
func Load(ctx context.Context, db *store.Store, c *Cipher) (*Keys, error) {
	k := &Keys{db: db, cipher: c, now: time.Now, keys: map[string]entry{}}
	rows, err := db.Reader().QueryContext(ctx,
		`SELECT provider, ciphertext, nonce, last4, created_at, updated_at FROM provider_keys`)
	if err != nil {
		return nil, fmt.Errorf("load provider keys: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var (
			e         entry
			ct, nonce []byte
		)
		if err := rows.Scan(&e.info.Provider, &ct, &nonce, &e.info.Last4, &e.info.CreatedAt, &e.info.UpdatedAt); err != nil {
			return nil, fmt.Errorf("load provider keys: %w", err)
		}
		plain, err := c.Decrypt(e.info.Provider, nonce, ct)
		if err != nil {
			slog.Error("provider key unreadable; register it again with `router providers set-key`",
				"provider", e.info.Provider, "err", err)
			e.info.Unreadable = true
		} else {
			e.key = string(plain)
		}
		k.keys[e.info.Provider] = e
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("load provider keys: %w", err)
	}
	return k, nil
}

// Get returns the decrypted key for provider, for upstream.Request.APIKey.
// ok is false when no usable key is registered.
func (k *Keys) Get(provider string) (key string, ok bool) {
	k.mu.RLock()
	defer k.mu.RUnlock()
	e, found := k.keys[provider]
	return e.key, found && e.key != ""
}

// Has reports whether provider has a usable key. It matches config.HasKey.
func (k *Keys) Has(provider string) bool {
	_, ok := k.Get(provider)
	return ok
}

// Info describes provider's stored key, if any.
func (k *Keys) Info(provider string) (Info, bool) {
	k.mu.RLock()
	defer k.mu.RUnlock()
	e, ok := k.keys[provider]
	return e.info, ok
}

// List describes every stored key, sorted by provider.
func (k *Keys) List() []Info {
	k.mu.RLock()
	defer k.mu.RUnlock()
	out := make([]Info, 0, len(k.keys))
	for _, e := range k.keys {
		out = append(out, e.info)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Provider < out[j].Provider })
	return out
}

// Set encrypts and stores key for provider, replacing any existing key, and
// makes it available to Get immediately. Callers check that provider exists
// in providers.yaml; Set only checks the key's shape (see CleanKey).
func (k *Keys) Set(ctx context.Context, provider, key string) (Info, error) {
	key, err := CleanKey(key)
	if err != nil {
		return Info{}, err
	}
	nonce, ct, err := k.cipher.Encrypt(provider, []byte(key))
	if err != nil {
		return Info{}, err
	}
	info := Info{Provider: provider, Last4: Last4(key), UpdatedAt: k.now().UTC().Format(time.RFC3339)}

	k.writeMu.Lock()
	defer k.writeMu.Unlock()
	err = k.db.WithTx(ctx, func(tx *sql.Tx) error {
		// created_at is kept on update; RETURNING hands back whichever it is.
		return tx.QueryRowContext(ctx, `
			INSERT INTO provider_keys (provider, ciphertext, nonce, last4, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?)
			ON CONFLICT (provider) DO UPDATE SET
				ciphertext = excluded.ciphertext,
				nonce      = excluded.nonce,
				last4      = excluded.last4,
				updated_at = excluded.updated_at
			RETURNING created_at`,
			provider, ct, nonce, info.Last4, info.UpdatedAt, info.UpdatedAt).Scan(&info.CreatedAt)
	})
	if err != nil {
		// Database errors can't contain the key: only ciphertext is sent.
		return Info{}, fmt.Errorf("save key for %s: %w", provider, err)
	}

	k.mu.Lock()
	k.keys[provider] = entry{key: key, info: info}
	k.mu.Unlock()
	return info, nil
}

// Remove deletes provider's key. It returns ErrNoKey if none was stored.
func (k *Keys) Remove(ctx context.Context, provider string) error {
	k.writeMu.Lock()
	defer k.writeMu.Unlock()
	var n int64
	err := k.db.WithTx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `DELETE FROM provider_keys WHERE provider = ?`, provider)
		if err != nil {
			return err
		}
		n, err = res.RowsAffected()
		return err
	})
	if err != nil {
		return fmt.Errorf("remove key for %s: %w", provider, err)
	}
	k.mu.Lock()
	delete(k.keys, provider)
	k.mu.Unlock()
	if n == 0 {
		return ErrNoKey
	}
	return nil
}

// CleanKey trims surrounding whitespace (a piped key usually ends in a
// newline) and checks the key looks like a single token. Its errors never
// include the key.
func CleanKey(key string) (string, error) {
	key = strings.TrimSpace(key)
	switch {
	case key == "":
		return "", errors.New("the key is empty")
	case len(key) < MinKeyLength:
		return "", fmt.Errorf("the key is shorter than %d characters; check you pasted all of it", MinKeyLength)
	case len(key) > MaxKeyLength:
		return "", fmt.Errorf("the key is longer than %d characters", MaxKeyLength)
	}
	for _, r := range key {
		// Keys go into an HTTP header, so spaces and control characters would
		// break the request (or be a sign two things were pasted).
		if unicode.IsSpace(r) || unicode.IsControl(r) || r > unicode.MaxASCII {
			return "", errors.New("the key contains spaces, line breaks or non-ASCII characters")
		}
	}
	return key, nil
}

// Last4 returns the last four characters of key, the only part ever shown.
func Last4(key string) string {
	if len(key) <= 4 {
		return "" // too short to show any of it safely; CleanKey rejects these anyway
	}
	return key[len(key)-4:]
}

// Redact replaces every occurrence of key in s. Use it on any text that
// might quote a key back, such as a provider's error body.
func Redact(s, key string) string {
	if key == "" {
		return s
	}
	return strings.ReplaceAll(s, key, "[redacted]")
}
