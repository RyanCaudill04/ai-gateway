package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strconv"
	"text/tabwriter"
	"time"

	"github.com/RyanCaudill04/ai-gateway/router/internal/apikeys"
	"github.com/RyanCaudill04/ai-gateway/router/internal/store"
)

const keysUsage = `usage:
  router keys create <name> [--daily-cap N]   make a key for a service (shown once)
  router keys list                            show all keys
  router keys revoke <name>                   stop a key from working`

// keys manages service API keys. Like backup, it runs as a second process
// next to the server (docker compose exec router /router keys ...) and talks
// to the same database; the server checks the database on every request, so a
// change applies immediately.
func keys(args []string, out io.Writer) error {
	if len(args) == 0 {
		return errors.New(keysUsage)
	}
	db, err := store.Open(context.Background(), envOr("DATABASE_PATH", "/data/gateway.db"))
	if err != nil {
		return err
	}
	defer db.Close()
	s := apikeys.NewStore(db)
	ctx := context.Background()

	switch args[0] {
	case "create":
		if len(args) < 2 {
			return errors.New("missing name\n" + keysUsage)
		}
		name := args[1]
		fs := flag.NewFlagSet("keys create", flag.ContinueOnError)
		capFlag := fs.Int("daily-cap", 0, "most requests per day (0 = no cap)")
		if err := fs.Parse(args[2:]); err != nil {
			return err
		}
		var dailyCap *int
		if *capFlag > 0 {
			dailyCap = capFlag
		}
		key, err := s.Create(ctx, name, dailyCap)
		if err != nil {
			return err
		}
		fmt.Fprintf(out, "Key for %q (copy it now, it cannot be shown again):\n\n  %s\n", name, key)
		return nil

	case "list":
		list, err := s.List(ctx)
		if err != nil {
			return err
		}
		tw := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
		fmt.Fprintln(tw, "NAME\tPREFIX\tDAILY CAP\tCREATED\tSTATUS")
		for _, k := range list {
			cap, status := "none", "active"
			if k.DailyCap != nil {
				cap = strconv.Itoa(*k.DailyCap)
			}
			if k.RevokedAt != nil {
				status = "revoked " + k.RevokedAt.Format(time.DateOnly)
			}
			fmt.Fprintf(tw, "%s\t%s…\t%s\t%s\t%s\n", k.Name, k.Prefix, cap, k.CreatedAt.Format(time.DateOnly), status)
		}
		return tw.Flush()

	case "revoke":
		if len(args) != 2 {
			return errors.New("usage: router keys revoke <name>")
		}
		if err := s.Revoke(ctx, args[1]); err != nil {
			return err
		}
		fmt.Fprintf(out, "Revoked %q. It stops working on the next request.\n", args[1])
		return nil
	}
	return errors.New("unknown keys command " + strconv.Quote(args[0]) + "\n" + keysUsage)
}

func runKeys(args []string) {
	if err := keys(args, os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "keys:", err)
		os.Exit(1)
	}
}
