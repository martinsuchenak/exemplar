package routes

import (
	"net/http"

	"github.com/martinsuchenak/exemplar/internal/generate"
)

func init() {
	Register(registerGenerateRoutes)
}

func registerGenerateRoutes(mux *http.ServeMux) {
	h := generate.NewHandler(services.Generate)
	mux.Handle("POST /api/v1/datasets/{id}/generate", authenticated(h.Generate))
	mux.Handle("GET /api/v1/record-schemas", authenticated(h.Schemas))
}
