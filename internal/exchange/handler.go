package exchange

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"time"

	"github.com/martinsuchenak/exemplar/internal/apperr"
	"github.com/martinsuchenak/exemplar/internal/rest"
)

// capped reports going over the import size limit as apperr.ErrTooLarge.
type capped struct {
	r     io.Reader
	limit int64
}

// Read implements io.Reader.
func (c capped) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	var tooLarge *http.MaxBytesError
	if errors.As(err, &tooLarge) {
		err = fmt.Errorf("%w: the archive is over %d bytes", apperr.ErrTooLarge, c.limit)
	}
	return n, err
}

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
// aborts the connection, so the client sees an error rather than a cleanly
// ended, truncated archive.
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
	if err := export.WriteTo(r.Context(), w); err != nil {
		panic(http.ErrAbortHandler)
	}
}

// Import reads an export archive from the request body (application/gzip)
// and returns what was added.
func (h *Handler) Import(w http.ResponseWriter, r *http.Request) {
	p, ok := rest.Caller(w, r)
	if !ok {
		return
	}
	_ = http.NewResponseController(w).SetReadDeadline(time.Time{})
	body := io.Reader(r.Body)
	if limit := h.service.MaxImportBytes(); limit > 0 {
		body = capped{r: http.MaxBytesReader(w, r.Body, limit), limit: limit}
	}
	res, err := h.service.Import(r.Context(), p, body)
	if err != nil {
		rest.RespondServiceError(w, err)
		return
	}
	rest.RespondJSON(w, http.StatusCreated, res)
}
