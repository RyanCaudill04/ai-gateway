// Package admin is the router's admin API: a second HTTP listener, bound to
// loopback only, that the router's own CLI subcommands talk to (for example
// `docker compose exec router /router providers set-key groq`).
//
// Why a listener instead of the CLI opening SQLite itself: the running router
// stays the only process that writes the database (see store), and it updates
// its in-memory state the moment a change is made, so no restart or reload
// signal is needed.
//
// Why it has no authentication, and why that is acceptable:
//   - It only ever listens on a loopback address (Listen refuses anything
//     else, and checks the address it actually got). Inside the router
//     container, loopback belongs to that container's own network namespace:
//     the proxy, ui, cloudflared and ollama containers have their own
//     loopback and can't reach it, and it is a different port from
//     LISTEN_ADDR, so Caddy never forwards to it. docker-compose.yml neither
//     publishes nor exposes it.
//   - So the only callers are processes inside the router container, which in
//     practice means someone running `docker compose exec`. That already
//     requires access to the Docker daemon, which is root on the host and can
//     read .env (the master key) and the database volume directly. A token
//     here would add nothing against that person.
//   - Even so, no endpoint ever returns a secret (provider keys are shown as
//     their last 4 characters only), so the worst a local caller can do is
//     replace or remove keys, not read them.
//   - Running the router outside Docker (development), loopback is the host's,
//     shared with the browser. Guard rejects requests a browser would send
//     (Origin / Sec-Fetch-* headers) and any Host that isn't loopback, which
//     stops a web page from reaching it directly or through DNS rebinding.
//   - Never run the router with `network_mode: host` or let another container
//     share its network namespace (`network_mode: service:router`): either
//     would make this listener reachable from outside the container.
//
// Adding endpoints: write a Register<Thing>(mux, ...) function in its own file
// (see providers.go), call it from the router's admin handler setup, and add
// a CLI command family that uses Client. Paths live under /admin/.
package admin

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
)

// DefaultAddr is where the admin listener runs unless ADMIN_ADDR says otherwise.
const DefaultAddr = "127.0.0.1:8081"

// maxBody bounds a request body. Admin requests are tiny.
const maxBody = 64 << 10

// Listen opens the admin listener on addr, which must be a loopback address
// (127.0.0.0/8, ::1 or localhost). Anything else, including an empty host
// like ":8081" that would listen on every interface, is refused, so the
// unauthenticated admin API can't be exposed by a typo.
func Listen(addr string) (net.Listener, error) {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, fmt.Errorf("admin listener: bad address %q: %w", addr, err)
	}
	if !isLoopbackHost(host) {
		return nil, fmt.Errorf("admin listener: %q is not a loopback address; the admin API has no authentication, so it may only listen on 127.0.0.1, ::1 or localhost", addr)
	}
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("admin listener: %w", err)
	}
	// "localhost" is resolved by the OS; make sure it really was loopback.
	if tcp, ok := ln.Addr().(*net.TCPAddr); !ok || !tcp.IP.IsLoopback() {
		ln.Close()
		return nil, fmt.Errorf("admin listener: %q resolved to non-loopback address %s", addr, ln.Addr())
	}
	return ln, nil
}

func isLoopbackHost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// Guard wraps the admin mux with the checks described in the package comment:
// loopback Host only, no browser requests, and responses never cached.
func Guard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		host := r.Host
		if h, _, err := net.SplitHostPort(host); err == nil {
			host = h
		}
		if !isLoopbackHost(host) {
			WriteError(w, http.StatusForbidden, "admin API: Host must be a loopback address")
			return
		}
		if r.Header.Get("Origin") != "" || r.Header.Get("Sec-Fetch-Site") != "" || r.Header.Get("Sec-Fetch-Mode") != "" {
			WriteError(w, http.StatusForbidden, "admin API: browser requests are not allowed")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// errorBody is every admin error response: {"error": "..."}.
type errorBody struct {
	Error string `json:"error"`
}

// WriteJSON writes v as a JSON response.
func WriteJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

// WriteError writes {"error": msg}. msg is shown to the CLI user as is, so it
// must never contain a secret.
func WriteError(w http.ResponseWriter, status int, msg string) {
	WriteJSON(w, status, errorBody{Error: msg})
}

// DecodeJSON reads a JSON request body into v, rejecting unknown fields and
// oversized bodies. The error is deliberately generic: decoder errors can
// quote parts of the body, and the body may carry a secret.
func DecodeJSON(r *http.Request, v any) error {
	dec := json.NewDecoder(io.LimitReader(r.Body, maxBody))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return errors.New("request body is not the expected JSON")
	}
	return nil
}
