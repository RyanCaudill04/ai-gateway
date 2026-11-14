// Package api holds the router's client-facing HTTP endpoints.
package api

import (
	"encoding/json"
	"net/http"
	"sort"

	"github.com/RyanCaudill04/ai-gateway/router/internal/config"
)

// EnabledFunc says whether a provider can be used right now. Handlers call it
// on every request instead of reading a startup snapshot, because provider
// keys can be registered while the router runs (#40) and the model list must
// follow without a restart.
type EnabledFunc func(provider string) bool

// Model is one entry of the OpenAI "list models" response. The first four
// fields are what the official SDKs expect; the rest are for the UI.
type Model struct {
	ID      string `json:"id"`
	Object  string `json:"object"` // always "model"
	Created int64  `json:"created"`
	OwnedBy string `json:"owned_by"`

	// ContextLength is the largest prompt (in tokens) this model accepts.
	// The UI compares it against the prompt to warn before sending.
	ContextLength int `json:"context_length"`
	// Provider is the provider name for a real model. A route has no single
	// provider (it may fall through several), so it is empty there.
	Provider string `json:"provider"`
	// Local means the request never leaves this machine. For a route, that is
	// true only when every usable entry is local, so `private` is always true.
	Local bool `json:"local"`
	// Virtual is true for routes (fast, long-context, private, ...).
	Virtual bool `json:"virtual"`
}

type modelList struct {
	Object string  `json:"object"`
	Data   []Model `json:"data"`
}

// gatewayOwner is owned_by for routes, which belong to the gateway itself.
const gatewayOwner = "gateway"

// Models serves GET /v1/models: routes first, then every real model of an
// enabled provider, each group sorted by ID so the list is stable.
//
// `created` is always 0: the config has no creation times, and an invented
// timestamp would change on every restart. The SDKs only need it to be an int.
func Models(cfg *config.Config, enabled EnabledFunc) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		list := modelList{Object: "list", Data: listModels(cfg, enabled)}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		json.NewEncoder(w).Encode(list)
	})
}

func listModels(cfg *config.Config, enabled EnabledFunc) []Model {
	// Non-nil so an empty list encodes as [] (SDKs choke on null).
	out := []Model{}

	routeNames := make([]string, 0, len(cfg.Routes))
	for name := range cfg.Routes {
		routeNames = append(routeNames, name)
	}
	sort.Strings(routeNames)
	for _, name := range routeNames {
		if m, ok := routeModel(cfg, name, enabled); ok {
			out = append(out, m)
		}
	}

	var real []Model
	for name, p := range cfg.Providers {
		if !enabled(name) {
			continue
		}
		for _, m := range p.Models {
			real = append(real, Model{
				ID:            name + "/" + m.ID,
				Object:        "model",
				OwnedBy:       name,
				ContextLength: m.ContextLength,
				Provider:      name,
				Local:         p.Local,
			})
		}
	}
	sort.Slice(real, func(i, j int) bool { return real[i].ID < real[j].ID })
	return append(out, real...)
}

// routeModel describes a route using only its entries whose provider is
// enabled. It reports false when none are, so the UI never offers a model that
// would fail every request.
//
// context_length is the largest among the usable entries. A route's entries
// differ (fast tries a big model before smaller fallbacks), and the router
// skips an entry the prompt doesn't fit in, so the route can serve any prompt
// that at least one usable entry fits. Taking the minimum instead would make
// `long-context` look as small as its smallest fallback and the UI would
// wrongly warn about prompts it can handle.
func routeModel(cfg *config.Config, name string, enabled EnabledFunc) (Model, bool) {
	m := Model{ID: name, Object: "model", OwnedBy: gatewayOwner, Virtual: true, Local: true}
	usable := 0
	for _, id := range cfg.Routes[name] {
		p, model, ok := cfg.Lookup(id)
		if !ok || !enabled(p.Name) {
			continue
		}
		usable++
		if model.ContextLength > m.ContextLength {
			m.ContextLength = model.ContextLength
		}
		if !p.Local {
			m.Local = false
		}
	}
	return m, usable > 0
}
