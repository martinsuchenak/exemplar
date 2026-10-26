package routes

import (
	"bytes"
	"context"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/martinsuchenak/exemplar/internal/account"
	"github.com/martinsuchenak/exemplar/internal/anonymise/anonymisetest"
	"github.com/martinsuchenak/exemplar/internal/app"
	"github.com/martinsuchenak/exemplar/internal/dataset"
	"github.com/martinsuchenak/exemplar/internal/ingest"
	"github.com/martinsuchenak/exemplar/internal/testutil"
)

// upload builds a multipart body: fields first, then each file.
func upload(t *testing.T, fields map[string]string, files map[string][]byte) (*bytes.Buffer, string) {
	t.Helper()
	var b bytes.Buffer
	mw := multipart.NewWriter(&b)
	for k, v := range fields {
		_ = mw.WriteField(k, v)
	}
	for name, data := range files {
		fw, err := mw.CreateFormFile("file", name)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = fw.Write(data)
	}
	_ = mw.Close()
	return &b, mw.FormDataContentType()
}

func TestIngestRoute(t *testing.T) {
	a := testutil.NewAppWith(t, func(c *app.Config) {
		c.AnonKey = anonymisetest.Key
		c.AnonDetector = anonymisetest.Detector{Names: []string{"Priya"}}
		c.MaxIngestBytes = 4096
	})
	_, issued, err := a.Accounts.Bootstrap(context.Background(), account.CreateAccountInput{Name: "root"})
	if err != nil {
		t.Fatal(err)
	}
	admin := testutil.Authenticate(t, a, issued.Secret)
	q, err := a.Datasets.Create(context.Background(), admin, dataset.CreateInput{Name: "q", Scope: dataset.ScopeQuarantine})
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	RegisterRoutes(mux, a)
	path := "/api/v1/datasets/" + q.ID + "/ingest"
	mail := anonymisetest.Email("Priya Shah", "Tom Reid", "acme.com.au", "Hello", "Hi Tom, Priya")

	post := func(body *bytes.Buffer, ctype string) *httptest.ResponseRecorder {
		req := httptest.NewRequest("POST", path, body)
		req.Header.Set("X-API-Key", issued.Secret)
		req.Header.Set("Content-Type", ctype)
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req)
		return w
	}

	body, ctype := upload(t, map[string]string{"dry_run": "true", "profile": "default", "batch": "dry"}, map[string][]byte{"a.eml": mail})
	w := post(body, ctype)
	var res ingest.Result
	if w.Code != http.StatusOK || w.Header().Get("Cache-Control") != "no-store" || jsonDecode(w.Body, &res) != nil ||
		!res.DryRun || res.Batch != "dry" || len(res.Files) != 1 || len(res.Files[0].Report) == 0 {
		t.Fatalf("dry run = %d %s %+v", w.Code, w.Header().Get("Cache-Control"), res)
	}

	body, ctype = upload(t, map[string]string{"batch": "real", "ignored": "x"}, map[string][]byte{"a.eml": mail, "b.msg": []byte("x")})
	w = post(body, ctype)
	res = ingest.Result{}
	if w.Code != http.StatusCreated || jsonDecode(w.Body, &res) != nil || len(res.Files) != 2 {
		t.Fatalf("ingest = %d %s", w.Code, w.Body)
	}
	for _, f := range res.Files {
		if f.Name == "a.eml" && (f.FixtureName != "real-001.eml" || f.Report != nil) {
			t.Errorf("stored file = %+v", f)
		}
	}

	for _, tt := range []struct {
		name  string
		body  func() (*bytes.Buffer, string)
		want  int
		match string
	}{
		{"not multipart", func() (*bytes.Buffer, string) { return bytes.NewBufferString("{}"), "application/json" }, http.StatusBadRequest, "multipart"},
		{"bad dry_run", func() (*bytes.Buffer, string) {
			return upload(t, map[string]string{"dry_run": "maybe"}, map[string][]byte{"a.eml": mail})
		}, http.StatusBadRequest, "dry_run"},
		{"truncated", func() (*bytes.Buffer, string) {
			b, c := upload(t, nil, map[string][]byte{"a.eml": mail})
			return bytes.NewBuffer(b.Bytes()[:b.Len()/2]), c
		}, http.StatusBadRequest, "reading upload"},
		{"over the request limit", func() (*bytes.Buffer, string) {
			return upload(t, nil, map[string][]byte{"a.eml": bytes.Repeat([]byte("x"), 4096+2<<20)})
		}, http.StatusRequestEntityTooLarge, "too large"},
		{"over the service limit", func() (*bytes.Buffer, string) {
			return upload(t, nil, map[string][]byte{"a.eml": bytes.Repeat([]byte("x"), 8192)})
		}, http.StatusRequestEntityTooLarge, ""},
		{"no files", func() (*bytes.Buffer, string) { return upload(t, map[string]string{"batch": "x"}, nil) }, http.StatusBadRequest, "files"},
	} {
		b, c := tt.body()
		if w := post(b, c); w.Code != tt.want || !strings.Contains(w.Body.String(), tt.match) {
			t.Errorf("%s = %d %s", tt.name, w.Code, w.Body)
		}
	}
}
