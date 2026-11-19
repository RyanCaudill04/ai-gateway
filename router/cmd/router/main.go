// Command router is the AI gateway's HTTP service.
//
// Usage:
//
//	router              start the server (listens on LISTEN_ADDR, default ":8080")
//	router healthcheck  exit 0 if the running server's /healthz returns 200, else 1
//	router backup <path>  write a consistent copy of the database to <path> (VACUUM INTO)
//
// The healthcheck subcommand exists because the runtime image has no shell or
// curl, so Docker's HEALTHCHECK runs this same binary as the HTTP client.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/RyanCaudill04/ai-gateway/router/internal/api"
	"github.com/RyanCaudill04/ai-gateway/router/internal/apierror"
	"github.com/RyanCaudill04/ai-gateway/router/internal/auth"
	"github.com/RyanCaudill04/ai-gateway/router/internal/config"
	"github.com/RyanCaudill04/ai-gateway/router/internal/store"
	"github.com/RyanCaudill04/ai-gateway/router/internal/upstream"
)

func main() {
	addr := envOr("LISTEN_ADDR", ":8080")

	if len(os.Args) > 1 && os.Args[1] == "healthcheck" {
		if err := healthcheck(addr); err != nil {
			fmt.Fprintln(os.Stderr, "unhealthy:", err)
			os.Exit(1)
		}
		return
	}

	if len(os.Args) > 1 && os.Args[1] == "backup" {
		if err := backup(os.Args[2:]); err != nil {
			fmt.Fprintln(os.Stderr, "backup failed:", err)
			os.Exit(1)
		}
		return
	}

	if err := run(addr); err != nil {
		slog.Error("router stopped", "err", err)
		os.Exit(1)
	}
}

// run starts the HTTP server and blocks until it fails or gets SIGINT/SIGTERM
// (what `docker compose stop` sends), then shuts down gracefully.
func run(addr string) error {
	dbPath := envOr("DATABASE_PATH", "/data/gateway.db")

	// A bad or missing providers file stops startup with the config package's
	// error, which names each offending field.
	cfg, err := config.LoadFile(envOr("PROVIDERS_CONFIG", "/config/providers.yaml"), os.Getenv, hasKey)
	if err != nil {
		return err
	}
	if disabled := cfg.DisabledProviders(); len(disabled) > 0 {
		slog.Warn("providers disabled (no key registered)", "providers", disabled)
	}

	// Lock first: a second router on the same data dir must exit before it
	// touches the database.
	lock, err := store.AcquireLock(filepath.Join(filepath.Dir(dbPath), "router.lock"))
	if err != nil {
		return err
	}
	defer lock.Release()

	db, err := store.Open(context.Background(), dbPath)
	if err != nil {
		return err
	}
	defer db.Close()

	handler := newHandler(handlerDeps{
		authn:       newAuthenticator(),
		db:          db,
		cfg:         cfg,
		enabled:     providerEnabled(cfg),
		upstream:    upstream.New(upstream.Options{}),
		providerKey: providerKey,
	})
	srv := &http.Server{
		Addr:              addr,
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
		// No WriteTimeout on purpose: streamed completions can run for minutes.
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	errCh := make(chan error, 1)
	go func() {
		slog.Info("router listening", "addr", addr)
		errCh <- srv.ListenAndServe()
	}()

	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
	}

	slog.Info("shutting down")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		return err
	}
	if err := <-errCh; !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

// hasKey is the one place that knows whether a provider has a registered API
// key. Until provider key storage lands (#40) nothing can be registered, so
// only local providers are enabled. #40 replaces this body with a lookup in
// the key store; startup and the request-time check below both use it.
func hasKey(provider string) bool { return false }

// providerKey returns a provider's API key for the chat endpoint. Like hasKey
// it is a placeholder until key storage lands (#40), which replaces this body
// with a lookup in the key store. Local providers never get here: they need
// no key.
func providerKey(provider string) (string, bool) { return "", false }

// providerEnabled answers "can this provider be used right now?" and is called
// on every request, not once at startup, so a key registered while the router
// runs takes effect without a restart. It mirrors the rule the config package
// applies at load time (local, or has a key).
func providerEnabled(cfg *config.Config) api.EnabledFunc {
	return func(name string) bool {
		p := cfg.Providers[name]
		return p != nil && (p.Local || hasKey(name))
	}
}

// pinger is the part of the store /healthz needs. nil skips the check (tests).
type pinger interface{ Ping(context.Context) error }

