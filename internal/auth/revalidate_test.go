package auth

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/martinsuchenak/exemplar/internal/apperr"
	"github.com/martinsuchenak/exemplar/internal/principal"
)

// fakeReauthn is an Authenticator whose key can stop being active mid-request.
type fakeReauthn struct {
	secret string
	active bool
	calls  int
	err    error
}

func (f *fakeReauthn) Authenticate(_ context.Context, secret string) (principal.Principal, error) {
	if secret != f.secret {
		return principal.Principal{}, apperr.ErrUnauthenticated
	}
	return principal.Principal{AccountID: "acc-1", KeyID: "key-1", Scopes: []principal.Scope{principal.ScopeRead}}, nil
}

func (f *fakeReauthn) KeyActive(context.Context, string) (bool, error) {
	f.calls++
	if f.err != nil {
		return false, f.err
	}
	return f.active, nil
}

func shortInterval(t *testing.T, d time.Duration) {
	t.Helper()
	old := revalidateInterval
	revalidateInterval = d
	t.Cleanup(func() { revalidateInterval = old })
}

// A download whose key is revoked after it started must stop streaming.
func TestRevokedKeyStopsInFlightResponse(t *testing.T) {
	shortInterval(t, time.Nanosecond)
	fake := &fakeReauthn{secret: "exm_good", active: true}
	handler := APIKeyMiddleware(fake)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		var writeErr error
		for range 100 {
			if _, writeErr = w.Write([]byte("chunk")); writeErr != nil {
				break
			}
			fake.active = false // revoked mid-transfer
		}
		if writeErr == nil {
			t.Error("write after revocation = nil error; want the transfer stopped")
		}
	}))
	req := httptest.NewRequest("GET", "/", nil)
	req.Header.Set("X-API-Key", "exm_good")
	handler.ServeHTTP(httptest.NewRecorder(), req)
}

// An upload whose key is revoked while its body arrives must stop reading.
func TestRevokedKeyStopsInFlightBody(t *testing.T) {
	shortInterval(t, time.Nanosecond)
	fake := &fakeReauthn{secret: "exm_good", active: true}
	handler := APIKeyMiddleware(fake)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		buf := make([]byte, 5)
		var readErr error
		for range 100 {
			if _, readErr = r.Body.Read(buf); readErr != nil {
				break
			}
			fake.active = false // revoked mid-upload
		}
		if readErr == nil {
			t.Error("read after revocation = nil error; want the upload stopped")
		}
	}))
	req := httptest.NewRequest("POST", "/", strings.NewReader(strings.Repeat("chunk", 100)))
	req.Header.Set("X-API-Key", "exm_good")
	handler.ServeHTTP(httptest.NewRecorder(), req)
}

// A revalidation that cannot run must not interrupt the transfer.
func TestRevalidateFailureKeepsStreaming(t *testing.T) {
	shortInterval(t, time.Nanosecond)
	fake := &fakeReauthn{secret: "exm_good", err: errors.New("db unavailable")}
	handler := APIKeyMiddleware(fake)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		for range 10 {
			if _, err := w.Write([]byte("chunk")); err != nil {
				t.Errorf("write with revalidate failing: %v", err)
			}
		}
	}))
	req := httptest.NewRequest("GET", "/", nil)
	req.Header.Set("X-API-Key", "exm_good")
	handler.ServeHTTP(httptest.NewRecorder(), req)
	if fake.calls == 0 {
		t.Error("revalidator was never consulted")
	}
}

// Revalidation is periodic: the key was just validated by Authenticate, so
// the first re-check waits a full interval and ordinary-speed transfers are
// never checked on every byte.
func TestRevalidateIsPeriodic(t *testing.T) {
	shortInterval(t, time.Hour)
	fake := &fakeReauthn{secret: "exm_good", active: true}
	handler := APIKeyMiddleware(fake)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		for range 10 {
			fake.active = false
			if _, err := w.Write([]byte("chunk")); err != nil {
				t.Errorf("write inside the interval: %v", err)
			}
		}
	}))
	req := httptest.NewRequest("GET", "/", nil)
	req.Header.Set("X-API-Key", "exm_good")
	handler.ServeHTTP(httptest.NewRecorder(), req)
	if fake.calls != 0 {
		t.Errorf("KeyActive calls = %d; want none before the interval has passed", fake.calls)
	}
}

// The wrapper keeps what streaming handlers rely on: Flush reaches the real
// writer, directly and through http.ResponseController, and a request
// without a key id (nothing to re-check) is not wrapped at all.
func TestRevalidatingWriterKeepsFlush(t *testing.T) {
	fake := &fakeReauthn{secret: "exm_good", active: true}
	var wrapped bool
	handler := APIKeyMiddleware(fake)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, wrapped = w.(*revalidatingWriter)
		if err := http.NewResponseController(w).Flush(); err != nil {
			t.Errorf("flush through the controller: %v", err)
		}
	}))
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/", nil)
	req.Header.Set("X-API-Key", "exm_good")
	handler.ServeHTTP(rec, req)
	if !wrapped {
		t.Error("an authenticated request's writer was not wrapped")
	}
	if !rec.Flushed {
		t.Error("flush did not reach the real writer")
	}
}
