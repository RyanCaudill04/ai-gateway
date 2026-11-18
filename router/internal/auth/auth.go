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
	"errors"
	"fmt"
	"log/slog"
	"net/http"

	"github.com/RyanCaudill04/ai-gateway/router/internal/apierror"
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
	// ErrNoCredentials is the ErrUnauthenticated case where the request carried
	// nothing to check at all, which is what scanners and bots send.
	ErrNoCredentials = fmt.Errorf("%w: no credentials", ErrUnauthenticated)
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
// gets an apierror response and never reaches next.
func Require(a Authenticator, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id, err := a.Authenticate(r)
		if err != nil {
			apiErr := apierror.Unauthorized("Sign-in required.")
			switch {
			case errors.Is(err, ErrForbidden):
				apiErr = apierror.Forbidden("forbidden", "This account may not use this gateway.")
			case errors.Is(err, ErrNotConfigured):
				apiErr = apierror.Unavailable("auth_not_configured",
					"The gateway's authentication is not configured. See the router logs.")
			}
			// The reason goes to the log only: telling the caller *why* a token
			// failed helps someone forging one more than it helps me.
			//
			// Requests with no credentials at all (bots probing /v1, or every request
			// while auth is unconfigured, which is already logged at startup) are
			// logged at debug level only, so a scan can't fill the disk with log lines.
			// A token that was presented and failed is worth seeing.
			level := slog.LevelWarn
			if errors.Is(err, ErrNoCredentials) || errors.Is(err, ErrNotConfigured) {
				level = slog.LevelDebug
			}
			slog.Log(r.Context(), level, "request rejected", "status", apiErr.Status, "method", r.Method, "path", r.URL.Path, "reason", err.Error())
			apierror.Write(w, apiErr)
			return
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), contextKey{}, id)))
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
