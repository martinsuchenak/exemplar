package routes

import (
	"net/http"

	"github.com/martinsuchenak/exemplar/internal/person"
)

func init() {
	Register(registerPersonRoutes)
}

func registerPersonRoutes(mux *http.ServeMux) {
	h := person.NewHandler(services.Persons)
	mux.Handle("GET /api/v1/persons", authenticated(h.List))
	mux.Handle("POST /api/v1/persons", authenticated(h.Generate))
	mux.Handle("GET /api/v1/persons/{id}", authenticated(h.Get))
}
