package rest

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/martinsuchenak/exemplar/internal/apperr"
)

func TestRespondJSON(t *testing.T) {
	w := httptest.NewRecorder()
	RespondJSON(w, http.StatusOK, map[string]string{"key": "value"})

	if w.Code != http.StatusOK {
		t.Errorf("expected status %d, got %d", http.StatusOK, w.Code)
	}
	if ct := w.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("expected Content-Type application/json, got %q", ct)
	}
}

func TestRespondError(t *testing.T) {
	w := httptest.NewRecorder()
	RespondError(w, http.StatusBadRequest, "bad input")

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected status %d, got %d", http.StatusBadRequest, w.Code)
	}
}

func TestRespondServiceError(t *testing.T) {
	tests := []struct {
		err      error
		status   int
		leaksMsg bool
	}{
		{fmt.Errorf("dataset x: %w", apperr.ErrNotFound), http.StatusNotFound, true},
		{apperr.ErrUnauthenticated, http.StatusUnauthorized, false},
		{fmt.Errorf("%w: needs admin", apperr.ErrForbidden), http.StatusForbidden, true},
		{fmt.Errorf("%w: frozen", apperr.ErrConflict), http.StatusConflict, true},
		{fmt.Errorf("%w: bad name", apperr.ErrInvalid), http.StatusBadRequest, true},
		{fmt.Errorf("%w: too big", apperr.ErrTooLarge), http.StatusRequestEntityTooLarge, true},
		{fmt.Errorf("%w: a run is in progress", apperr.ErrBusy), http.StatusTooManyRequests, true},
		{errors.New("disk full at /secret/path"), http.StatusInternalServerError, false},
	}
	for _, tt := range tests {
		w := httptest.NewRecorder()
		RespondServiceError(w, tt.err)
		if w.Code != tt.status {
			t.Errorf("%v: status %d, want %d", tt.err, w.Code, tt.status)
		}
		var body map[string]string
		if err := json.NewDecoder(w.Body).Decode(&body); err != nil {
			t.Fatalf("decoding body: %v", err)
		}
		if got := body["error"] == tt.err.Error(); got != tt.leaksMsg {
			t.Errorf("%v: body %q; service message shown = %v, want %v", tt.err, body["error"], got, tt.leaksMsg)
		}
	}
}

func TestDecodeJSON(t *testing.T) {
	var v struct {
		Name string `json:"name"`
	}
	r := httptest.NewRequest("POST", "/", strings.NewReader(`{"name":"x"}`))
	if err := DecodeJSON(r, &v); err != nil || v.Name != "x" {
		t.Errorf("DecodeJSON = %+v, %v", v, err)
	}
	r = httptest.NewRequest("POST", "/", strings.NewReader(`{`))
	if err := DecodeJSON(r, &v); err == nil {
		t.Error("malformed JSON decoded without error")
	}
	big := `{"name":"` + strings.Repeat("a", MaxBodyBytes) + `"}`
	r = httptest.NewRequest("POST", "/", strings.NewReader(big))
	if err := DecodeJSON(r, &v); !errors.Is(err, apperr.ErrTooLarge) {
		t.Errorf("body over MaxBodyBytes err = %v", err)
	}
	r = httptest.NewRequest("POST", "/", strings.NewReader(`{"name":"x"} {"name":"y"}`))
	if err := DecodeJSON(r, &v); !errors.Is(err, apperr.ErrInvalid) {
		t.Errorf("trailing data err = %v", err)
	}
	r = httptest.NewRequest("POST", "/", strings.NewReader(""))
	if err := DecodeJSON(r, &v); !errors.Is(err, io.EOF) || !errors.Is(err, apperr.ErrInvalid) {
		t.Errorf("empty body err = %v (optional bodies test for io.EOF)", err)
	}
	for err, want := range map[error]int{apperr.ErrTooLarge: http.StatusRequestEntityTooLarge, apperr.ErrInvalid: http.StatusBadRequest} {
		w := httptest.NewRecorder()
		RespondBodyError(w, err, "bad body")
		if w.Code != want {
			t.Errorf("RespondBodyError(%v) = %d", err, w.Code)
		}
	}
}
