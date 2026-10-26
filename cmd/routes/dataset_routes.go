package routes

import (
	"net/http"

	"github.com/martinsuchenak/exemplar/internal/dataset"
)

func init() {
	Register(registerDatasetRoutes)
}

func registerDatasetRoutes(mux *http.ServeMux) {
	h := dataset.NewHandler(services.Datasets)
	mux.Handle("GET /api/v1/datasets", authenticated(h.List))
	mux.Handle("POST /api/v1/datasets", authenticated(h.Create))
	mux.Handle("GET /api/v1/datasets/{id}", authenticated(h.Get))
	mux.Handle("DELETE /api/v1/datasets/{id}", authenticated(h.Delete))
	mux.Handle("POST /api/v1/datasets/{id}/freeze", authenticated(h.Freeze))
	mux.Handle("POST /api/v1/datasets/{id}/fork", authenticated(h.Fork))
	mux.Handle("POST /api/v1/datasets/{id}/promote", authenticated(h.Promote))
}
