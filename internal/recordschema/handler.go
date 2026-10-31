package recordschema

import (
	"net/http"

	"github.com/martinsuchenak/exemplar/internal/rest"
)

// Handler serves record schemas over HTTP. It only parses requests and
// formats responses; the service decides what the caller may do. The list
// is the generate handler's (GET /api/v1/record-schemas).
type Handler struct {
	service *Service
}

// NewHandler returns a Handler backed by s.
func NewHandler(s *Service) *Handler {
	return &Handler{service: s}
}

// Body is a schema file sent to add or change a schema. Version, when
// changing one, must be its current version (0 skips the check).
type Body struct {
	TOML    string `json:"toml"`
	Version int    `json:"version,omitempty"`
}

// Get returns one schema with its file; ?source=api asks for the API
// schema of that name even when a server file has taken it.
func (h *Handler) Get(w http.ResponseWriter, r *http.Request) {
	p, ok := rest.Caller(w, r)
	if !ok {
		return
	}
	source := r.URL.Query().Get("source")
	if source != "" && source != "api" {
		rest.RespondError(w, http.StatusBadRequest, "source must be api, or not given")
		return
	}
	d, err := h.service.Get(r.Context(), p, r.PathValue("name"), source == "api")
	if err != nil {
		rest.RespondServiceError(w, err)
		return
	}
	rest.RespondJSON(w, http.StatusOK, d)
}

// Create adds a schema from a Body.
func (h *Handler) Create(w http.ResponseWriter, r *http.Request) {
	p, ok := rest.Caller(w, r)
	if !ok {
		return
	}
	var in Body
	if err := rest.DecodeJSON(r, &in); err != nil {
		rest.RespondBodyError(w, err, `want {"toml": "<the schema file>"}`)
		return
	}
	d, err := h.service.Create(r.Context(), p, in.TOML)
	if err != nil {
		rest.RespondServiceError(w, err)
		return
	}
	rest.RespondJSON(w, http.StatusCreated, d)
}

// Update replaces a schema's file from a Body.
func (h *Handler) Update(w http.ResponseWriter, r *http.Request) {
	p, ok := rest.Caller(w, r)
	if !ok {
		return
	}
	var in Body
	if err := rest.DecodeJSON(r, &in); err != nil {
		rest.RespondBodyError(w, err, `want {"toml": "<the schema file>", "version": <its current version, optional>}`)
		return
	}
	d, err := h.service.Update(r.Context(), p, r.PathValue("name"), in.TOML, in.Version)
	if err != nil {
		rest.RespondServiceError(w, err)
		return
	}
	rest.RespondJSON(w, http.StatusOK, d)
}

// Delete removes a schema.
func (h *Handler) Delete(w http.ResponseWriter, r *http.Request) {
	p, ok := rest.Caller(w, r)
	if !ok {
		return
	}
	if err := h.service.Delete(r.Context(), p, r.PathValue("name")); err != nil {
		rest.RespondServiceError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
