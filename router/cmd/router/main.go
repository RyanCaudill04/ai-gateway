// Command router is the AI gateway's HTTP service.
//
// Usage:
//
//	router              start the server (listens on LISTEN_ADDR, default ":8080")
//	router healthcheck  exit 0 if the running server's /healthz returns 200, else 1
//	router backup <path>  write a consistent copy of the database to <path> (VACUUM INTO)
//	router providers ...  register, list and remove provider API keys (see providers.go)
//	router keys ...       create, list and revoke per-service API keys (see keys.go)
//
// The healthcheck subcommand exists because the runtime image has no shell or
// curl, so Docker's HEALTHCHECK runs this same binary as the HTTP client.
//
// The server needs GATEWAY_SECRET_KEY (the master key that encrypts provider
// keys); the subcommands don't.
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

	"github.com/RyanCaudill04/ai-gateway/router/internal/admin"
	"github.com/RyanCaudill04/ai-gateway/router/internal/api"
	"github.com/RyanCaudill04/ai-gateway/router/internal/apierror"
	"github.com/RyanCaudill04/ai-gateway/router/internal/apikeys"
	"github.com/RyanCaudill04/ai-gateway/router/internal/auth"
	"github.com/RyanCaudill04/ai-gateway/router/internal/config"
	"github.com/RyanCaudill04/ai-gateway/router/internal/poller"
	"github.com/RyanCaudill04/ai-gateway/router/internal/providerkeys"
	"github.com/RyanCaudill04/ai-gateway/router/internal/ratelimit"
	"github.com/RyanCaudill04/ai-gateway/router/internal/requestlog"
	"github.com/RyanCaudill04/ai-gateway/router/internal/store"
	"github.com/RyanCaudill04/ai-gateway/router/internal/upstream"
)

// commands are the subcommands. Each runs as a second process next to the
// server (`docker compose exec router /router <command> ...`) and keeps its
// family of subcommands in its own file.
var commands = map[string]func(args []string) error{
	"healthcheck": func([]string) error { return healthcheck(envOr("LISTEN_ADDR", ":8080")) },
	"backup":      backup,
	"providers":   providersCommand,
	"keys":        keysCommand,
}

func main() {
	if len(os.Args) > 1 {
		name := os.Args[1]
		cmd, ok := commands[name]
		if !ok {
			fmt.Fprintln(os.Stderr, "unknown command; available: healthcheck, backup, providers, keys")
			os.Exit(2)
		}
		if err := cmd(os.Args[2:]); err != nil {
			fmt.Fprintf(os.Stderr, "%s: %v\n", name, err)
			os.Exit(1)
		}
		return
	}

	if err := run(envOr("LISTEN_ADDR", ":8080")); err != nil {
		slog.Error("router stopped", "err", err)
		os.Exit(1)
	}
}

// run starts the HTTP server and the loopback-only admin listener, and blocks
// until one fails or the process gets SIGINT/SIGTERM (what `docker compose
// stop` sends), then shuts both down gracefully.
func run(addr string) error {
	dbPath := envOr("DATABASE_PATH", "/data/gateway.db")

	// The master key is checked before anything else, so a missing or
	// malformed one stops startup with a message saying how to make one.
	cipher, err := providerkeys.MasterKeyFromEnv(os.Getenv)
	if err != nil {
		return err
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

	keys, err := providerkeys.Load(context.Background(), db, cipher)
	if err != nil {
		return err
	}

	// A bad or missing providers file stops startup with the config package's
	// error, which names each offending field.
	cfg, err := config.LoadFile(envOr("PROVIDERS_CONFIG", "/config/providers.yaml"), os.Getenv, keys.Has)
	if err != nil {
		return err
	}
	if disabled := cfg.DisabledProviders(); len(disabled) > 0 {
		slog.Warn("providers disabled (no key registered)", "providers", disabled)
	}
	enabled := providerEnabled(cfg, keys.Has)

	// Rate-limit windows from the last run are loaded now, so a day limit
	// used up before a restart is still skipped after it. Deferred after
	// db.Close, so it runs first: the last pending windows are written
	// once the server has stopped and before the database closes.
	limits, err := ratelimit.Open(context.Background(), db, cfg)
	if err != nil {
		return err
	}
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := limits.Close(ctx); err != nil {
			slog.Error("saving rate limits on shutdown", "err", err)
		}
	}()

	pollInterval, err := poller.IntervalFromEnv(os.Getenv)
	if err != nil {
		return err
	}

	// Service keys and today's counts. Deferred after db.Close, so it runs
	// first: counts not yet written are saved once the server has stopped.
	apiKeys, err := apikeys.Open(context.Background(), db)
	if err != nil {
		return err
	}
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := apiKeys.Close(ctx); err != nil {
			slog.Error("saving api key usage on shutdown", "err", err)
		}
	}()

	// The request log (#15). Shutdown order matters: the HTTP servers stop
	// first (the Shutdown calls at the end of this function), so no request
	// is still producing rows; then this deferred Close writes every queued
	// row; then db.Close (deferred earlier, so it runs later). Closing the log
	// before the servers would drop the last requests' rows; closing the
	// database before the log would lose the queue.
	reqLog := requestlog.Open(db, requestlog.Options{})
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := reqLog.Close(ctx); err != nil {
			slog.Error("saving request log on shutdown", "err", err, "dropped", reqLog.Dropped(), "write_failed", reqLog.WriteFailed())
		}
	}()

	adminLn, err := admin.Listen(envOr("ADMIN_ADDR", admin.DefaultAddr))
	if err != nil {
		return err
	}
	adminSrv := &http.Server{
		Handler:           newAdminHandler(cfg, keys, enabled, apiKeys),
		ReadHeaderTimeout: 10 * time.Second,
	}

	handler := newHandler(handlerDeps{
		authn:       newAuthenticator(),
		db:          db,
		cfg:         cfg,
		enabled:     enabled,
		upstream:    upstream.New(upstream.Options{}),
		providerKey: keys.Get,
		apiKeys:     apiKeys,
		onSummary:   reqLog.OnSummary,
	})
	srv := &http.Server{
		Addr:              addr,
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
		// No WriteTimeout on purpose: streamed completions can run for minutes.
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// Buffered for both servers, so whichever returns second never blocks.
	errCh := make(chan error, 2)
	go func() {
		slog.Info("router listening", "addr", addr)
		errCh <- srv.ListenAndServe()
	}()
	go func() {
		slog.Info("admin API listening (loopback only)", "addr", adminLn.Addr().String())
		errCh <- adminSrv.Serve(adminLn)
	}()

	// Started last, once both servers are going, and stopped (deferred after
	// the tracker's, so it runs first) before the tracker flushes its results.
	usage := poller.New(poller.Deps{
		Config: cfg, Tracker: limits, Completer: upstream.New(upstream.Options{}),
		Key: keys.Get, Enabled: enabled,
	}, poller.Options{Interval: pollInterval})
	usage.Start(ctx)
	defer func() {
		stopCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		if err := usage.Stop(stopCtx); err != nil {
			slog.Error("stopping usage poller", "err", err)
		}
	}()

	var serveErr error
	select {
	case serveErr = <-errCh:
	case <-ctx.Done():
	}

	slog.Info("shutting down")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	for _, err := range []error{serveErr, srv.Shutdown(shutdownCtx), adminSrv.Shutdown(shutdownCtx)} {
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			return err
		}
	}
	return nil
}

