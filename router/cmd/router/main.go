// Command router is the AI gateway's HTTP service.
//
// Usage:
//
//	router              start the server (listens on LISTEN_ADDR, default ":8080")
//	router healthcheck  exit 0 if the running server's /healthz returns 200, else 1
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
	"syscall"
	"time"

	"github.com/RyanCaudill04/ai-gateway/router/internal/auth"
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

	if err := run(addr); err != nil {
		slog.Error("router stopped", "err", err)
		os.Exit(1)
	}
}

// run starts the HTTP server and blocks until it fails or gets SIGINT/SIGTERM
// (what `docker compose stop` sends), then shuts down gracefully.
func run(addr string) error {
	srv := &http.Server{
		Addr:              addr,
		Handler:           newHandler(newAuthenticator()),
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

// newHandler builds the router's routes. Access is deny-by-default: the only
// public routes are the two health checks registered by name below. Every
// other path, including ones that don't exist yet, goes through authn first,
// so a new endpoint can't be left open by forgetting to protect it.
func newHandler(authn auth.Authenticator) http.Handler {
	mux := http.NewServeMux()

	// Internal only: Caddy forwards /v1/* and /api/* unchanged, so this
	// path isn't reachable from outside the compose network.
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
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

	// The browser proves who it is with a cookie that it sends automatically,
	// so another site could try to make it send requests here (CSRF). This
	// rejects state-changing requests that a browser marks as cross-origin.
	// Non-browser clients don't send those markers and are unaffected.
	csrf := http.NewCrossOriginProtection()
	mux.Handle("/", auth.Require(authn, csrf.Handler(protected)))

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
