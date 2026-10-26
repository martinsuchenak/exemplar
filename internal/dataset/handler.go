package dataset

import (
	"errors"
	"io"
	"net/http"

	"github.com/martinsuchenak/exemplar/internal/rest"
)

// Handler serves the dataset endpoints over HTTP. It only parses requests and
// formats responses; the service decides what the caller may do.
type Handler struct {
	service *Service
}

// NewHandler returns a Handler backed by s.
func NewHandler(s *Service) *Handler {
	return &Handler{service: s}
}

// List returns the datasets the caller can read (?scope=, ?name=).
func (h *Handler) List(w http.ResponseWriter, r *http.Request) {
	p, ok := rest.Caller(w, r)
	if !ok {
		return
	}
	q := r.URL.Query()
	list, err := h.service.List(r.Context(), p, ListFilter{Scope: Scope(q.Get("scope")), Name: q.Get("name")})
	if err != nil {
		rest.RespondServiceError(w, err)
		return
	}
	rest.RespondJSON(w, http.StatusOK, list)
}

// Create creates a dataset from a CreateInput body.
func (h *Handler) Create(w http.ResponseWriter, r *http.Request) {
	p, ok := rest.Caller(w, r)
	if !ok {
		return
	}
	var in CreateInput
	if err := rest.DecodeJSON(r, &in); err != nil {
		rest.RespondBodyError(w, err, "invalid request body")
		return
	}
	d, err := h.service.Create(r.Context(), p, in)
	if err != nil {
		rest.RespondServiceError(w, err)
		return
	}
	rest.RespondJSON(w, http.StatusCreated, d)
}

// Get returns one dataset.
func (h *Handler) Get(w http.ResponseWriter, r *http.Request) {
	p, ok := rest.Caller(w, r)
	if !ok {
		return
	}
	d, err := h.service.Get(r.Context(), p, r.PathValue("id"))
	if err != nil {
		rest.RespondServiceError(w, err)
		return
	}
	rest.RespondJSON(w, http.StatusOK, d)
}

// Delete deletes a dataset and its fixtures.
func (h *Handler) Delete(w http.ResponseWriter, r *http.Request) {
	p, ok := rest.Caller(w, r)
	if !ok {
		return
	}
	if err := h.service.Delete(r.Context(), p, r.PathValue("id")); err != nil {
		rest.RespondServiceError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// Freeze makes a dataset's contents read-only.
func (h *Handler) Freeze(w http.ResponseWriter, r *http.Request) {
	p, ok := rest.Caller(w, r)
	if !ok {
		return
	}
	d, err := h.service.Freeze(r.Context(), p, r.PathValue("id"))
	if err != nil {
		rest.RespondServiceError(w, err)
		return
	}
	rest.RespondJSON(w, http.StatusOK, d)
}

// Fork copies a dataset into a new workspace; the body is a ForkInput.
func (h *Handler) Fork(w http.ResponseWriter, r *http.Request) {
	p, ok := rest.Caller(w, r)
	if !ok {
		return
	}
	var in ForkInput
	if err := decodeOptional(r, &in); err != nil {
		rest.RespondBodyError(w, err, "invalid request body")
		return
	}
	res, err := h.service.Fork(r.Context(), p, r.PathValue("id"), in)
	if err != nil {
		rest.RespondServiceError(w, err)
		return
	}
	rest.RespondJSON(w, http.StatusCreated, res)
}

// Promote copies a dataset into the global library; the body is a PromoteInput.
func (h *Handler) Promote(w http.ResponseWriter, r *http.Request) {
	p, ok := rest.Caller(w, r)
	if !ok {
		return
	}
	var in PromoteInput
	if err := decodeOptional(r, &in); err != nil {
		rest.RespondBodyError(w, err, "invalid request body")
		return
	}
	res, err := h.service.Promote(r.Context(), p, r.PathValue("id"), in)
	if err != nil {
		rest.RespondServiceError(w, err)
		return
	}
	rest.RespondJSON(w, http.StatusCreated, res)
}

// decodeOptional decodes a JSON body when there is one; an empty body leaves
// v at its defaults.
func decodeOptional(r *http.Request, v interface{}) error {
	if err := rest.DecodeJSON(r, v); err != nil && !errors.Is(err, io.EOF) {
		return err
	}
	return nil
}
