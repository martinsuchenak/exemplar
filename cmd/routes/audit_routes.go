package routes

import (
	"net/http"

	"github.com/martinsuchenak/exemplar/internal/audit"
)

func init() {
	Register(registerAuditRoutes)
}

func registerAuditRoutes(mux *http.ServeMux) {
	h := audit.NewHandler(services.Audit)
	mux.Handle("GET /api/v1/audit", authenticated(h.List))
}
