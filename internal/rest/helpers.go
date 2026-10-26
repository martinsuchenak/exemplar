package rest

import (
	"encoding/json"
	"io"
	"net/http"
)

// MaxBodyBytes caps the size of request bodies accepted by DecodeJSON.
const MaxBodyBytes = 1 << 20

func RespondJSON(w http.ResponseWriter, status int, data interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if data != nil {
		_ = json.NewEncoder(w).Encode(data)
	}
}

func RespondError(w http.ResponseWriter, status int, message string) {
	RespondJSON(w, status, map[string]string{"error": message})
}

func DecodeJSON(r *http.Request, v interface{}) error {
	defer func() { _ = r.Body.Close() }()
	return json.NewDecoder(io.LimitReader(r.Body, MaxBodyBytes)).Decode(v)
}
