package routes

import (
	"net/http"

	"github.com/martinsuchenak/exemplar/internal/exchange"
)

func init() {
	Register(registerExchangeRoutes)
}

func registerExchangeRoutes(mux *http.ServeMux) {
	h := exchange.NewHandler(services.Exchange)
	mux.Handle("POST /api/v1/export", authenticated(h.Export))
	mux.Handle("POST /api/v1/import", authenticated(h.Import))
}
