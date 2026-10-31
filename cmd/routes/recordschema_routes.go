package routes

import (
	"net/http"

	"github.com/martinsuchenak/exemplar/internal/recordschema"
)

func init() {
	Register(registerRecordSchemaRoutes)
}

// The list, GET /api/v1/record-schemas, is with the generate routes.
func registerRecordSchemaRoutes(mux *http.ServeMux) {
	h := recordschema.NewHandler(services.RecordSchemas)
	mux.Handle("POST /api/v1/record-schemas", authenticated(h.Create))
	mux.Handle("GET /api/v1/record-schemas/{name}", authenticated(h.Get))
	mux.Handle("PUT /api/v1/record-schemas/{name}", authenticated(h.Update))
	mux.Handle("DELETE /api/v1/record-schemas/{name}", authenticated(h.Delete))
}
