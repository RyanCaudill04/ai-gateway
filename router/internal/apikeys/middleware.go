package apikeys

import (
	"context"
	"log/slog"
	"net/http"
	"strings"

	"github.com/RyanCaudill04/ai-gateway/router/internal/apierror"
)

type contextKey struct{}

// NameFrom returns the name of the key that authenticated the request (the
// calling service), as stored by Require. The request log (#15) records it
// as `service`.
func NameFrom(ctx context.Context) (string, bool) {
	name, ok := ctx.Value(contextKey{}).(string)
	return name, ok && name != ""
}

// ContextWithName returns ctx carrying name as the calling service. Require
// uses it; it is exported for code that acts on a service's behalf (the UI
// endpoint that attaches the UI's own key, later) and for tests.
func ContextWithName(ctx context.Context, name string) context.Context {
	return context.WithValue(ctx, contextKey{}, name)
}

// unauthorizedMessage is the same for every rejected key, so a caller can't
// tell a revoked key from a mistyped one; the log says which it was.
const unauthorizedMessage = "A valid API key is required: send it as \"Authorization: Bearer <key>\"."

// Require lets a request through to next only with an active key under its
// daily cap, and counts it. Every request that reaches next has been counted,
// whatever it turns out to be (a chat, a model list, an unknown path):
// counting at the door is simple to reason about and bounds a runaway client
// whatever it calls.
//
//   - no Authorization header, a non-Bearer one, or a malformed token,
//     an unknown key, a revoked key: 401 unauthorized, same message for all
//   - over the cap: 429 rate_limited (limit_type requests, window day,
//     resets_at next midnight UTC, Retry-After), not counted
//
// It checks nothing else: not Cloudflare Access, not CSRF (see main.go).
func Require(s *Store, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token, reason := bearerToken(r.Header.Get("Authorization"))
		if reason != "" {
			reject(w, r, reason, "")
			return
		}
		v, info, resetsAt := s.admit(Hash(token))
		switch v {
		case unknownKey:
			reject(w, r, "unknown key", "")
			return
		case revokedKey:
			reject(w, r, "revoked key", info.Name)
			return
		case overCap:
			slog.Debug("request over daily cap", "service", info.Name, "cap", info.DailyCap, "path", r.URL.Path)
			// The store's clock, so Retry-After agrees with resets_at in tests too.
			apierror.WriteAt(w, apierror.RateLimited(apierror.Limit{
				LimitType: apierror.LimitRequests, Window: apierror.WindowDay, ResetsAt: resetsAt,
			}), s.now())
			return
		}
		if info.DailyCap > 0 && info.UsedToday == info.DailyCap {
			// Logged once a day per key: the request that uses the last slot.
			slog.Info("api key reached its daily cap", "service", info.Name, "cap", info.DailyCap, "resets_at", resetsAt)
		}
		next.ServeHTTP(w, r.WithContext(ContextWithName(r.Context(), info.Name)))
	})
}

// bearerToken extracts the key from an Authorization header. reason is ""
// on success, otherwise a short description for the log (never the token).
func bearerToken(header string) (token, reason string) {
	if header == "" {
		return "", "no credentials"
	}
	scheme, token, ok := strings.Cut(header, " ")
	if !ok || !strings.EqualFold(scheme, "Bearer") {
		return "", "not a Bearer authorization"
	}
	token = strings.TrimSpace(token)
	if token == "" || len(token) > maxKeyLength {
		return "", "malformed key"
	}
	for i := 0; i < len(token); i++ {
		if c := token[i]; c <= ' ' || c > '~' {
			return "", "malformed key"
		}
	}
	return token, ""
}

func reject(w http.ResponseWriter, r *http.Request, reason, name string) {
	// Requests with nothing to check (scanners, bots) are logged at debug
	// level only, so a scan can't fill the disk. A key that was presented and
	// failed is worth seeing. The key itself is never logged.
	level := slog.LevelWarn
	if reason == "no credentials" {
		level = slog.LevelDebug
	}
	attrs := []any{"status", http.StatusUnauthorized, "method", r.Method, "path", r.URL.Path, "reason", reason}
	if name != "" {
		attrs = append(attrs, "service", name)
	}
	slog.Log(r.Context(), level, "request rejected", attrs...)
	w.Header().Set("WWW-Authenticate", `Bearer realm="ai-gateway"`)
	apierror.Write(w, apierror.Unauthorized(unauthorizedMessage))
}
