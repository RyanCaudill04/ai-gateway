package admin

import (
	"encoding/hex"
	"errors"
	"log/slog"
	"net/http"

	"github.com/RyanCaudill04/ai-gateway/router/internal/apikeys"
)

// APIKeyStatus is one service key in GET /admin/keys and the response to a
// create or revoke. It never carries the key or its hash: only the display
// prefix.
type APIKeyStatus struct {
	Name   string `json:"name"`
	Prefix string `json:"prefix"`
	// DailyCap is omitted when the key has no cap.
	DailyCap  int64  `json:"daily_cap,omitempty"`
	UsedToday int64  `json:"used_today"`
	CreatedAt string `json:"created_at"`
	RevokedAt string `json:"revoked_at,omitempty"`
	// AlreadyRevoked is set on a revoke that found the key already revoked.
	AlreadyRevoked bool `json:"already_revoked,omitempty"`
}

// CreateAPIKeyRequest is the body of POST /admin/keys. The CLI generates the
// key itself (apikeys.Generate) and sends only what the router stores, so
// the plain key never crosses this API.
type CreateAPIKeyRequest struct {
	Name   string `json:"name"`
	Prefix string `json:"prefix"`
	// Hash is the SHA-256 of the key, hex-encoded.
	Hash     string `json:"hash"`
	DailyCap int64  `json:"daily_cap,omitempty"`
}

// RegisterAPIKeys adds the service key endpoints to mux:
//
//	GET    /admin/keys         every key, active and revoked
//	POST   /admin/keys         store a new key (CreateAPIKeyRequest)
//	DELETE /admin/keys/{name}  revoke a key (the row stays; see apikeys.Store.Revoke)
func RegisterAPIKeys(mux *http.ServeMux, keys *apikeys.Store) {
	h := apiKeys{keys}
	mux.HandleFunc("GET /admin/keys", h.list)
	mux.HandleFunc("POST /admin/keys", h.create)
	mux.HandleFunc("DELETE /admin/keys/{name}", h.revoke)
}

type apiKeys struct{ keys *apikeys.Store }

func keyStatus(info apikeys.Info) APIKeyStatus {
	return APIKeyStatus{
		Name: info.Name, Prefix: info.Prefix, DailyCap: info.DailyCap, UsedToday: info.UsedToday,
		CreatedAt: info.CreatedAt, RevokedAt: info.RevokedAt,
	}
}

func (h apiKeys) list(w http.ResponseWriter, r *http.Request) {
	infos := h.keys.List()
	out := make([]APIKeyStatus, 0, len(infos))
	for _, info := range infos {
		out = append(out, keyStatus(info))
	}
	WriteJSON(w, http.StatusOK, out)
}

func (h apiKeys) create(w http.ResponseWriter, r *http.Request) {
	var req CreateAPIKeyRequest
	if err := DecodeJSON(r, &req); err != nil {
		WriteError(w, http.StatusBadRequest, err.Error())
		return
	}
	// Validated before anything else, and the name is never echoed when it
	// is invalid (it might be a key pasted in the wrong place).
	if err := apikeys.ValidateName(req.Name); err != nil {
		WriteError(w, http.StatusBadRequest, err.Error())
		return
	}
	if req.DailyCap < 0 {
		WriteError(w, http.StatusBadRequest, "the daily cap must be at least 1 (omit it for no cap)")
		return
	}
	raw, err := hex.DecodeString(req.Hash)
	if err != nil || len(raw) != apikeys.HashSize {
		WriteError(w, http.StatusBadRequest, "hash must be a hex-encoded SHA-256")
		return
	}
	nk := apikeys.NewKey{Name: req.Name, Prefix: req.Prefix, DailyCap: req.DailyCap}
	copy(nk.Hash[:], raw)

	info, err := h.keys.Create(r.Context(), nk)
	switch {
	case errors.Is(err, apikeys.ErrExists):
		WriteError(w, http.StatusConflict, "an active key named "+req.Name+" already exists; revoke it first to replace it")
		return
	case errors.Is(err, apikeys.ErrInvalid), errors.Is(err, apikeys.ErrInvalidName):
		WriteError(w, http.StatusBadRequest, err.Error())
		return
	case err != nil:
		slog.Error("creating api key failed", "service", req.Name, "err", err)
		WriteError(w, http.StatusInternalServerError, "saving the key failed; see the router log")
		return
	}
	slog.Info("api key created", "service", info.Name, "prefix", info.Prefix, "daily_cap", info.DailyCap)
	WriteJSON(w, http.StatusCreated, keyStatus(info))
}

func (h apiKeys) revoke(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if err := apikeys.ValidateName(name); err != nil {
		WriteError(w, http.StatusBadRequest, err.Error())
		return
	}
	info, changed, err := h.keys.Revoke(r.Context(), name)
	switch {
	case errors.Is(err, apikeys.ErrNotFound):
		WriteError(w, http.StatusNotFound, "no key named "+name)
		return
	case err != nil:
		slog.Error("revoking api key failed", "service", name, "err", err)
		WriteError(w, http.StatusInternalServerError, "revoking the key failed; see the router log")
		return
	}
	if changed {
		slog.Info("api key revoked", "service", name)
	}
	st := keyStatus(info)
	st.AlreadyRevoked = !changed
	WriteJSON(w, http.StatusOK, st)
}
