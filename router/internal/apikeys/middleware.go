package apikeys

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strings"

	"github.com/RyanCaudill04/ai-gateway/router/internal/apierror"
)

// Verifier says which service a key belongs to. *Store is one.
type Verifier interface {
	Verify(ctx context.Context, key string) (name string, err error)
}

type contextKey struct{}

// NameFrom returns the name of the service that Require authenticated.
func NameFrom(ctx context.Context) (string, bool) {
	name, ok := ctx.Value(contextKey{}).(string)
	return name, ok
}

// Require runs next only for requests with a valid "Authorization: Bearer
// <key>". A missing header, a malformed one, an unknown key and a revoked key
// all get the same 401, so a caller can't learn which keys exist; the log
// says which it was (never the key itself).
func Require(v Verifier, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key, reason := bearer(r)
		if reason == "" {
			name, err := v.Verify(r.Context(), key)
			switch {
			case err == nil:
				next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), contextKey{}, name)))
				return
			case errors.Is(err, ErrInvalid):
				reason = "unknown or revoked key"
			default:
				slog.Error("api key check failed", "err", err)
				apierror.Write(w, apierror.Unavailable("auth_unavailable", "Could not check the API key. Try again."))
				return
			}
		}
		slog.Warn("api key rejected", "reason", reason, "path", r.URL.Path)
		w.Header().Set("WWW-Authenticate", "Bearer")
		apierror.Write(w, apierror.Unauthorized("A valid API key is required. Send it as 'Authorization: Bearer <key>'."))
	})
}

// bearer extracts the key, or says why there isn't one.
func bearer(r *http.Request) (key, reason string) {
	h := r.Header.Get("Authorization")
	if h == "" {
		return "", "no Authorization header"
	}
	scheme, key, ok := strings.Cut(h, " ")
	if !ok || !strings.EqualFold(scheme, "Bearer") || strings.TrimSpace(key) == "" {
		return "", "malformed Authorization header"
	}
	return strings.TrimSpace(key), ""
}
