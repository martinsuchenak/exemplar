package routes

import (
	"net/http"

	"github.com/martinsuchenak/exemplar/internal/ingest"
)

func init() {
	Register(registerIngestRoutes)
}

func registerIngestRoutes(mux *http.ServeMux) {
	h := ingest.NewHandler(services.Ingest)
	mux.Handle("POST /api/v1/datasets/{id}/ingest", authenticated(h.Ingest))
}
