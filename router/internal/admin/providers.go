package admin

import (
	"errors"
	"log/slog"
	"net/http"
	"sort"
	"strings"

	"github.com/RyanCaudill04/ai-gateway/router/internal/config"
	"github.com/RyanCaudill04/ai-gateway/router/internal/providerkeys"
)

// ProviderStatus is one provider in GET /admin/providers and the response to
// a key change. It never carries a key, only its last 4 characters.
type ProviderStatus struct {
	Provider string `json:"provider"`
	// InConfig is false for a key left behind after its provider was removed
	// from providers.yaml. Such a key is unused and can be removed.
	InConfig bool `json:"in_config"`
	Local    bool `json:"local"`
	Enabled  bool `json:"enabled"`
	HasKey   bool `json:"has_key"`
	// KeyUnreadable means a key is stored but couldn't be decrypted at
	// startup (wrong GATEWAY_SECRET_KEY). Register it again.
	KeyUnreadable bool   `json:"key_unreadable,omitempty"`
	KeyLast4      string `json:"key_last4,omitempty"`
	UpdatedAt     string `json:"updated_at,omitempty"`
}

// SetKeyRequest is the body of PUT /admin/providers/{provider}/key.
type SetKeyRequest struct {
	Key string `json:"key"`
	// Test makes one cheap authenticated call to the provider first, and
	// saves nothing if it fails.
	Test bool `json:"test"`
}

// Providers is what the provider endpoints need from the router.
type Providers struct {
	Config *config.Config
	Keys   *providerkeys.Keys
	// Enabled is the router's request-time check, so the admin API reports
	// exactly what requests will see.
	Enabled func(provider string) bool
	// HTTPClient is used by the --test call. nil means http.DefaultClient.
	HTTPClient *http.Client
}

// RegisterProviders adds the provider key endpoints to mux:
//
//	GET    /admin/providers                 every provider and its key status
//	PUT    /admin/providers/{provider}/key  register or replace a key (SetKeyRequest)
//	DELETE /admin/providers/{provider}/key  remove a key
func RegisterProviders(mux *http.ServeMux, p Providers) {
	mux.HandleFunc("GET /admin/providers", p.list)
	mux.HandleFunc("PUT /admin/providers/{provider}/key", p.setKey)
	mux.HandleFunc("DELETE /admin/providers/{provider}/key", p.removeKey)
}

func (p Providers) status(name string) ProviderStatus {
	s := ProviderStatus{Provider: name, Enabled: p.Enabled(name)}
	if cp := p.Config.Providers[name]; cp != nil {
		s.InConfig, s.Local = true, cp.Local
	}
	if info, ok := p.Keys.Info(name); ok {
		s.HasKey = !info.Unreadable
		s.KeyUnreadable = info.Unreadable
		s.KeyLast4 = info.Last4
		s.UpdatedAt = info.UpdatedAt
	}
	return s
}

func (p Providers) list(w http.ResponseWriter, r *http.Request) {
	names := map[string]bool{}
	for name := range p.Config.Providers {
		names[name] = true
	}
	for _, info := range p.Keys.List() {
		names[info.Provider] = true
	}
	out := make([]ProviderStatus, 0, len(names))
	for name := range names {
		out = append(out, p.status(name))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Provider < out[j].Provider })
	WriteJSON(w, http.StatusOK, out)
}

// unknownProvider answers for a name that isn't in providers.yaml. It doesn't
// repeat the name: someone who typed the key where the provider goes
// (`set-key sk-...`) would otherwise see their key printed back.
func (p Providers) unknownProvider(w http.ResponseWriter) {
	names := make([]string, 0, len(p.Config.Providers))
	for name := range p.Config.Providers {
		names = append(names, name)
	}
	sort.Strings(names)
	WriteError(w, http.StatusNotFound, "unknown provider; providers.yaml has: "+strings.Join(names, ", "))
}

func (p Providers) setKey(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("provider")
	cp := p.Config.Providers[name]
	if cp == nil {
		p.unknownProvider(w)
		return
	}
	if cp.Local {
		WriteError(w, http.StatusBadRequest, name+" is a local provider and takes no key")
		return
	}

	var req SetKeyRequest
	if err := DecodeJSON(r, &req); err != nil {
		WriteError(w, http.StatusBadRequest, err.Error())
		return
	}
	key, err := providerkeys.CleanKey(req.Key)
	if err != nil {
		WriteError(w, http.StatusBadRequest, err.Error())
		return
	}

	if req.Test {
		if err := providerkeys.Check(r.Context(), p.HTTPClient, cp.BaseURL, key); err != nil {
			slog.Warn("provider key test failed; nothing saved", "provider", name, "err", err)
			WriteError(w, http.StatusUnprocessableEntity, "key test failed, nothing saved: "+providerkeys.Redact(err.Error(), key))
			return
		}
	}

	info, err := p.Keys.Set(r.Context(), name, key)
	if err != nil {
		slog.Error("saving provider key failed", "provider", name, "err", err)
		WriteError(w, http.StatusInternalServerError, "saving the key failed; see the router log")
		return
	}
	slog.Info("provider key registered", "provider", name, "last4", info.Last4, "tested", req.Test)
	WriteJSON(w, http.StatusOK, p.status(name))
}

func (p Providers) removeKey(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("provider")
	// A key for a provider no longer in providers.yaml can still be removed;
	// anything else that isn't configured is unknown.
	if _, stored := p.Keys.Info(name); !stored && p.Config.Providers[name] == nil {
		p.unknownProvider(w)
		return
	}
	err := p.Keys.Remove(r.Context(), name)
	switch {
	case errors.Is(err, providerkeys.ErrNoKey):
		WriteError(w, http.StatusNotFound, "no key registered for "+name)
		return
	case err != nil:
		slog.Error("removing provider key failed", "provider", name, "err", err)
		WriteError(w, http.StatusInternalServerError, "removing the key failed; see the router log")
		return
	}
	slog.Info("provider key removed", "provider", name)
	WriteJSON(w, http.StatusOK, p.status(name))
}
