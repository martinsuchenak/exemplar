package audit

import (
	"net/http"

	"github.com/martinsuchenak/exemplar/internal/rest"
)

// Handler serves the audit log over HTTP. The service allows admins only.
type Handler struct {
	service *Service
}

// NewHandler returns a Handler backed by s.
func NewHandler(s *Service) *Handler {
	return &Handler{service: s}
}

// List returns audit events in order. Query parameters: account_id, action,
// target_type, target_id, after_seq (paging cursor), limit.
func (h *Handler) List(w http.ResponseWriter, r *http.Request) {
	p, ok := rest.Caller(w, r)
	if !ok {
		return
	}
	afterSeq, err := rest.QueryInt(r, "after_seq")
	if err != nil {
		rest.RespondServiceError(w, err)
		return
	}
	limit, err := rest.QueryInt(r, "limit")
	if err != nil {
		rest.RespondServiceError(w, err)
		return
	}
	q := r.URL.Query()
	events, err := h.service.List(r.Context(), p, Filter{
		AccountID:  q.Get("account_id"),
		Action:     q.Get("action"),
		TargetType: q.Get("target_type"),
		TargetID:   q.Get("target_id"),
		AfterSeq:   int64(afterSeq),
		Limit:      limit,
	})
	if err != nil {
		rest.RespondServiceError(w, err)
		return
	}
	rest.RespondJSON(w, http.StatusOK, events)
}
