// Package auth holds HTTP authentication middleware. API keys are resolved
// by the account service; the middleware only reads the header.
package auth

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/martinsuchenak/exemplar/internal/apperr"
	"github.com/martinsuchenak/exemplar/internal/principal"
	"github.com/martinsuchenak/exemplar/internal/rest"
)

// Authenticator resolves an API key secret to a principal. account.Service
// implements it; the middleware itself never touches the database.
type Authenticator interface {
	Authenticate(ctx context.Context, secret string) (principal.Principal, error)
}

// APIKeyMiddleware requires a valid X-API-Key header and stores the resolved
// principal in the request context (principal.FromContext). A nil
// authenticator rejects every request rather than disabling authentication.
func APIKeyMiddleware(authn Authenticator) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			key := r.Header.Get("X-API-Key")
			if authn == nil || key == "" {
				rest.RespondError(w, http.StatusUnauthorized, "unauthorized")
				return
			}
			p, err := authn.Authenticate(r.Context(), key)
			if errors.Is(err, apperr.ErrUnauthenticated) {
				rest.RespondError(w, http.StatusUnauthorized, "unauthorized")
				return
			}
			if err != nil {
				rest.RespondError(w, http.StatusInternalServerError, "authentication failed")
				return
			}
			next.ServeHTTP(w, r.WithContext(principal.WithContext(r.Context(), p)))
		})
	}
}

// TokenValidator reports whether a bearer token is valid.
type TokenValidator func(r *http.Request, token string) bool

// BearerTokenMiddleware requires an Authorization: Bearer token accepted by
// validate. A nil validate rejects every request.
// TODO: Supply a validator when SSO is added (JWT, OAuth 2.0 + PKCE, OIDC).
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
