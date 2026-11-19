package main

import (
	"net/http"

	"github.com/RyanCaudill04/ai-gateway/router/internal/admin"
	"github.com/RyanCaudill04/ai-gateway/router/internal/apikeys"
	"github.com/RyanCaudill04/ai-gateway/router/internal/config"
	"github.com/RyanCaudill04/ai-gateway/router/internal/providerkeys"
)

// newAdminHandler builds the admin API served on the loopback-only admin
// listener (ADMIN_ADDR). Each CLI command family that needs the running
// router registers its endpoints here with one admin.Register... call.
// Read the admin package comment before adding anything: this API has no
// authentication and must never return a secret.
func newAdminHandler(cfg *config.Config, keys *providerkeys.Keys, enabled func(string) bool, apiKeys *apikeys.Store) http.Handler {
	mux := http.NewServeMux()
	admin.RegisterProviders(mux, admin.Providers{Config: cfg, Keys: keys, Enabled: enabled})
	admin.RegisterAPIKeys(mux, apiKeys)
	return admin.Guard(mux)
}
