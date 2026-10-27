package routes

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/martinsuchenak/exemplar/internal/account"
	"github.com/martinsuchenak/exemplar/internal/dataset"
	"github.com/martinsuchenak/exemplar/internal/fixture"
	"github.com/martinsuchenak/exemplar/internal/testutil"
)

// testServer registers the real routes on a test app and returns the mux and
// the bootstrapped admin's key.
func testServer(t *testing.T) (*http.ServeMux, string, string) {
	t.Helper()
	a := testutil.NewApp(t)
	_, issued, err := a.Accounts.Bootstrap(context.Background(), account.CreateAccountInput{Name: "root"})
	if err != nil {
		t.Fatalf("bootstrap: %v", err)
	}
	mux := http.NewServeMux()
	RegisterRoutes(mux, a)

	p := testutil.Authenticate(t, a, issued.Secret)
	d, err := a.Datasets.Create(context.Background(), p, dataset.CreateInput{Name: "mine"})
	if err != nil {
		t.Fatalf("dataset: %v", err)
	}
	return mux, issued.Secret, d.ID
}

func call(mux *http.ServeMux, method, path, key, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	if key != "" {
		req.Header.Set("X-API-Key", key)
	}
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	return w
}

func TestEveryAPIRouteNeedsAKey(t *testing.T) {
	mux, _, _ := testServer(t)
	for _, r := range []struct{ method, path string }{
		{"GET", "/api/v1/me"},
		{"GET", "/api/v1/accounts"},
		{"POST", "/api/v1/accounts"},
		{"GET", "/api/v1/accounts/x"},
		{"POST", "/api/v1/accounts/x/disable"},
		{"GET", "/api/v1/keys"},
		{"POST", "/api/v1/keys"},
		{"DELETE", "/api/v1/keys/x"},
		{"GET", "/api/v1/datasets"},
		{"POST", "/api/v1/datasets"},
		{"GET", "/api/v1/datasets/x"},
		{"DELETE", "/api/v1/datasets/x"},
		{"POST", "/api/v1/datasets/x/freeze"},
		{"POST", "/api/v1/datasets/x/fork"},
		{"POST", "/api/v1/datasets/x/promote"},
		{"GET", "/api/v1/datasets/x/fixtures"},
		{"POST", "/api/v1/datasets/x/fixtures"},
		{"GET", "/api/v1/fixtures/x"},
		{"GET", "/api/v1/fixtures/x/content"},
		{"DELETE", "/api/v1/fixtures/x"},
		{"POST", "/api/v1/fixtures/x/review"},
		{"POST", "/api/v1/purge"},
		{"GET", "/api/v1/audit"},
		{"GET", "/api/v1/persons"},
		{"POST", "/api/v1/persons"},
		{"GET", "/api/v1/persons/x"},
		{"POST", "/api/v1/datasets/x/generate"},
		{"GET", "/api/v1/record-schemas"},
		{"POST", "/api/v1/datasets/x/ingest"},
		{"POST", "/api/v1/export"},
		{"POST", "/api/v1/import"},
	} {
		if w := call(mux, r.method, r.path, "", ""); w.Code != http.StatusUnauthorized {
			t.Errorf("%s %s without key: %d, want 401", r.method, r.path, w.Code)
		}
	}
}

func TestMalformedRequests(t *testing.T) {
	mux, key, dsID := testServer(t)
	for _, r := range []struct{ method, path, body string }{
		{"POST", "/api/v1/accounts", "{"},
		{"POST", "/api/v1/keys", "["},
		{"POST", "/api/v1/datasets", "nope"},
		{"POST", "/api/v1/datasets/" + dsID + "/fork", "{"},
		{"POST", "/api/v1/datasets/" + dsID + "/promote", "{"},
		{"POST", "/api/v1/fixtures/x/review", "{}"},
		{"POST", "/api/v1/fixtures/x/review", "{"},
		{"POST", "/api/v1/purge", "{"},
		{"GET", "/api/v1/datasets/" + dsID + "/fixtures?limit=ten", ""},
		{"GET", "/api/v1/datasets/" + dsID + "/fixtures?offset=x", ""},
		{"GET", "/api/v1/audit?after_seq=x", ""},
		{"GET", "/api/v1/audit?limit=x", ""},
		{"POST", "/api/v1/datasets/" + dsID + "/fixtures", "no name given"},
		{"POST", "/api/v1/persons", "{"},
		{"POST", "/api/v1/persons", `{"count":0}`},
		{"GET", "/api/v1/persons?limit=x", ""},
		{"GET", "/api/v1/persons?offset=x", ""},
		{"POST", "/api/v1/datasets/" + dsID + "/generate", "{"},
		{"POST", "/api/v1/datasets/" + dsID + "/generate", `{"kind":"files"}`},
	} {
		if w := call(mux, r.method, r.path, key, r.body); w.Code != http.StatusBadRequest {
			t.Errorf("%s %s %q: %d, want 400 (%s)", r.method, r.path, r.body, w.Code, w.Body)
		}
	}
}

