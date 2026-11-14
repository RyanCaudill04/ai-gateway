package admin

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"syscall"
	"time"
)

// ErrNotRunning means nothing answered on the admin address: the router
// isn't running (or ADMIN_ADDR differs between the router and the CLI).
var ErrNotRunning = errors.New("the router is not running")

// Error is an error response from the admin API. Message is the server's
// {"error": ...} text, written to be shown to the user as is.
type Error struct {
	Status  int
	Message string
}

func (e *Error) Error() string { return e.Message }

// Client calls the admin API. CLI subcommands share it.
type Client struct {
	addr string
	http *http.Client
}

// NewClient returns a Client for the admin listener at addr (host:port).
func NewClient(addr string) *Client {
	// Long enough for a --test call to a slow provider (providerkeys.CheckTimeout).
	return &Client{addr: addr, http: &http.Client{Timeout: 30 * time.Second}}
}

// Do sends a JSON request and decodes a JSON response into out. in and out
// may be nil. A non-2xx answer is an *Error; an unreachable router is
// ErrNotRunning (wrapped, with the address and what to do).
func (c *Client) Do(ctx context.Context, method, path string, in, out any) error {
	var body io.Reader
	if in != nil {
		data, err := json.Marshal(in)
		if err != nil {
			return err
		}
		body = bytes.NewReader(data)
	}
	req, err := http.NewRequestWithContext(ctx, method, "http://"+c.addr+path, body)
	if err != nil {
		return err
	}
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		if errors.Is(err, syscall.ECONNREFUSED) {
			return fmt.Errorf("%w: nothing is listening on %s. This command talks to the running router; start it with `docker compose up -d` and try again", ErrNotRunning, c.addr)
		}
		// The request body is never part of this error (url.Error quotes the
		// method and URL only), so a key can't leak through it.
		return fmt.Errorf("admin API at %s: %w", c.addr, err)
	}
	defer resp.Body.Close()

	data, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return fmt.Errorf("admin API at %s: %w", c.addr, err)
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		var eb errorBody
		if json.Unmarshal(data, &eb) != nil || eb.Error == "" {
			eb.Error = fmt.Sprintf("admin API answered %s", resp.Status)
		}
		return &Error{Status: resp.StatusCode, Message: eb.Error}
	}
	if out != nil {
		if err := json.Unmarshal(data, out); err != nil {
			return fmt.Errorf("admin API at %s: unexpected response: %w", c.addr, err)
		}
	}
	return nil
}
