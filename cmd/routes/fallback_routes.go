package routes

import (
	"net/http"
	"strings"

	"github.com/martinsuchenak/exemplar/internal/rest"
)

func init() {
	Register(registerFallbackRoutes)
}

// registerFallbackRoutes answers every path no other route serves with JSON,
// never Go's plain-text "404 page not found", which clients that expect JSON
// fail to parse.
func registerFallbackRoutes(mux *http.ServeMux) {
	mux.HandleFunc("/", fallback)
}

// noOAuth explains, in the OAuth error format, that exemplar takes API keys.
// MCP clients that get a 401 try OAuth discovery and client registration
// next; this is what they show the user.
const noOAuth = "exemplar does not use OAuth. Configure the client with an exemplar API key as a header: Authorization: Bearer exm_… (create one with exemplar keys create, or exemplar bootstrap on the host)"

func fallback(w http.ResponseWriter, r *http.Request) {
	if oauthPath(r.URL.Path) {
		rest.RespondJSON(w, http.StatusNotFound, map[string]string{"error": "invalid_request", "error_description": noOAuth})
		return
	}
	rest.RespondError(w, http.StatusNotFound, "not found: "+r.URL.Path)
}

// oauthPath reports whether path is one an OAuth client probes: discovery
// metadata (RFC 8414, RFC 9728, OpenID) or the default registration,
// authorisation and token endpoints.
func oauthPath(path string) bool {
	switch path {
	case "/register", "/authorize", "/token":
		return true
	}
	return strings.Contains(path, "/.well-known/oauth-") || strings.Contains(path, "/.well-known/openid-configuration")
}
