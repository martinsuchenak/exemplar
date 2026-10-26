package routes

import (
	"net/http"

	"github.com/martinsuchenak/exemplar/internal/fixture"
)

func init() {
	Register(registerFixtureRoutes)
}

func registerFixtureRoutes(mux *http.ServeMux) {
	h := fixture.NewHandler(services.Fixtures)
	mux.Handle("GET /api/v1/datasets/{id}/fixtures", authenticated(h.List))
	mux.Handle("POST /api/v1/datasets/{id}/fixtures", authenticated(h.Upload))
	mux.Handle("GET /api/v1/fixtures/{id}", authenticated(h.Get))
	mux.Handle("GET /api/v1/fixtures/{id}/content", authenticated(h.Content))
	mux.Handle("DELETE /api/v1/fixtures/{id}", authenticated(h.Delete))
	mux.Handle("POST /api/v1/fixtures/{id}/review", authenticated(h.Review))
	mux.Handle("POST /api/v1/purge", authenticated(h.Purge))
}
