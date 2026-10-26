package ingest

import (
	"errors"
	"io"
	"net/http"
	"strconv"

	"github.com/martinsuchenak/exemplar/internal/rest"
)

// Handler serves ingest over HTTP. It only parses the upload and formats the
// result; the service decides what the caller may do.
type Handler struct {
	service *Service
}

// NewHandler returns a Handler backed by s.
func NewHandler(s *Service) *Handler {
	return &Handler{service: s}
}

// maxField caps a form field other than a file.
const maxField = 1024

// Ingest takes a multipart/form-data upload: any number of "file" parts plus
// optional "profile", "batch" and "dry_run" fields. Parts are read into
// memory as they stream in, never into temporary files, so raw emails are
// not written to the server's disk. The response may hold original data (a
// dry-run report), so it is marked no-store.
func (h *Handler) Ingest(w http.ResponseWriter, r *http.Request) {
	p, ok := rest.Caller(w, r)
	if !ok {
		return
	}
	if limit := h.service.MaxBytes(); limit > 0 {
		r.Body = http.MaxBytesReader(w, r.Body, limit+1<<20)
	}
	mr, err := r.MultipartReader()
	if err != nil {
		rest.RespondError(w, http.StatusBadRequest, "body must be multipart/form-data with file parts")
		return
	}
	var req Request
	var files []File
	for {
		part, err := mr.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			respondReadError(w, err)
			return
		}
		switch part.FormName() {
		case "file":
			data, err := io.ReadAll(part)
			if err != nil {
				respondReadError(w, err)
				return
			}
			files = append(files, File{Name: part.FileName(), Data: data})
		case "profile", "batch", "dry_run":
			v, err := io.ReadAll(io.LimitReader(part, maxField))
			if err != nil {
				respondReadError(w, err)
				return
			}
			switch part.FormName() {
			case "profile":
				req.Profile = string(v)
			case "batch":
				req.Batch = string(v)
			default:
				if req.DryRun, err = strconv.ParseBool(string(v)); err != nil {
					rest.RespondError(w, http.StatusBadRequest, "dry_run must be true or false")
					return
				}
			}
		}
	}
	res, err := h.service.Ingest(r.Context(), p, r.PathValue("id"), req, files)
	if err != nil {
		rest.RespondServiceError(w, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	status := http.StatusCreated
	if req.DryRun {
		status = http.StatusOK
	}
	rest.RespondJSON(w, status, res)
}

func respondReadError(w http.ResponseWriter, err error) {
	var tooLarge *http.MaxBytesError
	if errors.As(err, &tooLarge) {
		rest.RespondError(w, http.StatusRequestEntityTooLarge, "upload too large")
		return
	}
	rest.RespondError(w, http.StatusBadRequest, "reading upload failed")
}
