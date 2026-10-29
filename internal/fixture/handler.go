package fixture

import (
	"io"
	"mime"
	"net/http"
	"strconv"
	"strings"

	"github.com/martinsuchenak/exemplar/internal/apperr"
	"github.com/martinsuchenak/exemplar/internal/rest"
)

// Handler serves the fixture endpoints over HTTP. It only parses requests and
// formats responses; the service decides what the caller may do.
type Handler struct {
	service *Service
}

// NewHandler returns a Handler backed by s.
func NewHandler(s *Service) *Handler {
	return &Handler{service: s}
}

// List returns a dataset's fixtures. Query parameters: kind, format,
// scenario, tag, review_state, record_schema, person_id, query, limit, offset.
func (h *Handler) List(w http.ResponseWriter, r *http.Request) {
	p, ok := rest.Caller(w, r)
	if !ok {
		return
	}
	q := r.URL.Query()
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
	list, err := h.service.List(r.Context(), p, r.PathValue("id"), ListFilter{
		Kind:         Kind(q.Get("kind")),
		Format:       q.Get("format"),
		Scenario:     q.Get("scenario"),
		Tag:          q.Get("tag"),
		ReviewState:  ReviewState(q.Get("review_state")),
		RecordSchema: q.Get("record_schema"),
		PersonID:     q.Get("person_id"),
		Query:        q.Get("query"),
		Limit:        limit,
		Offset:       offset,
	})
	if err != nil {
		rest.RespondServiceError(w, err)
		return
	}
	rest.RespondJSON(w, http.StatusOK, list)
}

// Upload adds a fixture to a dataset. The request body is the raw content;
// metadata comes from query parameters: name (required), kind, format,
// mime_type, scenario, tags (comma-separated), source, generator,
// record_schema.
func (h *Handler) Upload(w http.ResponseWriter, r *http.Request) {
	p, ok := rest.Caller(w, r)
	if !ok {
		return
	}
	rest.AllowLongUpload(w, r)
	q := r.URL.Query()
	f, err := h.service.Create(r.Context(), p, CreateInput{
		DatasetID:    r.PathValue("id"),
		Name:         q.Get("name"),
		Kind:         Kind(q.Get("kind")),
		Format:       q.Get("format"),
		MimeType:     q.Get("mime_type"),
		Scenario:     q.Get("scenario"),
		Tags:         rest.QueryList(r, "tags"),
		Source:       Source(q.Get("source")),
		Generator:    q.Get("generator"),
		RecordSchema: q.Get("record_schema"),
		Content:      r.Body,
	})
	if err != nil {
		rest.RespondServiceError(w, err)
		return
	}
	rest.RespondJSON(w, http.StatusCreated, f)
}

// Get returns a fixture's metadata.
func (h *Handler) Get(w http.ResponseWriter, r *http.Request) {
	p, ok := rest.Caller(w, r)
	if !ok {
		return
	}
	f, err := h.service.Get(r.Context(), p, r.PathValue("id"))
	if err != nil {
		rest.RespondServiceError(w, err)
		return
	}
	rest.RespondJSON(w, http.StatusOK, f)
}

// Content streams a fixture's content as a download named after the fixture.
func (h *Handler) Content(w http.ResponseWriter, r *http.Request) {
	p, ok := rest.Caller(w, r)
	if !ok {
		return
	}
	w = rest.AllowLongDownload(w)
	body, f, err := h.service.Open(r.Context(), p, r.PathValue("id"))
	if err != nil {
		rest.RespondServiceError(w, err)
		return
	}
	defer func() { _ = body.Close() }()

	w.Header().Set("Content-Type", f.MimeType)
	w.Header().Set("Content-Length", strconv.FormatInt(f.Size, 10))
	w.Header().Set("Content-Disposition", attachment(f.Name))
	// Fixtures are test data, often HTML or email; never let a browser run them.
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Security-Policy", "sandbox")
	w.WriteHeader(http.StatusOK)
	_, _ = io.Copy(w, body)
}

// Delete removes a fixture.
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

// ReviewInput is the body of POST /api/v1/fixtures/{id}/review.
type ReviewInput struct {
	Approve *bool `json:"approve"`
}

// Review approves or rejects a real-derived fixture.
func (h *Handler) Review(w http.ResponseWriter, r *http.Request) {
	p, ok := rest.Caller(w, r)
	if !ok {
		return
	}
	var in ReviewInput
	err := rest.DecodeJSON(r, &in)
	if err == nil && in.Approve == nil {
		err = apperr.ErrInvalid
	}
	if err != nil {
		rest.RespondBodyError(w, err, `body must be {"approve": true} or {"approve": false}`)
		return
	}
	f, err := h.service.Review(r.Context(), p, r.PathValue("id"), *in.Approve)
	if err != nil {
		rest.RespondServiceError(w, err)
		return
	}
	rest.RespondJSON(w, http.StatusOK, f)
}

// PurgeInput is the body of POST /api/v1/purge.
type PurgeInput struct {
	IngestBatch string `json:"ingest_batch"`
}

// PurgeResult is the response of POST /api/v1/purge.
type PurgeResult struct {
	IngestBatch string `json:"ingest_batch"`
	Deleted     int64  `json:"deleted"`
}

// Purge deletes every fixture from an ingest batch.
func (h *Handler) Purge(w http.ResponseWriter, r *http.Request) {
	p, ok := rest.Caller(w, r)
	if !ok {
		return
	}
	var in PurgeInput
	if err := rest.DecodeJSON(r, &in); err != nil {
		rest.RespondBodyError(w, err, "invalid request body")
		return
	}
	n, err := h.service.PurgeBatch(r.Context(), p, in.IngestBatch)
	if err != nil {
		rest.RespondServiceError(w, err)
		return
	}
	rest.RespondJSON(w, http.StatusOK, PurgeResult{IngestBatch: in.IngestBatch, Deleted: n})
}

// attachment is a Content-Disposition that always says attachment, so a
// browser saves fixture content instead of rendering it. A name the header
// cannot carry is simplified, or left out.
func attachment(name string) string {
	if v := mime.FormatMediaType("attachment", map[string]string{"filename": name}); v != "" {
		return v
	}
	safe := strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f || r == '"' || r == '\\' || r > 0x7e {
			return '_'
		}
		return r
	}, name)
	if v := mime.FormatMediaType("attachment", map[string]string{"filename": safe}); v != "" {
		return v
	}
	return "attachment"
}
