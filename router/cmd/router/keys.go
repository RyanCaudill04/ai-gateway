package main

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"text/tabwriter"

	"github.com/RyanCaudill04/ai-gateway/router/internal/admin"
	"github.com/RyanCaudill04/ai-gateway/router/internal/apikeys"
)

const keysUsage = `usage:
  router keys create <name> [--daily-cap N]   prints the new key once
  router keys list
  router keys revoke <name>`

// keysCommand runs `router keys ...`: the per-service API keys that callers
// of /v1/* send as "Authorization: Bearer <key>". Like providersCommand it
// goes through the running router's admin API, so a new or revoked key
// applies to the very next request.
func keysCommand(args []string) error {
	c := admin.NewClient(envOr("ADMIN_ADDR", admin.DefaultAddr))
	return runKeys(context.Background(), c, args, os.Stdout, os.Stderr)
}

func runKeys(ctx context.Context, c *admin.Client, args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		return errors.New("missing subcommand\n" + keysUsage)
	}
	sub, rest := args[0], args[1:]
	switch sub {
	case "create":
		name, dailyCap, err := parseCreate(rest)
		if err != nil {
			return err
		}
		return keysCreate(ctx, c, name, dailyCap, stdout, stderr)
	case "list":
		if len(rest) != 0 {
			return errors.New("list takes no arguments\n" + keysUsage)
		}
		return keysList(ctx, c, stdout)
	case "revoke":
		if len(rest) != 1 {
			return errors.New("revoke needs exactly one key name\n" + keysUsage)
		}
		return keysRevoke(ctx, c, rest[0], stdout)
	default:
		return errors.New("unknown subcommand\n" + keysUsage)
	}
}

// parseCreate reads `<name> [--daily-cap N]` (also --daily-cap=N, flags in
// any position). Errors never repeat an argument, in case it was a key.
func parseCreate(args []string) (name string, dailyCap int64, err error) {
	var names []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		var value string
		switch {
		case a == "--daily-cap" || a == "-daily-cap":
			if i+1 >= len(args) {
				return "", 0, errors.New("--daily-cap needs a number\n" + keysUsage)
			}
			i++
			value = args[i]
		case strings.HasPrefix(a, "--daily-cap=") || strings.HasPrefix(a, "-daily-cap="):
			_, value, _ = strings.Cut(a, "=")
		case strings.HasPrefix(a, "-"):
			return "", 0, errors.New("unknown flag\n" + keysUsage)
		default:
			names = append(names, a)
			continue
		}
		n, perr := strconv.ParseInt(value, 10, 64)
		if perr != nil || n < 1 {
			return "", 0, errors.New("--daily-cap must be a whole number of at least 1 (leave it out for no cap)")
		}
		dailyCap = n
	}
	if len(names) != 1 {
		return "", 0, errors.New("create needs exactly one key name\n" + keysUsage)
	}
	if err := apikeys.ValidateName(names[0]); err != nil {
		return "", 0, err
	}
	return names[0], dailyCap, nil
}

func keysCreate(ctx context.Context, c *admin.Client, name string, dailyCap int64, stdout, stderr io.Writer) error {
	// The key is made here, in the CLI, and the router only receives its
	// prefix and hash: the admin API never carries a secret.
	key, prefix, hash, err := apikeys.Generate(name)
	if err != nil {
		return err
	}
	req := admin.CreateAPIKeyRequest{Name: name, Prefix: prefix, Hash: hex.EncodeToString(hash[:]), DailyCap: dailyCap}
	var st admin.APIKeyStatus
	if err := c.Do(ctx, http.MethodPost, "/admin/keys", req, &st); err != nil {
		return err
	}
	limit := "no daily cap"
	if st.DailyCap > 0 {
		limit = fmt.Sprintf("daily cap %d requests (UTC day)", st.DailyCap)
	}
	fmt.Fprintf(stderr, "Created key %s (%s). It is shown once and can't be retrieved later; store it now:\n", st.Name, limit)
	// The key alone on stdout, so `... keys create x > file` captures just it.
	fmt.Fprintln(stdout, key)
	return nil
}

func keysList(ctx context.Context, c *admin.Client, stdout io.Writer) error {
	var list []admin.APIKeyStatus
	if err := c.Do(ctx, http.MethodGet, "/admin/keys", nil, &list); err != nil {
		return err
	}
	tw := tabwriter.NewWriter(stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "NAME\tPREFIX\tCAP\tTODAY\tCREATED\tREVOKED")
	for _, k := range list {
		dailyCap, revoked := "-", "-"
		if k.DailyCap > 0 {
			dailyCap = strconv.FormatInt(k.DailyCap, 10)
		}
		if k.RevokedAt != "" {
			revoked = k.RevokedAt
		}
		fmt.Fprintf(tw, "%s\t%s...\t%s\t%d\t%s\t%s\n", k.Name, k.Prefix, dailyCap, k.UsedToday, k.CreatedAt, revoked)
	}
	return tw.Flush()
}

func keysRevoke(ctx context.Context, c *admin.Client, name string, stdout io.Writer) error {
	if err := apikeys.ValidateName(name); err != nil {
		return err
	}
	var st admin.APIKeyStatus
	if err := c.Do(ctx, http.MethodDelete, "/admin/keys/"+url.PathEscape(name), nil, &st); err != nil {
		return err
	}
	if st.AlreadyRevoked {
		fmt.Fprintf(stdout, "Key %s was already revoked (at %s). Nothing changed.\n", st.Name, st.RevokedAt)
		return nil
	}
	fmt.Fprintf(stdout, "Revoked key %s. It stops working on the next request.\n", st.Name)
	return nil
}
