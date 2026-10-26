package routes

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

func TestFallbackAnswersJSON(t *testing.T) {
	mux, _, _ := testServer(t)
	for path, oauth := range map[string]bool{
		"/.well-known/oauth-protected-resource":     true,
		"/.well-known/oauth-protected-resource/mcp": true,
		"/.well-known/oauth-authorization-server":   true,
		"/.well-known/openid-configuration":         true,
		"/mcp/.well-known/openid-configuration":     true,
		"/register":                                 true,
		"/token":                                    true,
		"/nowhere":                                  false,
		"/api/v2/datasets":                          false,
	} {
		for _, method := range []string{"GET", "POST"} {
			w := call(mux, method, path, "", "{}")
			var body map[string]string
			if w.Code != http.StatusNotFound || !strings.HasPrefix(w.Header().Get("Content-Type"), "application/json") || json.Unmarshal(w.Body.Bytes(), &body) != nil {
				t.Errorf("%s %s = %d %q %s", method, path, w.Code, w.Header().Get("Content-Type"), w.Body)
				continue
			}
			if oauth && (body["error"] != "invalid_request" || !strings.Contains(body["error_description"], "Authorization: Bearer exm_")) {
				t.Errorf("%s %s = %v", method, path, body)
			}
			if !oauth && !strings.Contains(body["error"], path) {
				t.Errorf("%s %s = %v", method, path, body)
			}
		}
	}
}

func TestUnauthorizedSaysHowToAuthenticate(t *testing.T) {
	mux, _, _ := testServer(t)
	w := call(mux, "GET", "/api/v1/me", "", "")
	if w.Code != http.StatusUnauthorized || w.Header().Get("WWW-Authenticate") != `Bearer realm="exemplar"` || !strings.Contains(w.Body.String(), "Authorization: Bearer exm_") {
		t.Errorf("no key = %d %q %s", w.Code, w.Header().Get("WWW-Authenticate"), w.Body)
	}
	w = call(mux, "GET", "/api/v1/me", "exm_wrong", "")
	if w.Code != http.StatusUnauthorized || !strings.Contains(w.Header().Get("WWW-Authenticate"), `error="invalid_token"`) {
		t.Errorf("bad key = %d %q", w.Code, w.Header().Get("WWW-Authenticate"))
	}
}
