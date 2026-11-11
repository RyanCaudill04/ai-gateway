package auth

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"math/big"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

const (
	testAud   = "aud-of-this-app"
	testEmail = "me@example.com"
	testKid   = "key-1"
)

// fakeAccess stands in for Cloudflare: it serves a key set and signs tokens.
type fakeAccess struct {
	t       *testing.T
	srv     *httptest.Server
	key     *rsa.PrivateKey
	fetches atomic.Int32
}

func newFakeAccess(t *testing.T) *fakeAccess {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	f := &fakeAccess{t: t, key: key}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.fetches.Add(1)
		json.NewEncoder(w).Encode(map[string]any{"keys": []map[string]string{{
			"kid": testKid, "kty": "RSA", "alg": "RS256",
			"n": base64.RawURLEncoding.EncodeToString(key.N.Bytes()),
			"e": base64.RawURLEncoding.EncodeToString(big.NewInt(int64(key.E)).Bytes()),
		}}})
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeAccess) verifier() *AccessVerifier {
	return newAccessVerifier(f.srv.URL, f.srv.URL, testAud, []string{testEmail})
}

// claims returns a valid set of claims; tests break one thing at a time.
func (f *fakeAccess) claims() jwt.MapClaims {
	now := time.Now()
	return jwt.MapClaims{
		"iss":   f.srv.URL,
		"aud":   []string{testAud},
		"email": testEmail,
		"iat":   now.Unix(),
		"exp":   now.Add(time.Hour).Unix(),
	}
}

func (f *fakeAccess) sign(method jwt.SigningMethod, key any, kid string, claims jwt.MapClaims) string {
	f.t.Helper()
	tok := jwt.NewWithClaims(method, claims)
	if kid != "" {
		tok.Header["kid"] = kid
	}
	s, err := tok.SignedString(key)
	if err != nil {
		f.t.Fatal(err)
	}
	return s
}

func request(token string) *http.Request {
	r := httptest.NewRequest(http.MethodGet, "/api/me", nil)
	if token != "" {
		r.Header.Set(accessHeader, token)
	}
	return r
}

func TestAccessVerifier(t *testing.T) {
	f := newFakeAccess(t)
	otherKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	// What an attacker who only knows the *public* key would use as an HMAC secret.
	pubPEM := pem.EncodeToMemory(&pem.Block{Type: "RSA PUBLIC KEY", Bytes: x509.MarshalPKCS1PublicKey(&f.key.PublicKey)})

	with := func(k string, v any) jwt.MapClaims {
		c := f.claims()
		if v == nil {
			delete(c, k)
		} else {
			c[k] = v
		}
		return c
	}
	rs := func(c jwt.MapClaims) string { return f.sign(jwt.SigningMethodRS256, f.key, testKid, c) }

	tests := []struct {
		name  string
		token string
		want  error // nil = accepted
	}{
		{"valid token", rs(f.claims()), nil},
		{"email differs only by case", rs(with("email", "Me@Example.com")), nil},

		{"no header", "", ErrUnauthenticated},
		{"garbage", "not.a.jwt", ErrUnauthenticated},
		{"expired", rs(with("exp", time.Now().Add(-time.Hour).Unix())), ErrUnauthenticated},
		{"no expiry", rs(with("exp", nil)), ErrUnauthenticated},
		{"issued in the future", rs(with("iat", time.Now().Add(time.Hour).Unix())), ErrUnauthenticated},
		{"wrong audience (another Access app)", rs(with("aud", []string{"some-other-app"})), ErrUnauthenticated},
		{"no audience", rs(with("aud", nil)), ErrUnauthenticated},
		{"wrong issuer (another team)", rs(with("iss", "https://evil.cloudflareaccess.com")), ErrUnauthenticated},
		{"signed by a different key", f.sign(jwt.SigningMethodRS256, otherKey, testKid, f.claims()), ErrUnauthenticated},
		{"unknown key id", f.sign(jwt.SigningMethodRS256, otherKey, "key-2", f.claims()), ErrUnauthenticated},
		{"no key id", f.sign(jwt.SigningMethodRS256, f.key, "", f.claims()), ErrUnauthenticated},
		{"alg none", f.sign(jwt.SigningMethodNone, jwt.UnsafeAllowNoneSignatureType, testKid, f.claims()), ErrUnauthenticated},
		{"HS256 using the public key as the secret", f.sign(jwt.SigningMethodHS256, pubPEM, testKid, f.claims()), ErrUnauthenticated},

		{"genuine token, someone else's email", rs(with("email", "stranger@example.com")), ErrForbidden},
		{"genuine token, no email (service token)", rs(with("email", nil)), ErrForbidden},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			id, err := f.verifier().Authenticate(request(tt.token))
			if tt.want == nil {
				if err != nil {
					t.Fatalf("rejected: %v", err)
				}
				if id.Email != testEmail {
					t.Fatalf("email = %q, want %q", id.Email, testEmail)
				}
				return
			}
			if !errors.Is(err, tt.want) {
				t.Fatalf("err = %v, want %v", err, tt.want)
			}
		})
	}
}

