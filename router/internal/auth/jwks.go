package auth

import (
	"context"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math/big"
	"net/http"
	"sync"
	"time"
)

const (
	// keysTTL: refetch keys older than this, so a key Cloudflare has withdrawn stops being trusted.
	keysTTL = time.Hour
	// minRefreshInterval caps how often we call Cloudflare. Without it, anyone
	// could make the router fetch on every request by sending tokens with made-up key IDs.
	minRefreshInterval = time.Minute
	minRSABits         = 2048
)

// keySet caches the public keys Cloudflare Access signs tokens with.
// Access rotates them every few weeks, so they're fetched at runtime
// (by key ID) rather than configured.
type keySet struct {
	url    string
	client *http.Client
	now    func() time.Time

	mu          sync.Mutex
	keys        map[string]*rsa.PublicKey
	fetchedAt   time.Time
	lastAttempt time.Time
}

func newKeySet(url string) *keySet {
	return &keySet{
		url:    url,
		client: &http.Client{Timeout: 5 * time.Second},
		now:    time.Now,
	}
}

// get returns the key with the given ID, refetching the set when the key is
// unknown (rotation) or the cache is stale. If a refetch fails, keys already
// cached keep working: a Cloudflare blip shouldn't sign me out.
func (s *keySet) get(ctx context.Context, kid string) (*rsa.PublicKey, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	now := s.now()
	key, ok := s.keys[kid]
	fresh := now.Sub(s.fetchedAt) < keysTTL
	if (!ok || !fresh) && now.Sub(s.lastAttempt) >= minRefreshInterval {
		s.lastAttempt = now
		keys, err := s.fetch(ctx)
		if err != nil {
			slog.Error("fetching Cloudflare Access keys failed", "url", s.url, "err", err)
		} else {
			s.keys, s.fetchedAt = keys, now
			key, ok = keys[kid]
		}
	}
	if !ok {
		return nil, fmt.Errorf("no signing key with id %q", kid)
	}
	return key, nil
}

func (s *keySet) fetch(ctx context.Context) (map[string]*rsa.PublicKey, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := s.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("status %d", resp.StatusCode)
	}

	var body struct {
		Keys []struct {
			Kid string `json:"kid"`
			Kty string `json:"kty"`
			N   string `json:"n"`
			E   string `json:"e"`
		} `json:"keys"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&body); err != nil {
		return nil, err
	}

	keys := make(map[string]*rsa.PublicKey)
	for _, k := range body.Keys {
		if k.Kty != "RSA" || k.Kid == "" {
			continue
		}
		n, errN := base64.RawURLEncoding.DecodeString(k.N)
		e, errE := base64.RawURLEncoding.DecodeString(k.E)
		if errN != nil || errE != nil {
			continue
		}
		pub := &rsa.PublicKey{N: new(big.Int).SetBytes(n), E: int(new(big.Int).SetBytes(e).Int64())}
		if pub.N.BitLen() < minRSABits || pub.E < 3 {
			continue
		}
		keys[k.Kid] = pub
	}
	if len(keys) == 0 {
		return nil, errors.New("response contained no usable RSA keys")
	}
	return keys, nil
}
