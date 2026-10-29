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
	"time"

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
	case errors.Is(err, apperr.ErrBusy):
		RespondError(w, http.StatusTooManyRequests, err.Error())
	default:
		RespondError(w, http.StatusInternalServerError, "internal error")
	}
}

// DecodeJSON decodes the request body into v, reading at most MaxBodyBytes.
func DecodeJSON(r *http.Request, v interface{}) error {
	defer func() { _ = r.Body.Close() }()
	body := &io.LimitedReader{R: r.Body, N: MaxBodyBytes + 1}
	dec := json.NewDecoder(body)
	err := dec.Decode(v)
	if err == nil {
		// One JSON value only: trailing data is a malformed request.
		if _, terr := dec.Token(); !errors.Is(terr, io.EOF) {
			err = errors.New("unexpected data after the JSON value")
		}
	}
	if body.N <= 0 {
		return fmt.Errorf("%w: the request body is over %d bytes", apperr.ErrTooLarge, MaxBodyBytes)
	}
	if err != nil {
		return fmt.Errorf("%w: %w", apperr.ErrInvalid, err)
	}
	return nil
}

// RespondBodyError answers a DecodeJSON failure: 413 for a body over
// MaxBodyBytes, otherwise 400 with msg.
func RespondBodyError(w http.ResponseWriter, err error, msg string) {
	if errors.Is(err, apperr.ErrTooLarge) {
		RespondError(w, http.StatusRequestEntityTooLarge, fmt.Sprintf("the request body is over %d bytes", MaxBodyBytes))
		return
	}
	RespondError(w, http.StatusBadRequest, msg)
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

// LongTransfer is how long one large upload or download may take. The
// server's own read and write timeouts stay short so slow clients cannot hold
// ordinary requests open; handlers that move fixture contents, ingests and
// archives extend their connection's deadline to this instead.
const LongTransfer = time.Hour

// transferIdle is how long a long transfer may go without moving a byte. A
// client that stops sending or reading is cut off then rather than at the
// end of LongTransfer, so an abandoned upload does not hold its memory, or
// an ingest or anonymise run slot, for an hour.
var transferIdle = 2 * time.Minute

// AllowLongUpload lets the request body take up to LongTransfer to arrive,
// as long as it never stalls for longer than transferIdle. It replaces
// r.Body, so call it before wrapping the body in size limits, which still
// apply separately.
func AllowLongUpload(w http.ResponseWriter, r *http.Request) {
	rc := http.NewResponseController(w)
	end := time.Now().Add(LongTransfer)
	_ = rc.SetReadDeadline(idleDeadline(end))
	r.Body = &idleBody{ReadCloser: r.Body, rc: rc, end: end}
}

// AllowLongDownload lets the response take up to LongTransfer to send, as
// long as the client never stops reading for longer than transferIdle.
// Write the response to the writer it returns.
func AllowLongDownload(w http.ResponseWriter) http.ResponseWriter {
	rc := http.NewResponseController(w)
	end := time.Now().Add(LongTransfer)
	_ = rc.SetWriteDeadline(idleDeadline(end))
	return &idleWriter{ResponseWriter: w, rc: rc, end: end}
}

// idleDeadline is when the next read or write must have finished: one idle
// interval from now, but never past the transfer's end.
func idleDeadline(end time.Time) time.Time {
	if d := time.Now().Add(transferIdle); d.Before(end) {
		return d
	}
	return end
}

// idleBody moves the read deadline on before every read, so each read has
// transferIdle to make progress.
type idleBody struct {
	io.ReadCloser
	rc  *http.ResponseController
	end time.Time
}

func (b *idleBody) Read(p []byte) (int, error) {
	_ = b.rc.SetReadDeadline(idleDeadline(b.end))
	return b.ReadCloser.Read(p)
}

// idleWriter moves the write deadline on before every write. Flush is
// forwarded and Unwrap lets http.ResponseController reach the connection.
type idleWriter struct {
	http.ResponseWriter
	rc  *http.ResponseController
	end time.Time
}

func (w *idleWriter) Write(p []byte) (int, error) {
	_ = w.rc.SetWriteDeadline(idleDeadline(w.end))
	return w.ResponseWriter.Write(p)
}

func (w *idleWriter) Flush() {
	_ = w.rc.Flush()
}

func (w *idleWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }
