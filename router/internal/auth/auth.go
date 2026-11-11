// Package auth decides who may call the router.
//
// Authentication ("who are you?") is delegated to Cloudflare Access, which
// signs a JWT for every request it lets through. Authorization ("are you
// allowed?") is an allowlist of email addresses checked here.
//
// Everything fails closed: a request is rejected unless an Authenticator
// positively returns an Identity.
package auth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
)

// Identity is the authenticated caller of a request.
type Identity struct {
	Email string
}

// Authenticator turns a request into an Identity, or says why it can't.
// Implementations return an error wrapping one of the Err* values below.
type Authenticator interface {
	Authenticate(r *http.Request) (Identity, error)
}

var (
	// ErrUnauthenticated: no credential, or one that didn't verify. → 401
	ErrUnauthenticated = errors.New("unauthenticated")
	// ErrForbidden: a genuine credential for someone who isn't allowed in. → 403
	ErrForbidden = errors.New("forbidden")
	// ErrNotConfigured: the router has no usable auth settings. → 503
	ErrNotConfigured = errors.New("auth not configured")
)

type contextKey struct{}

// IdentityFrom returns the caller that Require stored on the request context.
func IdentityFrom(ctx context.Context) (Identity, bool) {
	id, ok := ctx.Value(contextKey{}).(Identity)
	return id, ok
}

// Require runs next only for requests that a authenticates. Everything else
// gets a JSON error and never reaches next.
func Require(a Authenticator, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id, err := a.Authenticate(r)
		if err != nil {
			status, code, msg := http.StatusUnauthorized, "unauthorized", "Sign-in required."
			switch {
			case errors.Is(err, ErrForbidden):
				status, code, msg = http.StatusForbidden, "forbidden", "This account may not use this gateway."
			case errors.Is(err, ErrNotConfigured):
				status, code, msg = http.StatusServiceUnavailable, "auth_not_configured",
					"The gateway's authentication is not configured. See the router logs."
			}
			// The reason goes to the log only: telling the caller *why* a token
			// failed helps someone forging one more than it helps me.
			slog.Warn("request rejected", "status", status, "method", r.Method, "path", r.URL.Path, "reason", err.Error())
			writeError(w, status, code, msg)
			return
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), contextKey{}, id)))
	})
}

func writeError(w http.ResponseWriter, status int, code, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(map[string]any{
		"error": map[string]string{"code": code, "message": message},
	})
}

// Disabled lets every request through as a fixed local identity.
// Only for AUTH_MODE=none (local development).
func Disabled() Authenticator { return disabled{} }

type disabled struct{}

func (disabled) Authenticate(*http.Request) (Identity, error) {
	return Identity{Email: "local-dev"}, nil
}

// Locked rejects every request with ErrNotConfigured. The router uses it when
// its auth settings are missing or invalid, so a bad .env locks the API
// instead of opening it.
func Locked(reason error) Authenticator { return locked{reason} }

type locked struct{ reason error }

func (l locked) Authenticate(*http.Request) (Identity, error) {
	return Identity{}, fmt.Errorf("%w: %v", ErrNotConfigured, l.reason)
}
