package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"text/tabwriter"

	"golang.org/x/term"

	"github.com/RyanCaudill04/ai-gateway/router/internal/admin"
	"github.com/RyanCaudill04/ai-gateway/router/internal/providerkeys"
)

const providersUsage = `usage:
  router providers list
  router providers set-key [--test] <provider>   key from a hidden prompt, or stdin when piped
  router providers remove-key <provider>`

// providersCommand runs `router providers ...`. It sends everything to the
// running router's admin API, so the router stays the only process that
// writes the database and picks up a change on the very next request.
func providersCommand(args []string) error {
	c := admin.NewClient(envOr("ADMIN_ADDR", admin.DefaultAddr))
	return runProviders(context.Background(), c, args, os.Stdin, os.Stdout, os.Stderr)
}

func runProviders(ctx context.Context, c *admin.Client, args []string, stdin io.Reader, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		return errors.New("missing subcommand\n" + providersUsage)
	}
	sub, rest := args[0], args[1:]
	switch sub {
	case "list":
		if len(rest) != 0 {
			return errors.New("list takes no arguments\n" + providersUsage)
		}
		return providersList(ctx, c, stdout)
	case "set-key":
		test := false
		var names []string
		for _, a := range rest {
			switch {
			case a == "--test" || a == "-test":
				test = true
			case strings.HasPrefix(a, "-"):
				return errors.New("unknown flag\n" + providersUsage)
			default:
				names = append(names, a)
			}
		}
		if len(names) != 1 {
			return errors.New("set-key needs exactly one provider name\n" + providersUsage)
		}
		return providersSetKey(ctx, c, names[0], test, stdin, stdout, stderr)
	case "remove-key":
		if len(rest) != 1 {
			return errors.New("remove-key needs exactly one provider name\n" + providersUsage)
		}
		return providersRemoveKey(ctx, c, rest[0], stdout)
	default:
		return errors.New("unknown subcommand\n" + providersUsage)
	}
}

func keyPath(provider string) string {
	return "/admin/providers/" + url.PathEscape(provider) + "/key"
}

func providersList(ctx context.Context, c *admin.Client, stdout io.Writer) error {
	var list []admin.ProviderStatus
	if err := c.Do(ctx, http.MethodGet, "/admin/providers", nil, &list); err != nil {
		return err
	}
	tw := tabwriter.NewWriter(stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "PROVIDER\tENABLED\tKEY\tUPDATED")
	for _, p := range list {
		key := "-"
		switch {
		case p.KeyUnreadable:
			key = "unreadable, set it again"
		case p.HasKey:
			key = "..." + p.KeyLast4
		case p.Local:
			key = "not needed (local)"
		}
		updated := p.UpdatedAt
		if updated == "" {
			updated = "-"
		}
		name := p.Provider
		if !p.InConfig {
			name += " (not in providers.yaml)"
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", name, yesNo(p.Enabled), key, updated)
	}
	return tw.Flush()
}

func yesNo(b bool) string {
	if b {
		return "yes"
	}
	return "no"
}

func providersSetKey(ctx context.Context, c *admin.Client, provider string, test bool, stdin io.Reader, stdout, stderr io.Writer) error {
	// Check the provider before asking for the key, so a typo doesn't cost a
	// paste, and so "router not running" shows up before the prompt. The
	// server checks again; this is only for a friendlier CLI.
	var list []admin.ProviderStatus
	if err := c.Do(ctx, http.MethodGet, "/admin/providers", nil, &list); err != nil {
		return err
	}
	var known []string
	var found *admin.ProviderStatus
	for i, p := range list {
		if !p.InConfig {
			continue
		}
		known = append(known, p.Provider)
		if p.Provider == provider {
			found = &list[i]
		}
	}
	if found == nil {
		// The name isn't repeated: it might be a key typed in the wrong place.
		return fmt.Errorf("unknown provider; providers.yaml has: %s", strings.Join(known, ", "))
	}
	if found.Local {
		return fmt.Errorf("%s is a local provider and takes no key", provider)
	}

	key, err := readKey(provider, stdin, stderr)
	if err != nil {
		return err
	}
	if test {
		fmt.Fprintf(stderr, "Testing the key against %s...\n", provider)
	}
	var st admin.ProviderStatus
	if err := c.Do(ctx, http.MethodPut, keyPath(provider), admin.SetKeyRequest{Key: key, Test: test}, &st); err != nil {
		// Server messages never contain the key, but be certain.
		return errors.New(providerkeys.Redact(err.Error(), strings.TrimSpace(key)))
	}
	tested := ""
	if test {
		tested = ", tested OK"
	}
	fmt.Fprintf(stdout, "Saved the key for %s (ends in %s%s). Enabled: %s.\n", provider, st.KeyLast4, tested, yesNo(st.Enabled))
	return nil
}

// readKey gets the key from a hidden prompt when stdin is a terminal, or
// reads all of stdin when it's piped (`docker compose exec -T router ...`).
func readKey(provider string, stdin io.Reader, stderr io.Writer) (string, error) {
	if f, ok := stdin.(*os.File); ok && term.IsTerminal(int(f.Fd())) {
		fmt.Fprintf(stderr, "API key for %s (input is hidden): ", provider)
		b, err := term.ReadPassword(int(f.Fd()))
		fmt.Fprintln(stderr)
		if err != nil {
			return "", fmt.Errorf("read key: %w", err)
		}
		return string(b), nil
	}
	b, err := io.ReadAll(io.LimitReader(stdin, providerkeys.MaxKeyLength+1024))
	if err != nil {
		return "", fmt.Errorf("read key from stdin: %w", err)
	}
	if strings.TrimSpace(string(b)) == "" {
		return "", errors.New("no key on stdin; pipe it in, or run in a terminal to be prompted")
	}
	return string(b), nil
}

func providersRemoveKey(ctx context.Context, c *admin.Client, provider string, stdout io.Writer) error {
	var st admin.ProviderStatus
	if err := c.Do(ctx, http.MethodDelete, keyPath(provider), nil, &st); err != nil {
		return err
	}
	fmt.Fprintf(stdout, "Removed the key for %s. Enabled: %s.\n", provider, yesNo(st.Enabled))
	return nil
}
