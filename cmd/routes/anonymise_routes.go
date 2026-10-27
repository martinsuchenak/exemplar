package routes

import (
	"net/http"

	"github.com/martinsuchenak/exemplar/internal/scrub"
)

func init() {
	Register(registerAnonymiseRoutes)
}

// The anonymise tool: files in, anonymised files out, nothing stored. Any
// key that can read may use it.
func registerAnonymiseRoutes(mux *http.ServeMux) {
	h := scrub.NewHandler(services.Scrub)
	mux.Handle("POST /api/v1/anonymise", authenticated(h.Anonymise))
}
