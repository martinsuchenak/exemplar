// Package rest holds JSON request and response helpers for HTTP handlers.
package rest

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"github.com/martinsuchenak/exemplar/internal/apperr"
)

// MaxBodyBytes caps the size of request bodies accepted by DecodeJSON.
const MaxBodyBytes = 1 << 20

// RespondJSON writes data as JSON with status.
func RespondJSON(w http.ResponseWriter, status int, data interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if data != nil {
		_ = json.NewEncoder(w).Encode(data)
	}
}

// RespondError writes {"error": message} with status.
func RespondError(w http.ResponseWriter, status int, message string) {
	RespondJSON(w, status, map[string]string{"error": message})
}

// RespondServiceError maps a service error to its HTTP status. Unexpected
// errors are reported as a generic 500 so internal details do not leak.
func RespondServiceError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, apperr.ErrNotFound):
		RespondError(w, http.StatusNotFound, err.Error())
	case errors.Is(err, apperr.ErrUnauthenticated):
		RespondError(w, http.StatusUnauthorized, "unauthorized")
	case errors.Is(err, apperr.ErrForbidden):
		RespondError(w, http.StatusForbidden, err.Error())
	case errors.Is(err, apperr.ErrConflict):
		RespondError(w, http.StatusConflict, err.Error())
	case errors.Is(err, apperr.ErrInvalid):
		RespondError(w, http.StatusBadRequest, err.Error())
	default:
		RespondError(w, http.StatusInternalServerError, "internal error")
	}
}

// DecodeJSON decodes the request body into v, reading at most MaxBodyBytes.
func DecodeJSON(r *http.Request, v interface{}) error {
	defer func() { _ = r.Body.Close() }()
	return json.NewDecoder(io.LimitReader(r.Body, MaxBodyBytes)).Decode(v)
}
