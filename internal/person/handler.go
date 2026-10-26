package person

import (
	"net/http"

	"github.com/martinsuchenak/exemplar/internal/rest"
)

// Handler serves the person registry over HTTP. It only parses requests and
// formats responses; the service decides what the caller may do.
type Handler struct {
	service *Service
}

// NewHandler returns a Handler backed by s.
func NewHandler(s *Service) *Handler {
	return &Handler{service: s}
}

// List returns people (?company=, ?limit=, ?offset=).
func (h *Handler) List(w http.ResponseWriter, r *http.Request) {
	p, ok := rest.Caller(w, r)
	if !ok {
		return
	}
	limit, err := rest.QueryInt(r, "limit")
	if err != nil {
		rest.RespondServiceError(w, err)
		return
	}
	offset, err := rest.QueryInt(r, "offset")
	if err != nil {
		rest.RespondServiceError(w, err)
		return
	}
	people, err := h.service.List(r.Context(), p, ListFilter{Company: r.URL.Query().Get("company"), Limit: limit, Offset: offset})
	if err != nil {
		rest.RespondServiceError(w, err)
		return
	}
	rest.RespondJSON(w, http.StatusOK, people)
}

// Get returns one person.
func (h *Handler) Get(w http.ResponseWriter, r *http.Request) {
	p, ok := rest.Caller(w, r)
	if !ok {
		return
	}
	person, err := h.service.Get(r.Context(), p, r.PathValue("id"))
	if err != nil {
		rest.RespondServiceError(w, err)
		return
	}
	rest.RespondJSON(w, http.StatusOK, person)
}

// GenerateResult is the response of POST /api/v1/persons.
type GenerateResult struct {
	Seed   uint64   `json:"seed"`
	People []Person `json:"people"`
}

// Generate creates new people from a GenerateInput body.
func (h *Handler) Generate(w http.ResponseWriter, r *http.Request) {
	p, ok := rest.Caller(w, r)
	if !ok {
		return
	}
	var in GenerateInput
	if err := rest.DecodeJSON(r, &in); err != nil {
		rest.RespondError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	people, seed, err := h.service.Generate(r.Context(), p, in)
	if err != nil {
		rest.RespondServiceError(w, err)
		return
	}
	rest.RespondJSON(w, http.StatusCreated, GenerateResult{Seed: seed, People: people})
}
