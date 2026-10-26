package auth

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/martinsuchenak/exemplar/internal/apperr"
	"github.com/martinsuchenak/exemplar/internal/principal"
)

type fakeAuthn struct {
	secret string
	err    error
}

func (f fakeAuthn) Authenticate(_ context.Context, secret string) (principal.Principal, error) {
	if f.err != nil {
		return principal.Principal{}, f.err
	}
	if secret != f.secret {
		return principal.Principal{}, apperr.ErrUnauthenticated
	}
	return principal.Principal{AccountID: "acc-1", Scopes: []principal.Scope{principal.ScopeRead}}, nil
}

func TestAPIKeyMiddleware(t *testing.T) {
	tests := []struct {
		name  string
		authn Authenticator
		key   string
		want  int
	}{
		{"valid key", fakeAuthn{secret: "exm_good"}, "exm_good", http.StatusOK},
		{"missing key", fakeAuthn{secret: "exm_good"}, "", http.StatusUnauthorized},
		{"wrong key", fakeAuthn{secret: "exm_good"}, "exm_bad", http.StatusUnauthorized},
		{"nil authenticator", nil, "exm_good", http.StatusUnauthorized},
		{"lookup failure", fakeAuthn{err: errors.New("db down")}, "exm_good", http.StatusInternalServerError},
		{"bearer key", fakeAuthn{secret: "exm_good"}, "Bearer exm_good", http.StatusOK},
		{"bearer non-key token", fakeAuthn{secret: "exm_good"}, "Bearer eyJhbGciOi", http.StatusUnauthorized},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var got principal.Principal
			handler := APIKeyMiddleware(tt.authn)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				got, _ = principal.FromContext(r.Context())
				w.WriteHeader(http.StatusOK)
			}))
			req := httptest.NewRequest("GET", "/", nil)
			if bearer, ok := strings.CutPrefix(tt.key, "Bearer "); ok {
				req.Header.Set("Authorization", "Bearer "+bearer)
			} else if tt.key != "" {
				req.Header.Set("X-API-Key", tt.key)
			}
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, req)

			if w.Code != tt.want {
				t.Errorf("expected status %d, got %d", tt.want, w.Code)
			}
			if tt.want == http.StatusOK && got.AccountID != "acc-1" {
				t.Errorf("principal not stored in context, got %+v", got)
			}
		})
	}
}

func TestBearerTokenMiddleware(t *testing.T) {
	validate := func(r *http.Request, token string) bool { return token == "good-token" }
	tests := []struct {
		name     string
		validate TokenValidator
		header   string
		want     int
	}{
		{"valid token", validate, "Bearer good-token", http.StatusOK},
		{"invalid token", validate, "Bearer bad-token", http.StatusUnauthorized},
		{"missing header", validate, "", http.StatusUnauthorized},
		{"empty token", validate, "Bearer ", http.StatusUnauthorized},
		{"nil validator", nil, "Bearer good-token", http.StatusUnauthorized},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			handler := BearerTokenMiddleware(tt.validate)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusOK)
			}))

			req := httptest.NewRequest("GET", "/", nil)
			if tt.header != "" {
				req.Header.Set("Authorization", tt.header)
			}
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, req)

			if w.Code != tt.want {
				t.Errorf("expected status %d, got %d", tt.want, w.Code)
			}
		})
	}
}