func TestKeySetCachesAndLimitsRefetches(t *testing.T) {
	f := newFakeAccess(t)
	v := f.verifier()
	now := time.Now()
	v.keys.now = func() time.Time { return now }

	good := f.sign(jwt.SigningMethodRS256, f.key, testKid, f.claims())
	unknown := f.sign(jwt.SigningMethodRS256, f.key, "made-up", f.claims())

	for range 3 {
		if _, err := v.Authenticate(request(good)); err != nil {
			t.Fatal(err)
		}
	}
	if n := f.fetches.Load(); n != 1 {
		t.Fatalf("fetched keys %d times for 3 valid requests, want 1", n)
	}

	// A flood of made-up key IDs must not turn into a flood of fetches.
	for range 5 {
		v.Authenticate(request(unknown))
	}
	if n := f.fetches.Load(); n != 1 {
		t.Fatalf("fetched keys %d times within the refresh interval, want 1", n)
	}

	now = now.Add(minRefreshInterval)
	v.Authenticate(request(unknown))
	if n := f.fetches.Load(); n != 2 {
		t.Fatalf("fetched keys %d times after the refresh interval, want 2", n)
	}

	// Cloudflare unreachable and the cache stale: known keys keep working.
	f.srv.Close()
	now = now.Add(2 * keysTTL)
	if _, err := v.Authenticate(request(good)); err != nil {
		t.Fatalf("cached key stopped working when the refetch failed: %v", err)
	}
}

func TestConfigFromEnv(t *testing.T) {
	full := map[string]string{
		"CF_ACCESS_TEAM_DOMAIN": "https://MyTeam.cloudflareaccess.com/",
		"CF_ACCESS_AUD":         testAud,
		"AUTH_ALLOWED_EMAILS":   " Me@Example.com , other@example.com,",
	}
	env := func(m map[string]string) func(string) string {
		return func(k string) string { return m[k] }
	}
	without := func(k string) map[string]string {
		m := map[string]string{}
		for key, v := range full {
			if key != k {
				m[key] = v
			}
		}
		return m
	}

	cfg, err := ConfigFromEnv(env(full))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Mode != ModeCloudflareAccess || cfg.TeamDomain != "myteam.cloudflareaccess.com" ||
		len(cfg.AllowedEmails) != 2 || cfg.AllowedEmails[0] != "me@example.com" {
		t.Fatalf("unexpected config: %+v", cfg)
	}

	if cfg, err := ConfigFromEnv(env(map[string]string{"AUTH_MODE": "none"})); err != nil || cfg.Mode != ModeNone {
		t.Fatalf("AUTH_MODE=none: cfg=%+v err=%v", cfg, err)
	}

	bad := map[string]map[string]string{
		"nothing set":      {},
		"no team domain":   without("CF_ACCESS_TEAM_DOMAIN"),
		"no audience":      without("CF_ACCESS_AUD"),
		"no emails":        without("AUTH_ALLOWED_EMAILS"),
		"unknown mode":     {"AUTH_MODE": "off"},
		"domain with path": {"CF_ACCESS_TEAM_DOMAIN": "team.cloudflareaccess.com/x", "CF_ACCESS_AUD": testAud, "AUTH_ALLOWED_EMAILS": testEmail},
		"http domain":      {"CF_ACCESS_TEAM_DOMAIN": "http://team.cloudflareaccess.com", "CF_ACCESS_AUD": testAud, "AUTH_ALLOWED_EMAILS": testEmail},
	}
	for name, m := range bad {
		if _, err := ConfigFromEnv(env(m)); err == nil {
			t.Errorf("%s: accepted, want an error", name)
		}
	}
}
