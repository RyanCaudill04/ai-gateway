package main

import (
	"context"
	"errors"
	"fmt"
	"os"

	"github.com/RyanCaudill04/ai-gateway/router/internal/store"
)

// backup writes a consistent copy of the live database to args[0]. It runs
// as a second process while the server is up (via `docker compose exec`), so
// it takes no lock and runs no migrations; see store.Backup.
func backup(args []string) error {
	if len(args) != 1 || args[0] == "" {
		return errors.New("missing destination path\nusage: router backup <path>")
	}
	dest := args[0]
	dbPath := envOr("DATABASE_PATH", "/data/gateway.db")

	if err := store.Backup(context.Background(), dbPath, dest); err != nil {
		return err
	}
	fi, err := os.Stat(dest)
	if err != nil {
		return err
	}
	fmt.Printf("backup written to %s (%d bytes)\n", dest, fi.Size())
	return nil
}
