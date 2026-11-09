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
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"
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
	mux := http.NewServeMux()
	// Internal only: Caddy forwards /v1/* and /api/* unchanged, so this
	// path isn't reachable from outside the compose network.
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		fmt.Fprintln(w, "ok")
	})

	srv := &http.Server{
		Addr:              addr,
		Handler:           mux,
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
