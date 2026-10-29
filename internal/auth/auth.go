// Package auth holds HTTP authentication middleware. API keys are resolved
// by the account service; the middleware only reads the header.
package auth

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/martinsuchenak/exemplar/internal/apperr"
	"github.com/martinsuchenak/exemplar/internal/principal"
	"github.com/martinsuchenak/exemplar/internal/rest"
)

// Authenticator resolves an API key secret to a principal. account.Service
// implements it; the middleware itself never touches the database.
type Authenticator interface {
	Authenticate(ctx context.Context, secret string) (principal.Principal, error)
}

// APIKeyMiddleware requires a valid API key and stores the resolved principal
// in the request context (principal.FromContext). The key comes from the
// X-API-Key header or, as most MCP clients send it, from
// "Authorization: Bearer exm_…". A nil authenticator rejects every request
// rather than disabling authentication.
func APIKeyMiddleware(authn Authenticator) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			key := apiKey(r)
			if authn == nil || key == "" {
				unauthorized(w, false)
				return
			}
			p, err := authn.Authenticate(r.Context(), key)
			if errors.Is(err, apperr.ErrUnauthenticated) {
				unauthorized(w, true)
				return
			}
			if err != nil {
				rest.RespondError(w, http.StatusInternalServerError, "authentication failed")
				return
			}
			ctx := principal.WithContext(r.Context(), p)
			if rv, ok := authn.(Revalidator); ok && p.KeyID != "" {
				// Long transfers re-check the key while they stream, so a
				// key revoked (or an account disabled) after the request
				// started also stops it.
				rw := &revalidatingWriter{ResponseWriter: w, rv: rv, keyID: p.KeyID, last: time.Now(), ctx: ctx}
				r.Body = &revalidatingBody{ReadCloser: r.Body, rv: rv, keyID: p.KeyID, last: time.Now(), ctx: ctx}
				w = rw
			}
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// Revalidator reports whether a key resolved earlier is still active.
// account.Service implements it alongside Authenticator; when the
// authenticator also implements Revalidator, streaming transfers re-check
// their key, so revoking a key or disabling an account also stops requests
// that are already in flight.
type Revalidator interface {
	KeyActive(ctx context.Context, keyID string) (bool, error)
}

// revalidateInterval is how often a streaming transfer re-checks its key.
// Ordinary requests finish far faster and are never checked.
var revalidateInterval = 10 * time.Second

// errKeyRevoked ends a transfer whose key stopped being valid after the
// transfer started. The response has usually begun, so the caller sees the
// stream cut short rather than a status code.
var errKeyRevoked = errors.New("api key revoked or account disabled during transfer")

// revalidate reports whether the transfer may continue. A check that cannot
// run (database unavailable, client gone) never interrupts the transfer: the
// check is hardening, and the next interval tries again.
func revalidate(ctx context.Context, rv Revalidator, keyID string, last *time.Time) error {
	now := time.Now()
	if now.Sub(*last) < revalidateInterval {
		return nil
	}
	*last = now
	active, err := rv.KeyActive(context.WithoutCancel(ctx), keyID)
	if err != nil {
		return nil
	}
	if !active {
		return errKeyRevoked
	}
	return nil
}

// revalidatingWriter wraps the ResponseWriter so writes of response bytes
// re-check the key. Flush is forwarded, and Unwrap lets
// http.ResponseController reach the real connection, so handlers keep their
// deadline control behind the wrapper.
type revalidatingWriter struct {
	http.ResponseWriter
	rv    Revalidator
	keyID string
	last  time.Time
	ctx   context.Context
}

func (w *revalidatingWriter) Write(p []byte) (int, error) {
	if err := revalidate(w.ctx, w.rv, w.keyID, &w.last); err != nil {
		return 0, err
	}
	return w.ResponseWriter.Write(p)
}

func (w *revalidatingWriter) Flush() {
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func (w *revalidatingWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

// revalidatingBody wraps the request body so long uploads re-check the key
// as their bytes arrive.
type revalidatingBody struct {
	io.ReadCloser
	rv    Revalidator
	keyID string
	last  time.Time
	ctx   context.Context
}

func (b *revalidatingBody) Read(p []byte) (int, error) {
	if err := revalidate(b.ctx, b.rv, b.keyID, &b.last); err != nil {
		return 0, err
	}
	return b.ReadCloser.Read(p)
}

// NoKeyMessage tells a caller without a usable key how to authenticate.
const NoKeyMessage = "unauthorized: send an exemplar API key as \"Authorization: Bearer exm_…\" or \"X-API-Key: exm_…\""

// unauthorized answers 401 with a Bearer challenge (RFC 6750), so clients
// (MCP clients in particular) can tell they need a bearer token. A request
// with no key gets no error code, as the RFC asks; a rejected key gets
// invalid_token.
func unauthorized(w http.ResponseWriter, badKey bool) {
	challenge := `Bearer realm="exemplar"`
	if badKey {
		challenge += `, error="invalid_token", error_description="unknown, revoked or disabled API key"`
	}
	w.Header().Set("WWW-Authenticate", challenge)
	rest.RespondError(w, http.StatusUnauthorized, NoKeyMessage)
}

// apiKey returns the request's API key. Bearer tokens count only when they
// carry the exm_ key prefix, so SSO tokens can use the same header later.
func apiKey(r *http.Request) string {
	if key := r.Header.Get("X-API-Key"); key != "" {
		return key
	}
	if token, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer "); ok && strings.HasPrefix(token, keyPrefix) {
		return token
	}
	return ""
}

// keyPrefix matches account.KeyPrefix; auth does not import account.
const keyPrefix = "exm_"

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
