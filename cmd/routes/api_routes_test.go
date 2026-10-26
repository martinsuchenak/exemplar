package routes

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/martinsuchenak/exemplar/internal/account"
	"github.com/martinsuchenak/exemplar/internal/testutil"
)

func TestHealthHandler(t *testing.T) {
	req := httptest.NewRequest("GET", "/health", nil)
	w := httptest.NewRecorder()
	healthHandler(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected status %d, got %d", http.StatusOK, w.Code)
	}
}

func TestMetricsNeedAnAdminKey(t *testing.T) {
	a := testutil.NewApp(t)
	_, issued, err := a.Accounts.Bootstrap(context.Background(), account.CreateAccountInput{Name: "root"})
	if err != nil {
		t.Fatal(err)
	}
	admin := testutil.Authenticate(t, a, issued.Secret)
	member, err := a.Accounts.CreateAccount(context.Background(), admin, account.CreateAccountInput{Name: "m", Kind: account.KindDeveloper})
	if err != nil {
		t.Fatal(err)
	}
	mkey, err := a.Accounts.CreateKey(context.Background(), admin, account.CreateKeyInput{AccountID: member.ID, Name: "k"})
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	RegisterRoutes(mux, a)
	for key, want := range map[string]int{"": http.StatusUnauthorized, mkey.Secret: http.StatusForbidden, issued.Secret: http.StatusOK} {
		req := httptest.NewRequest("GET", "/metrics", nil)
		if key != "" {
			req.Header.Set("X-API-Key", key)
		}
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req)
		if w.Code != want {
			t.Errorf("metrics with key %q... = %d, want %d", key[:min(len(key), 8)], w.Code, want)
		}
	}
	// Health stays open, for load balancers and container checks.
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest("GET", "/health", nil))
	if w.Code != http.StatusOK {
		t.Errorf("health = %d", w.Code)
	}
}

func TestMeRequiresKey(t *testing.T) {
	a := testutil.NewApp(t)
	_, issued, err := a.Accounts.Bootstrap(context.Background(), account.CreateAccountInput{Name: "root"})
	if err != nil {
		t.Fatalf("bootstrap: %v", err)
	}
	mux := http.NewServeMux()
	RegisterRoutes(mux, a)

	req := httptest.NewRequest("GET", "/api/v1/me", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Errorf("without key: status %d, want 401", w.Code)
	}

	req = httptest.NewRequest("GET", "/api/v1/me", nil)
	req.Header.Set("X-API-Key", issued.Secret)
	w = httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("with key: status %d, want 200: %s", w.Code, w.Body)
	}
	var body struct {
		Account struct {
			Name string `json:"name"`
		} `json:"account"`
	}
	if err := json.NewDecoder(w.Body).Decode(&body); err != nil || body.Account.Name != "root" {
		t.Errorf("body = %+v, %v", body, err)
	}
}
