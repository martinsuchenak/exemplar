package routes

import (
	"net/http"

	"github.com/martinsuchenak/exemplar/internal/account"
)

func init() {
	Register(registerAccountRoutes)
}

func registerAccountRoutes(mux *http.ServeMux) {
	h := account.NewHandler(services.Accounts)
	mux.Handle("GET /api/v1/me", authenticated(h.Me))
	mux.Handle("GET /api/v1/accounts", authenticated(h.ListAccounts))
	mux.Handle("POST /api/v1/accounts", authenticated(h.CreateAccount))
	mux.Handle("GET /api/v1/accounts/{id}", authenticated(h.GetAccount))
	mux.Handle("POST /api/v1/accounts/{id}/disable", authenticated(h.DisableAccount))
	mux.Handle("GET /api/v1/keys", authenticated(h.ListKeys))
	mux.Handle("POST /api/v1/keys", authenticated(h.CreateKey))
	mux.Handle("DELETE /api/v1/keys/{id}", authenticated(h.RevokeKey))
}
