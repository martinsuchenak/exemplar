package auth

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestAPIKeyMiddleware(t *testing.T) {
	handler := APIKeyMiddleware("test-key")(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest("GET", "/", nil)
	req.Header.Set("X-API-Key", "test-key")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected status %d, got %d", http.StatusOK, w.Code)
	}
}

func TestAPIKeyMiddlewareUnauthorized(t *testing.T) {
	handler := APIKeyMiddleware("test-key")(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest("GET", "/", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("expected status %d, got %d", http.StatusUnauthorized, w.Code)
	}
}

func TestAPIKeyMiddlewareWrongKey(t *testing.T) {
	handler := APIKeyMiddleware("test-key")(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest("GET", "/", nil)
	req.Header.Set("X-API-Key", "wrong-key")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("expected status %d, got %d", http.StatusUnauthorized, w.Code)
	}
}

func TestAPIKeyMiddlewareEmptyConfiguredKey(t *testing.T) {
	handler := APIKeyMiddleware("")(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest("GET", "/", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("expected status %d, got %d", http.StatusUnauthorized, w.Code)
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
