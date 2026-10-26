// Package routes registers the HTTP routes. Route files only wire handlers
// and authentication; access checks happen in the services.
package routes

import (
	"encoding/json"
	"net/http"
	"runtime"

	"github.com/martinsuchenak/exemplar/internal/app"
	"github.com/martinsuchenak/exemplar/internal/auth"
	"github.com/martinsuchenak/exemplar/internal/rest"
)

var registrations []func(*http.ServeMux)

// services is set by RegisterRoutes before any registration runs, so route
// files keep the scaffolder's func(*http.ServeMux) signature.
var services *app.App

// Register adds a route registration; route files call it from init.
func Register(fn func(*http.ServeMux)) {
	registrations = append(registrations, fn)
}

// RegisterRoutes adds the health and metrics routes and every registered
// route to mux, giving route files access to the services in a.
func RegisterRoutes(mux *http.ServeMux, a *app.App) {
	services = a
	mux.HandleFunc("GET /health", healthHandler)
	mux.Handle("GET /metrics", authenticated(metricsHandler))

	for _, fn := range registrations {
		fn(mux)
	}
}

// authenticated wraps a handler so it only runs for a valid API key; the
// principal is then in the request context.
func authenticated(h http.HandlerFunc) http.Handler {
	return auth.APIKeyMiddleware(services.Accounts)(h)
}

func healthHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}

// metricsHandler reports process figures to admins. They say little, but
// nothing about the process is anyone else's business.
func metricsHandler(w http.ResponseWriter, r *http.Request) {
	p, ok := rest.Caller(w, r)
	if !ok {
		return
	}
	if !p.IsAdmin() {
		rest.RespondError(w, http.StatusForbidden, "metrics need an admin key")
		return
	}

	w.Header().Set("Content-Type", "application/json")
	var m runtime.MemStats
	runtime.ReadMemStats(&m)
	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"goroutines": runtime.NumGoroutine(),
		"alloc_mb":   m.Alloc / 1024 / 1024,
	})
}
