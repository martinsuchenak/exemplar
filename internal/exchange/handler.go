package exchange

import (
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/martinsuchenak/exemplar/internal/rest"
)

// Handler serves export and import over HTTP. It only parses requests and
// formats responses; the services decide what the caller may do.
type Handler struct {
	service *Service
}

// NewHandler returns a Handler backed by s.
func NewHandler(s *Service) *Handler {
	return &Handler{service: s}
}

// Export streams an export archive for the datasets named in the JSON body.
// Everything that can fail with a status code fails before the first byte;
// the counts are sent as X-Exemplar-* headers. A failure while streaming
// truncates the archive, which import then refuses.
func (h *Handler) Export(w http.ResponseWriter, r *http.Request) {
	p, ok := rest.Caller(w, r)
	if !ok {
		return
	}
	var req ExportRequest
	if err := rest.DecodeJSON(r, &req); err != nil {
		rest.RespondError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	export, err := h.service.PrepareExport(r.Context(), p, req)
	if err != nil {
		rest.RespondServiceError(w, err)
		return
	}
	// Archives can take longer than the server's usual write timeout.
	_ = http.NewResponseController(w).SetWriteDeadline(time.Time{})
	name := fmt.Sprintf("exemplar-export-%s.tar.gz", time.Now().UTC().Format("20060102-150405"))
	hdr := w.Header()
	hdr.Set("Content-Type", "application/gzip")
	hdr.Set("Content-Disposition", `attachment; filename="`+name+`"`)
	hdr.Set("Cache-Control", "no-store")
	hdr.Set("X-Content-Type-Options", "nosniff")
	for k, v := range export.Counts() {
		hdr.Set("X-Exemplar-"+k, strconv.Itoa(v))
	}
	hdr.Set("X-Exemplar-Skipped", strconv.Itoa(export.Skipped))
	w.WriteHeader(http.StatusOK)
	_ = export.WriteTo(r.Context(), w)
}

// Import reads an export archive from the request body (application/gzip)
// and returns what was added.
func (h *Handler) Import(w http.ResponseWriter, r *http.Request) {
	p, ok := rest.Caller(w, r)
	if !ok {
		return
	}
	_ = http.NewResponseController(w).SetReadDeadline(time.Time{})
	res, err := h.service.Import(r.Context(), p, r.Body)
	if err != nil {
		rest.RespondServiceError(w, err)
		return
	}
	rest.RespondJSON(w, http.StatusCreated, res)
}
