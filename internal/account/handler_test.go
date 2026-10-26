package account_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/martinsuchenak/exemplar/internal/account"
	"github.com/martinsuchenak/exemplar/internal/principal"
	"github.com/martinsuchenak/exemplar/internal/testutil"
)

func TestMeHandler(t *testing.T) {
	a := testutil.NewApp(t)
	admin := testutil.Admin(t, a)
	h := account.NewHandler(a.Accounts)

	tests := []struct {
		name string
		ctx  func(*http.Request) *http.Request
		want int
	}{
		{"no principal", func(r *http.Request) *http.Request { return r }, http.StatusUnauthorized},
		{"valid principal", func(r *http.Request) *http.Request {
			return r.WithContext(principal.WithContext(r.Context(), admin))
		}, http.StatusOK},
		{"account gone", func(r *http.Request) *http.Request {
			ghost := admin
			ghost.AccountID = "missing"
			return r.WithContext(principal.WithContext(r.Context(), ghost))
		}, http.StatusNotFound},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			h.Me(w, tt.ctx(httptest.NewRequest("GET", "/api/v1/me", nil)))
			if w.Code != tt.want {
				t.Errorf("status %d, want %d", w.Code, tt.want)
			}
		})
	}
}
