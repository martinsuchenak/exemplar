package account

import (
	"net/http"

	"github.com/martinsuchenak/exemplar/internal/principal"
	"github.com/martinsuchenak/exemplar/internal/rest"
)

// Handler serves the account and key endpoints over HTTP. It only parses
// requests and formats responses; the service decides what the caller may do.
type Handler struct {
	service *Service
}

// NewHandler returns a Handler backed by s.
func NewHandler(s *Service) *Handler {
	return &Handler{service: s}
}

// MeResponse is the body of GET /api/v1/me.
type MeResponse struct {
	Account   *Account            `json:"account"`
	Principal principal.Principal `json:"principal"`
}

// Me returns the calling account and the key it used.
func (h *Handler) Me(w http.ResponseWriter, r *http.Request) {
	p, ok := rest.Caller(w, r)
	if !ok {
		return
	}
	acc, err := h.service.GetAccount(r.Context(), p, p.AccountID)
	if err != nil {
		rest.RespondServiceError(w, err)
		return
	}
	rest.RespondJSON(w, http.StatusOK, MeResponse{Account: acc, Principal: p})
}

// ListAccounts returns every account to admins, and the caller's own to members.
func (h *Handler) ListAccounts(w http.ResponseWriter, r *http.Request) {
	p, ok := rest.Caller(w, r)
	if !ok {
		return
	}
	accounts, err := h.service.ListAccounts(r.Context(), p)
	if err != nil {
		rest.RespondServiceError(w, err)
		return
	}
	rest.RespondJSON(w, http.StatusOK, accounts)
}

// CreateAccount creates an account from a CreateAccountInput body.
func (h *Handler) CreateAccount(w http.ResponseWriter, r *http.Request) {
	p, ok := rest.Caller(w, r)
	if !ok {
		return
	}
	var in CreateAccountInput
	if err := rest.DecodeJSON(r, &in); err != nil {
		rest.RespondBodyError(w, err, "invalid request body")
		return
	}
	acc, err := h.service.CreateAccount(r.Context(), p, in)
	if err != nil {
		rest.RespondServiceError(w, err)
		return
	}
	rest.RespondJSON(w, http.StatusCreated, acc)
}

// GetAccount returns one account.
func (h *Handler) GetAccount(w http.ResponseWriter, r *http.Request) {
	p, ok := rest.Caller(w, r)
	if !ok {
		return
	}
	acc, err := h.service.GetAccount(r.Context(), p, r.PathValue("id"))
	if err != nil {
		rest.RespondServiceError(w, err)
		return
	}
	rest.RespondJSON(w, http.StatusOK, acc)
}

// DisableAccount disables an account and revokes its keys.
func (h *Handler) DisableAccount(w http.ResponseWriter, r *http.Request) {
	p, ok := rest.Caller(w, r)
	if !ok {
		return
	}
	if err := h.service.DisableAccount(r.Context(), p, r.PathValue("id")); err != nil {
		rest.RespondServiceError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ListKeys returns an account's keys (?account_id=, default the caller's).
func (h *Handler) ListKeys(w http.ResponseWriter, r *http.Request) {
	p, ok := rest.Caller(w, r)
	if !ok {
		return
	}
	keys, err := h.service.ListKeys(r.Context(), p, r.URL.Query().Get("account_id"))
	if err != nil {
		rest.RespondServiceError(w, err)
		return
	}
	rest.RespondJSON(w, http.StatusOK, keys)
}

// CreateKey issues a key from a CreateKeyInput body; the response holds the
// secret, which is never shown again.
func (h *Handler) CreateKey(w http.ResponseWriter, r *http.Request) {
	p, ok := rest.Caller(w, r)
	if !ok {
		return
	}
	var in CreateKeyInput
	if err := rest.DecodeJSON(r, &in); err != nil {
		rest.RespondBodyError(w, err, "invalid request body")
		return
	}
	issued, err := h.service.CreateKey(r.Context(), p, in)
	if err != nil {
		rest.RespondServiceError(w, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	rest.RespondJSON(w, http.StatusCreated, issued)
}

// RevokeKey revokes a key.
func (h *Handler) RevokeKey(w http.ResponseWriter, r *http.Request) {
	p, ok := rest.Caller(w, r)
	if !ok {
		return
	}
	if err := h.service.RevokeKey(r.Context(), p, r.PathValue("id")); err != nil {
		rest.RespondServiceError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
