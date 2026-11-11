package auth

import (
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// accessHeader is where Cloudflare's edge puts the signed token on every
// request it lets through an Access application.
const accessHeader = "Cf-Access-Jwt-Assertion"

// AccessVerifier authenticates requests by their Cloudflare Access JWT.
//
// The router can't just trust that the header is present: anything that can
// reach the router without going through Cloudflare (another container, the
// host port on the LAN) can send any header it likes. What it can't do is
// produce a token signed by Cloudflare's private key, so the signature is
// the thing being trusted here.
type AccessVerifier struct {
	keys    *keySet
	parser  *jwt.Parser
	allowed map[string]bool
}

type accessClaims struct {
	Email string `json:"email"`
	jwt.RegisteredClaims
}

// NewAccessVerifier builds a verifier for cfg, which must come from ConfigFromEnv.
func NewAccessVerifier(cfg Config) *AccessVerifier {
	issuer := "https://" + cfg.TeamDomain
	return newAccessVerifier(issuer, issuer+"/cdn-cgi/access/certs", cfg.Audience, cfg.AllowedEmails)
}

func newAccessVerifier(issuer, certsURL, audience string, emails []string) *AccessVerifier {
	allowed := make(map[string]bool, len(emails))
	for _, e := range emails {
		allowed[e] = true
	}
	return &AccessVerifier{
		keys: newKeySet(certsURL),
		parser: jwt.NewParser(
			// Pin the algorithm. Otherwise the token's own header picks it, which
			// allows "alg: none" and HS256-signed-with-the-public-key forgeries.
			jwt.WithValidMethods([]string{"RS256"}),
			jwt.WithIssuer(issuer),
			jwt.WithAudience(audience),
			jwt.WithExpirationRequired(),
			jwt.WithIssuedAt(),
			jwt.WithLeeway(30*time.Second), // clock drift between Cloudflare and the Pi
		),
		allowed: allowed,
	}
}

// Authenticate implements Authenticator.
func (v *AccessVerifier) Authenticate(r *http.Request) (Identity, error) {
	raw := r.Header.Get(accessHeader)
	if raw == "" {
		return Identity{}, fmt.Errorf("%w: no %s header", ErrUnauthenticated, accessHeader)
	}

	var claims accessClaims
	_, err := v.parser.ParseWithClaims(raw, &claims, func(t *jwt.Token) (any, error) {
		kid, _ := t.Header["kid"].(string)
		if kid == "" {
			return nil, errors.New("token has no key id")
		}
		return v.keys.get(r.Context(), kid)
	})
	if err != nil {
		return Identity{}, fmt.Errorf("%w: %v", ErrUnauthenticated, err)
	}

	// Authorization. The token is genuine, but genuine for whom? Service tokens
	// carry no email at all, and a loosened Access policy could admit other people.
	email := normalizeEmail(claims.Email)
	if !v.allowed[email] {
		return Identity{}, fmt.Errorf("%w: %q is not in AUTH_ALLOWED_EMAILS", ErrForbidden, email)
	}
	return Identity{Email: email}, nil
}
