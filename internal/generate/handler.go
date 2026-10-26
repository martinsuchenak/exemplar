package generate

import (
	"net/http"

	"github.com/martinsuchenak/exemplar/internal/rest"
)

// Handler serves generation over HTTP. It only parses requests and formats
// responses; the services decide what the caller may do.
type Handler struct {
	service *Service
}

// NewHandler returns a Handler backed by s.
func NewHandler(s *Service) *Handler {
	return &Handler{service: s}
}

// Generate fills a dataset from a Request body and returns the Result.
func (h *Handler) Generate(w http.ResponseWriter, r *http.Request) {
	p, ok := rest.Caller(w, r)
	if !ok {
		return
	}
	var req Request
	if err := rest.DecodeJSON(r, &req); err != nil {
		rest.RespondBodyError(w, err, "invalid request body")
		return
	}
	res, err := h.service.Generate(r.Context(), p, r.PathValue("id"), req)
	if err != nil {
		rest.RespondServiceError(w, err)
		return
	}
	rest.RespondJSON(w, http.StatusCreated, res)
}
