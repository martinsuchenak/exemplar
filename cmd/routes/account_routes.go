package routes

import (
	"net/http"

	"github.com/martinsuchenak/exemplar/internal/account"
)

func init() {
	Register(registerAccountRoutes)
}

func registerAccountRoutes(mux *http.ServeMux) {
	accountHandler := account.NewHandler(services.Accounts)
	mux.Handle("GET /api/v1/me", authenticated(accountHandler.Me))
}