// handlerDeps is everything the routes need. The chat hooks left nil mean
// "nothing registered" (see api.ChatDeps), which is what tests use.
type handlerDeps struct {
	authn   auth.Authenticator
	db      pinger
	cfg     *config.Config
	enabled api.EnabledFunc

	upstream    *upstream.Client     // nil: a client with the default timeouts
	providerKey api.KeyFunc          // provider API keys (#40); nil: none
	observer    api.UpstreamObserver // rate-limit tracker (#16); nil: none
	onSummary   api.SummaryFunc      // request log (#15); nil: none
}

// newHandler builds the router's routes. Access is deny-by-default: the only
// public routes are the two health checks registered by name below. Every
// other path, including ones that don't exist yet, goes through authn first,
// so a new endpoint can't be left open by forgetting to protect it.
func newHandler(d handlerDeps) http.Handler {
	authn, db, cfg, enabled := d.authn, d.db, d.cfg, d.enabled
	if d.upstream == nil {
		d.upstream = upstream.New(upstream.Options{})
	}
	mux := http.NewServeMux()

	// Internal only: Caddy forwards /v1/* and /api/* unchanged, so this
	// path isn't reachable from outside the compose network.
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		if db != nil {
			if err := db.Ping(r.Context()); err != nil {
				slog.Error("healthz: database check failed", "err", err)
				http.Error(w, "database unavailable", http.StatusServiceUnavailable)
				return
			}
		}
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		fmt.Fprintln(w, "ok")
	})
	// Public: reachable through Caddy's /api/* route (and so through the tunnel).
	// Kept separate from /healthz so it can report more detail later without
	// changing what Docker's healthcheck depends on. Must never reveal anything
	// worth protecting, because anyone can call it.
	mux.HandleFunc("GET /api/health", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
	})

	// Everything registered on this mux requires an authenticated caller.
	protected := http.NewServeMux()
	// Who the router thinks is calling. Lets the UI tell "signed in" from "not".
	protected.HandleFunc("GET /api/me", func(w http.ResponseWriter, r *http.Request) {
		id, _ := auth.IdentityFrom(r.Context())
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		json.NewEncoder(w).Encode(map[string]string{"email": id.Email})
	})

	protected.Handle("GET /v1/models", api.Models(cfg, enabled))
	protected.Handle("POST /v1/chat/completions", api.Chat(api.ChatDeps{
		Config:    cfg,
		Enabled:   enabled,
		Client:    d.upstream,
		Key:       d.providerKey,
		Observer:  d.observer,
		OnSummary: d.onSummary,
	}))

	// The browser proves who it is with a cookie that it sends automatically,
	// so another site could try to make it send requests here (CSRF). This
	// rejects state-changing requests that a browser marks as cross-origin.
	// Non-browser clients don't send those markers and are unaffected.
	csrf := http.NewCrossOriginProtection()
	csrf.SetDenyHandler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		apierror.Write(w, apierror.Forbidden("cross_origin_forbidden", "Cross-origin requests are not allowed."))
	}))
	mux.Handle("/", auth.Require(authn, csrf.Handler(apierror.Routes(protected))))

	return mux
}

// newAuthenticator reads the auth settings from the environment. Bad or
// missing settings don't stop the router (the health checks keep working, so
// Docker and the updater don't flap); they lock every protected endpoint.
func newAuthenticator() auth.Authenticator {
	cfg, err := auth.ConfigFromEnv(os.Getenv)
	if err != nil {
		slog.Error("auth is not configured; every endpoint except the health checks will return 503", "err", err)
		return auth.Locked(err)
	}
	if cfg.Mode == auth.ModeNone {
		slog.Warn("AUTH_MODE=none: authentication is DISABLED. Never expose this instance to a network you don't trust.")
		return auth.Disabled()
	}
	slog.Info("auth: Cloudflare Access", "team_domain", cfg.TeamDomain, "allowed_emails", len(cfg.AllowedEmails))
	return auth.NewAccessVerifier(cfg)
}

// healthcheck calls the local server's /healthz endpoint.
func healthcheck(listenAddr string) error {
	host, port, err := net.SplitHostPort(listenAddr)
	if err != nil {
		return fmt.Errorf("bad LISTEN_ADDR %q: %w", listenAddr, err)
	}
	if host == "" || host == "0.0.0.0" || host == "::" {
		host = "127.0.0.1"
	}

	client := http.Client{Timeout: 3 * time.Second}
	resp, err := client.Get("http://" + net.JoinHostPort(host, port) + "/healthz")
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("status %d", resp.StatusCode)
	}
	return nil
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