func TestStatusCodes(t *testing.T) {
	mux, key, dsID := testServer(t)
	for _, r := range []struct {
		method, path, body string
		want               int
	}{
		{"POST", "/api/v1/datasets/" + dsID + "/generate", `{"kind":"records","count":2,"seed":1}`, http.StatusCreated},
		// An empty body forks under the source's name, which the owner already uses.
		{"POST", "/api/v1/datasets/" + dsID + "/fork", "", http.StatusConflict},
		{"POST", "/api/v1/datasets/" + dsID + "/fork", `{"name":"copy"}`, http.StatusCreated},
		{"POST", "/api/v1/datasets/" + dsID + "/promote", "", http.StatusCreated},
		{"POST", "/api/v1/datasets/" + dsID + "/freeze", "", http.StatusOK},
		{"GET", "/api/v1/accounts/missing", "", http.StatusNotFound},
		{"POST", "/api/v1/accounts/missing/disable", "", http.StatusNotFound},
		{"DELETE", "/api/v1/keys/missing", "", http.StatusNotFound},
		{"GET", "/api/v1/datasets/missing/fixtures", "", http.StatusNotFound},
		{"GET", "/api/v1/fixtures/missing", "", http.StatusNotFound},
		{"GET", "/api/v1/fixtures/missing/content", "", http.StatusNotFound},
		{"DELETE", "/api/v1/fixtures/missing", "", http.StatusNotFound},
		{"POST", "/api/v1/fixtures/missing/review", `{"approve":true}`, http.StatusNotFound},
		{"GET", "/api/v1/persons/missing", "", http.StatusNotFound},
		{"POST", "/api/v1/datasets/missing/generate", `{"kind":"files","count":1}`, http.StatusNotFound},
		{"POST", "/api/v1/persons", `{"count":2}`, http.StatusCreated},
		{"GET", "/api/v1/record-schemas", "", http.StatusOK},
		{"GET", "/api/v1/datasets/" + dsID + "/fixtures?record_schema=contact&person_id=x", "", http.StatusOK},
		{"DELETE", "/api/v1/datasets/missing", "", http.StatusNotFound},
		{"POST", "/api/v1/datasets/missing/freeze", "", http.StatusNotFound},
		{"POST", "/api/v1/datasets/missing/fork", "", http.StatusNotFound},
		{"POST", "/api/v1/datasets/missing/promote", "", http.StatusNotFound},
		{"GET", "/api/v1/datasets/missing", "", http.StatusNotFound},
		{"POST", "/api/v1/accounts", `{"name":"bad name"}`, http.StatusBadRequest},
		{"POST", "/api/v1/keys", `{"name":""}`, http.StatusBadRequest},
		{"GET", "/api/v1/datasets?scope=public", "", http.StatusBadRequest},
		{"POST", "/api/v1/purge", `{"ingest_batch":""}`, http.StatusBadRequest},
		{"POST", "/api/v1/datasets/" + dsID + "/fixtures?name=late.txt", "x", http.StatusConflict},
	} {
		if w := call(mux, r.method, r.path, key, r.body); w.Code != r.want {
			t.Errorf("%s %s: %d, want %d (%s)", r.method, r.path, w.Code, r.want, w.Body)
		}
	}
}

func TestContentDownloadHeaders(t *testing.T) {
	mux, key, dsID := testServer(t)
	up := call(mux, "POST", "/api/v1/datasets/"+dsID+"/fixtures?name=page.html&tags=a,b&tags=c", key, "<script>alert(1)</script>")
	if up.Code != http.StatusCreated {
		t.Fatalf("upload: %d %s", up.Code, up.Body)
	}
	var f fixture.Fixture
	if err := jsonDecode(up.Body, &f); err != nil {
		t.Fatal(err)
	}
	if len(f.Tags) != 3 {
		t.Errorf("tags = %v, want a, b, c", f.Tags)
	}

	w := call(mux, "GET", "/api/v1/fixtures/"+f.ID+"/content", key, "")
	if w.Code != http.StatusOK || w.Body.String() != "<script>alert(1)</script>" {
		t.Fatalf("download: %d %q", w.Code, w.Body)
	}
	h := w.Header()
	if !strings.HasPrefix(h.Get("Content-Type"), "text/html") || h.Get("Content-Length") != "25" {
		t.Errorf("type/length = %q/%q", h.Get("Content-Type"), h.Get("Content-Length"))
	}
	if h.Get("Content-Disposition") != `attachment; filename=page.html` {
		t.Errorf("disposition = %q", h.Get("Content-Disposition"))
	}
	if h.Get("X-Content-Type-Options") != "nosniff" || h.Get("Content-Security-Policy") != "sandbox" {
		t.Errorf("missing safety headers: %v", h)
	}

	keyResp := call(mux, "POST", "/api/v1/keys", key, `{"name":"x"}`)
	if keyResp.Code != http.StatusCreated || keyResp.Header().Get("Cache-Control") != "no-store" {
		t.Errorf("key creation: %d, Cache-Control %q", keyResp.Code, keyResp.Header().Get("Cache-Control"))
	}
}

func jsonDecode(r io.Reader, v interface{}) error {
	return json.NewDecoder(r).Decode(v)
}