// providerEnabled answers "can this provider be used right now?" and is called
// on every request, not once at startup, so a key registered while the router
// runs takes effect without a restart. It mirrors the rule the config package
// applies at load time (local, or has a key). hasKey is the key store's Has.
func providerEnabled(cfg *config.Config, hasKey func(provider string) bool) api.EnabledFunc {
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
	apiKeys *apikeys.Store // service keys for /v1/* (#14)

	upstream    *upstream.Client     // nil: a client with the default timeouts
	providerKey api.KeyFunc          // provider API keys (#40); nil: none
	observer    api.UpstreamObserver // rate-limit tracker (#16); nil: none
	onSummary   api.SummaryFunc      // request log (#15); nil: none
}

// newHandler builds the router's routes. Access is deny-by-default: the only
// public routes are the two health checks registered by name below. Every
// other path, including ones that don't exist yet, is caught by one of two
// subtrees, each behind its own check, so a new endpoint can't be left open
// by forgetting to protect it:
//
//   - /v1/* (the OpenAI-compatible API, called by services): a service API
//     key from `router keys create`, sent as "Authorization: Bearer <key>",
//     checked by apikeys.Require, which also enforces the key's daily cap.
//     That is the only check. No Cloudflare Access token is needed or looked
//     at, in every AUTH_MODE: with AUTH_MODE=none a key is still required,
//     and misconfigured Access settings ("locked") lock /api/* only. No CSRF
//     check either: it exists because browsers attach cookies on their own,
//     and they never attach an Authorization header on their own, so a page
//     on another site can't make a browser send a key it doesn't have.
//   - everything else, /api/* in practice (the UI): authn (Cloudflare Access,
//     or nobody with AUTH_MODE=none) plus CSRF protection, as before. An API
//     key alone gets nowhere here.
//
// Register /v1 routes on v1 and every other route on protected. A /v1 route
// put on protected would never be reached: the /v1/ subtree catches it first.
func newHandler(d handlerDeps) http.Handler {
	authn, db, cfg, enabled, keys := d.authn, d.db, d.cfg, d.enabled, d.apiKeys
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

	// Everything registered on this mux requires a valid service API key.
	v1 := http.NewServeMux()
	v1.Handle("GET /v1/models", api.Models(cfg, enabled))
	v1.Handle("POST /v1/chat/completions", api.Chat(api.ChatDeps{
		Config:    cfg,
		Enabled:   enabled,
		Client:    d.upstream,
		Key:       d.providerKey,
		Observer:  d.observer,
		OnSummary: d.onSummary,
	}))
	v1Handler := apikeys.Require(keys, apierror.Routes(v1))
	mux.Handle("/v1/", v1Handler)
	// Bare "/v1" too, or the mux would answer it with an unauthenticated
	// redirect to "/v1/".
	mux.Handle("/v1", v1Handler)

	// Everything registered on this mux requires an authenticated caller.
	protected := http.NewServeMux()
	// Who the router thinks is calling. Lets the UI tell "signed in" from "not".
	protected.HandleFunc("GET /api/me", func(w http.ResponseWriter, r *http.Request) {
		id, _ := auth.IdentityFrom(r.Context())
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		json.NewEncoder(w).Encode(map[string]string{"email": id.Email})
	})

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
		slog.Warn("AUTH_MODE=none: authentication of /api/* is DISABLED (/v1/* still needs an API key). Never expose this instance to a network you don't trust.")
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
