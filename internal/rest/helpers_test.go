package rest

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

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

func shortIdle(t *testing.T, d time.Duration) {
	t.Helper()
	old := transferIdle
	transferIdle = d
	t.Cleanup(func() { transferIdle = old })
}

// rawRequest sends a request's headers and the first part of a body that
// declares more, then lets the caller decide when to send the rest.
func rawRequest(t *testing.T, srv *httptest.Server, length int, first string) net.Conn {
	t.Helper()
	conn, err := net.Dial("tcp", srv.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	_, _ = fmt.Fprintf(conn, "POST / HTTP/1.1\r\nHost: x\r\nContent-Length: %d\r\n\r\n%s", length, first)
	return conn
}

// An upload that stops sending is cut off after the idle interval, long
// before LongTransfer.
func TestLongUploadStallEnds(t *testing.T) {
	shortIdle(t, 100*time.Millisecond)
	got := make(chan error, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		AllowLongUpload(w, r)
		_, err := io.ReadAll(r.Body)
		got <- err
	}))
	defer srv.Close()
	rawRequest(t, srv, 100, "only ten b")
	select {
	case err := <-got:
		if err == nil {
			t.Error("a stalled upload was read to the end")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("a stalled upload was not cut off")
	}
}

// An upload that keeps moving may take far longer than one idle interval.
func TestLongUploadThatKeepsMovingSucceeds(t *testing.T) {
	shortIdle(t, 100*time.Millisecond)
	got := make(chan string, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		AllowLongUpload(w, r)
		b, err := io.ReadAll(r.Body)
		if err != nil {
			got <- "error: " + err.Error()
			return
		}
		got <- string(b)
	}))
	defer srv.Close()
	conn := rawRequest(t, srv, 10, "")
	for range 10 { // 10 × 50ms: five idle intervals in all, never one stalled
		time.Sleep(50 * time.Millisecond)
		if _, err := conn.Write([]byte("x")); err != nil {
			t.Fatal(err)
		}
	}
	if b := <-got; b != strings.Repeat("x", 10) {
		t.Errorf("body = %q", b)
	}
}

// A download whose client stops reading is cut off after the idle interval.
func TestLongDownloadStallEnds(t *testing.T) {
	shortIdle(t, 100*time.Millisecond)
	got := make(chan error, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w = AllowLongDownload(w)
		chunk := make([]byte, 64<<10)
		var err error
		for range 4096 { // 256 MiB, far more than socket buffers hold
			if _, err = w.Write(chunk); err != nil {
				break
			}
		}
		got <- err
	}))
	defer srv.Close()
	conn, err := net.Dial("tcp", srv.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	_, _ = fmt.Fprint(conn, "GET / HTTP/1.1\r\nHost: x\r\n\r\n") // and never read
	select {
	case err := <-got:
		if err == nil {
			t.Error("a download nobody read was written to the end")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("a stalled download was not cut off")
	}
}

// The download writer keeps Flush working, directly and through
// http.ResponseController.
func TestLongDownloadKeepsFlush(t *testing.T) {
	rec := httptest.NewRecorder()
	w := AllowLongDownload(rec)
	if err := http.NewResponseController(w).Flush(); err != nil {
		t.Fatalf("flush: %v", err)
	}
	if !rec.Flushed {
		t.Error("flush did not reach the real writer")
	}
}
