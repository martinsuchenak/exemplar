package auth

import (
	"crypto/subtle"
	"net/http"
	"strings"
)

// APIKeyMiddleware requires the X-API-Key header to match apiKey. An empty
// apiKey rejects every request rather than disabling authentication.
func APIKeyMiddleware(apiKey string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			key := r.Header.Get("X-API-Key")
			if apiKey == "" || subtle.ConstantTimeCompare([]byte(key), []byte(apiKey)) != 1 {
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// TokenValidator reports whether a bearer token is valid.
type TokenValidator func(r *http.Request, token string) bool

// BearerTokenMiddleware requires an Authorization: Bearer token accepted by
// validate. A nil validate rejects every request.
// TODO: Supply a validator for your auth scheme (JWT, OAuth 2.0 + PKCE, OIDC).
func BearerTokenMiddleware(validate TokenValidator) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			token, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
			if !ok || token == "" || validate == nil || !validate(r, token) {
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}
