// Package rest holds JSON request and response helpers for HTTP handlers.
package rest

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/martinsuchenak/exemplar/internal/apperr"
	"github.com/martinsuchenak/exemplar/internal/principal"
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
	case errors.Is(err, apperr.ErrTooLarge):
		RespondError(w, http.StatusRequestEntityTooLarge, err.Error())
	default:
		RespondError(w, http.StatusInternalServerError, "internal error")
	}
}

// DecodeJSON decodes the request body into v, reading at most MaxBodyBytes.
func DecodeJSON(r *http.Request, v interface{}) error {
	defer func() { _ = r.Body.Close() }()
	return json.NewDecoder(io.LimitReader(r.Body, MaxBodyBytes)).Decode(v)
}

// Caller returns the authenticated principal, or writes 401 and returns false.
// Routes behind auth.APIKeyMiddleware always have one.
func Caller(w http.ResponseWriter, r *http.Request) (principal.Principal, bool) {
	p, ok := principal.FromContext(r.Context())
	if !ok {
		RespondError(w, http.StatusUnauthorized, "unauthorized")
	}
	return p, ok
}

// QueryInt reads an optional integer query parameter. A malformed value
// returns an apperr.ErrInvalid error naming the parameter.
func QueryInt(r *http.Request, name string) (int, error) {
	v := r.URL.Query().Get(name)
	if v == "" {
		return 0, nil
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return 0, fmt.Errorf("%w: %s must be a whole number", apperr.ErrInvalid, name)
	}
	return n, nil
}

// QueryList reads a comma-separated query parameter; repeating the parameter
// also works. Empty items are dropped.
func QueryList(r *http.Request, name string) []string {
	var out []string
	for _, v := range r.URL.Query()[name] {
		for _, item := range strings.Split(v, ",") {
			if item = strings.TrimSpace(item); item != "" {
				out = append(out, item)
			}
		}
	}
	return out
}
