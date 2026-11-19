// Package apikeys is the router's own authentication for /v1/*: one key per
// calling service (#14), so usage is attributed to a service and one service
// can't eat everyone's quota.
//
// A key looks like sk-<service name>-<52 random characters>. Only its SHA-256
// is stored; the plain key exists in two places only: the output of
// `router keys create`, once, and the calling service's config.
//
// Keys are loaded into memory at startup and looked up by hash on every
// request, so lookups never wait on the database. Creating or revoking a key
// writes through the store's single writer and then updates memory, so the
// change applies to the very next request with no restart.
//
// Each key may have a daily request cap. Requests are counted per key per UTC
// day in memory (exact under concurrency, see Store) and written to SQLite in
// the background, so a slow disk never delays a request.
package apikeys

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base32"
	"errors"
	"fmt"
	"strings"
)

// Name limits. A name appears in the key itself, in logs and in the request
// log, so it is kept short and to characters that need no quoting anywhere.
const MaxNameLength = 32

// randomBytes is the key's secret part: 256 bits from crypto/rand.
//
// That much entropy is why a plain, unsalted SHA-256 is a sound way to store
// the key. Salts and slow hashes (bcrypt, argon2) exist to protect secrets
// people choose, which are guessable, so an attacker with the hashes can try
// likely ones cheaply. A 256-bit random value can't be guessed: there is no
// list of likely keys to try, and searching 2^256 values is out of reach at
// any hash speed. Reversing SHA-256 itself is not feasible either. A fast
// hash also keeps the per-request lookup cheap.
const randomBytes = 32

// prefixRandomChars is how much of the random part the display prefix shows:
// enough to tell two keys of the same service apart, and 20 bits out of 256,
// which leaves the rest far beyond guessing.
const prefixRandomChars = 4

// HashSize is the length of a key hash (SHA-256).
const HashSize = sha256.Size

// maxKeyLength bounds what the middleware will hash. A real key is
// len("sk-") + MaxNameLength + len("-") + 52 = 88 characters.
const maxKeyLength = 128

// encoding is lowercase base32 without padding (a-z, 2-7): no characters
// that need escaping in a header, a shell or a YAML file, and no '-', so the
// random part is easy to pick out of the key by eye.
var encoding = base32.NewEncoding("abcdefghijklmnopqrstuvwxyz234567").WithPadding(base32.NoPadding)

// ErrInvalidName is wrapped by ValidateName's errors.
var ErrInvalidName = errors.New("invalid key name")

// ValidateName checks a service name: 1 to 32 characters of lowercase
// letters, digits and '-', starting with a letter, not ending with '-' and
// without "--" (so the name reads unambiguously inside the key).
//
// Its errors never repeat the name: someone who pasted a key where the name
// goes would otherwise see it printed back.
func ValidateName(name string) error {
	switch {
	case name == "":
		return fmt.Errorf("%w: the name is empty", ErrInvalidName)
	case len(name) > MaxNameLength:
		return fmt.Errorf("%w: the name is longer than %d characters", ErrInvalidName, MaxNameLength)
	case name[0] < 'a' || name[0] > 'z':
		return fmt.Errorf("%w: the name must start with a lowercase letter", ErrInvalidName)
	case strings.HasSuffix(name, "-") || strings.Contains(name, "--"):
		return fmt.Errorf("%w: the name can't end with '-' or contain \"--\"", ErrInvalidName)
	}
	for _, r := range name {
		if !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '-') {
			return fmt.Errorf("%w: use only lowercase letters, digits and '-'", ErrInvalidName)
		}
	}
	return nil
}

// Generate makes a new key for the service name. It returns the key (show it
// once, never store it), its display prefix and its hash. The CLI calls this,
// not the router, so the key never crosses the admin API (which never carries
// secrets); the router only ever receives the prefix and hash.
func Generate(name string) (key, prefix string, hash [HashSize]byte, err error) {
	if err := ValidateName(name); err != nil {
		return "", "", hash, err
	}
	b := make([]byte, randomBytes)
	if _, err := rand.Read(b); err != nil {
		return "", "", hash, fmt.Errorf("generate key: %w", err)
	}
	random := encoding.EncodeToString(b)
	key = "sk-" + name + "-" + random
	return key, displayPrefix(name, random), Hash(key), nil
}

func displayPrefix(name, random string) string {
	return "sk-" + name + "-" + random[:prefixRandomChars]
}

// Hash is how a key is stored and looked up.
func Hash(key string) [HashSize]byte { return sha256.Sum256([]byte(key)) }

// validPrefix checks that prefix is exactly what Generate makes for name, so
// the admin API can't be used to store something else (a whole key, say) in
// the column `keys list` prints.
func validPrefix(name, prefix string) bool {
	random, ok := strings.CutPrefix(prefix, "sk-"+name+"-")
	if !ok || len(random) != prefixRandomChars {
		return false
	}
	for _, r := range random {
		if !(r >= 'a' && r <= 'z' || r >= '2' && r <= '7') {
			return false
		}
	}
	return true
}
