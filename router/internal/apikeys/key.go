// Package apikeys manages the keys that services use to call /v1.
//
// A key looks like "sk-<name>-<52 characters>", made from 32 random bytes. It
// is shown once, when it is created. Only its SHA-256 hash is stored, so a
// copy of the database does not reveal any key. The key is long and random,
// so a plain hash (no salt, no slow function) is enough, and it lets a
// request find its key with one indexed lookup.
package apikeys

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base32"
	"errors"
	"regexp"
	"strings"
)

var encoding = base32.StdEncoding.WithPadding(base32.NoPadding) // 32 bytes -> 52 characters

// validName keeps names short and safe to put in a key, a log line and a command.
var validName = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,31}$`)

// ErrInvalidName is returned for a name that is not lowercase letters, digits
// and hyphens (1 to 32 characters, not starting with a hyphen).
var ErrInvalidName = errors.New("name must be 1-32 characters: lowercase letters, digits and hyphens, not starting with a hyphen")

// Generate returns a new random key for a service.
func Generate(name string) (string, error) {
	if !validName.MatchString(name) {
		return "", ErrInvalidName
	}
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return "sk-" + name + "-" + strings.ToLower(encoding.EncodeToString(b)), nil
}

// Hash is what gets stored and looked up.
func Hash(key string) []byte {
	h := sha256.Sum256([]byte(key))
	return h[:]
}

// displayPrefix is the start of a key that is safe to show in a list: the
// "sk-<name>-" part and four random characters, enough to tell keys apart.
func displayPrefix(name, key string) string {
	n := len("sk-"+name+"-") + 4
	if n > len(key) {
		n = len(key)
	}
	return key[:n]
}
