// Package providerkeys stores upstream provider API keys (Groq, Gemini, ...)
// encrypted in SQLite and keeps the decrypted keys in memory for requests.
//
// Keys are encrypted with AES-256-GCM under a master key, GATEWAY_SECRET_KEY,
// which lives only in the environment. A copied database or backup is
// useless without it. Losing it means re-registering every key.
//
// Nothing in this package logs a key or puts one in an error message. The
// only part of a key that leaves it is the last four characters (Last4).
package providerkeys

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
)

// MasterKeyEnv is the environment variable that holds the master key.
const MasterKeyEnv = "GATEWAY_SECRET_KEY"

// MasterKeySize is the master key length in bytes (AES-256).
const MasterKeySize = 32

// ErrDecrypt means a stored key could not be decrypted: the master key is not
// the one it was saved with, or the row was changed (for example copied from
// another provider's row). GCM can't tell these apart, and that's fine: in
// every case the key is unusable and must be registered again.
var ErrDecrypt = errors.New("stored key can't be decrypted (GATEWAY_SECRET_KEY differs from the one it was saved with, or the row was altered)")

// Cipher encrypts and decrypts provider keys with the master key.
type Cipher struct {
	aead cipher.AEAD
}

// MasterKeyFromEnv reads, checks and loads GATEWAY_SECRET_KEY. The errors
// say what is wrong and how to fix it, and never include the value.
func MasterKeyFromEnv(getenv func(string) string) (*Cipher, error) {
	return ParseMasterKey(getenv(MasterKeyEnv))
}

// ParseMasterKey checks a base64 master key and returns a Cipher for it.
func ParseMasterKey(encoded string) (*Cipher, error) {
	const fix = "generate one with `openssl rand -base64 32` and put it in .env"
	encoded = strings.TrimSpace(encoded)
	if encoded == "" {
		return nil, fmt.Errorf("%s is not set; %s", MasterKeyEnv, fix)
	}
	key, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		// Don't wrap err: base64 errors quote the offending byte offset only,
		// but there's no reason to say anything about the value at all.
		return nil, fmt.Errorf("%s is not valid base64; %s", MasterKeyEnv, fix)
	}
	if len(key) != MasterKeySize {
		return nil, fmt.Errorf("%s decodes to %d bytes, want exactly %d; %s", MasterKeyEnv, len(key), MasterKeySize, fix)
	}
	return NewCipher(key)
}

// NewCipher returns a Cipher for a raw 32-byte key.
func NewCipher(key []byte) (*Cipher, error) {
	if len(key) != MasterKeySize {
		return nil, fmt.Errorf("master key is %d bytes, want %d", len(key), MasterKeySize)
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return &Cipher{aead: aead}, nil
}

// additionalData binds a ciphertext to its provider. GCM authenticates it
// along with the ciphertext, so a row moved to another provider fails to
// decrypt. The fixed prefix keeps these ciphertexts from being valid for any
// other purpose the master key might be used for later.
func additionalData(provider string) []byte {
	return []byte("ai-gateway provider key v1\x00" + provider)
}

// Encrypt seals plaintext for provider with a fresh random nonce. Reusing a
// nonce under the same key would break GCM, so one is never reused: 96 random
// bits per encryption is safe far beyond the number of keys anyone registers.
func (c *Cipher) Encrypt(provider string, plaintext []byte) (nonce, ciphertext []byte, err error) {
	nonce = make([]byte, c.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, nil, fmt.Errorf("generate nonce: %w", err)
	}
	return nonce, c.aead.Seal(nil, nonce, plaintext, additionalData(provider)), nil
}

// Decrypt opens a ciphertext sealed by Encrypt for the same provider. Any
// failure is ErrDecrypt.
func (c *Cipher) Decrypt(provider string, nonce, ciphertext []byte) ([]byte, error) {
	if len(nonce) != c.aead.NonceSize() {
		return nil, ErrDecrypt
	}
	plaintext, err := c.aead.Open(nil, nonce, ciphertext, additionalData(provider))
	if err != nil {
		return nil, ErrDecrypt
	}
	return plaintext, nil
}
