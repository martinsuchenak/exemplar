package account

import (
	"net/http"

	"github.com/martinsuchenak/exemplar/internal/principal"
	"github.com/martinsuchenak/exemplar/internal/rest"
)

// Handler serves the account endpoints over HTTP.
type Handler struct {
	service *Service
}

// NewHandler returns a Handler backed by s.
func NewHandler(s *Service) *Handler {
	return &Handler{service: s}
}

type meResponse struct {
	Account *Account            `json:"account"`
	Key     principal.Principal `json:"principal"`
}

// Me returns the calling account and the key it used.
func (h *Handler) Me(w http.ResponseWriter, r *http.Request) {
	p, ok := principal.FromContext(r.Context())
	if !ok {
		rest.RespondError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	acc, err := h.service.GetAccount(r.Context(), p, p.AccountID)
	if err != nil {
		rest.RespondServiceError(w, err)
		return
	}
	rest.RespondJSON(w, http.StatusOK, meResponse{Account: acc, Key: p})
}
