package routes

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/martinsuchenak/exemplar/internal/account"
	"github.com/martinsuchenak/exemplar/internal/anonymise"
	"github.com/martinsuchenak/exemplar/internal/anonymise/anonymisetest"
	"github.com/martinsuchenak/exemplar/internal/app"
	"github.com/martinsuchenak/exemplar/internal/principal"
	"github.com/martinsuchenak/exemplar/internal/scrub"
	"github.com/martinsuchenak/exemplar/internal/testutil"
)

func TestAnonymiseRoute(t *testing.T) {
	a := testutil.NewAppWith(t, func(c *app.Config) {
		c.AnonDetector = anonymisetest.Detector{Names: []string{"Ottoline"}}
		c.MaxIngestBytes = 4096
	})
	_, issued, err := a.Accounts.Bootstrap(context.Background(), account.CreateAccountInput{Name: "root"})
	if err != nil {
		t.Fatal(err)
	}
	admin := testutil.Authenticate(t, a, issued.Secret)
	acc, err := a.Accounts.CreateAccount(context.Background(), admin, account.CreateAccountInput{Name: "reader", Kind: account.KindDeveloper})
	if err != nil {
		t.Fatal(err)
	}
	reader, err := a.Accounts.CreateKey(context.Background(), admin, account.CreateKeyInput{AccountID: acc.ID, Name: "r", Scopes: []principal.Scope{principal.ScopeRead}})
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	RegisterRoutes(mux, a)
	post := func(target, key, ctype string, body io.Reader) *httptest.ResponseRecorder {
		req := httptest.NewRequest("POST", target, body)
		if key != "" {
			req.Header.Set("X-API-Key", key)
		}
		if ctype != "" {
			req.Header.Set("Content-Type", ctype)
		}
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req)
		return w
	}
	md := "# Notes\n\nOttoline called on 0412 345 678.\n"

	// One file in, the same format out, with what it is in the headers.
	w := post("/api/v1/anonymise?name=Ottoline%20notes.md", reader.Secret, "text/markdown", strings.NewReader(md))
	h := w.Header()
	if w.Code != http.StatusOK || h.Get("Content-Type") != "text/markdown" || h.Get("X-Exemplar-Format") != "md" ||
		h.Get("X-Exemplar-Replacements") != "2" || h.Get("X-Exemplar-Unreviewed") != "true" || h.Get("Cache-Control") != "no-store" ||
		h.Get("X-Content-Type-Options") != "nosniff" || h.Get("Content-Security-Policy") != "sandbox" {
		t.Fatalf("single = %d %v %s", w.Code, h, w.Body)
	}
	if !strings.HasPrefix(h.Get("Content-Disposition"), `attachment; filename=document-`) || strings.Contains(h.Get("Content-Disposition"), "Ottoline") {
		t.Errorf("disposition = %s", h.Get("Content-Disposition"))
	}
	if out := w.Body.String(); !strings.HasPrefix(out, "# Notes\n\n") || strings.Contains(out, "Ottoline") || strings.Contains(out, "345 678") {
		t.Errorf("body = %q", out)
	}

	// A DOCX comes back as one, listing the parts it dropped.
	w = post("/api/v1/anonymise?name=quote.docx", reader.Secret, "", bytes.NewReader(anonymisetest.DOCX("Quote for Ottoline")))
	if w.Code != http.StatusOK || w.Header().Get("Content-Type") != "application/vnd.openxmlformats-officedocument.wordprocessingml.document" ||
		w.Header().Get("X-Exemplar-Dropped-Parts") != "word/vbaProject.bin" || !bytes.HasPrefix(w.Body.Bytes(), []byte("PK")) {
		t.Errorf("docx = %d %v", w.Code, w.Header())
	}

	// A format instead of a name.
	w = post("/api/v1/anonymise?format=json", reader.Secret, "", strings.NewReader(`{"name": "Ottoline"}`))
	if w.Code != http.StatusOK || w.Header().Get("Content-Type") != "application/json" || strings.Contains(w.Body.String(), "Ottoline") {
		t.Errorf("format = %d %s", w.Code, w.Body)
	}

	for target, want := range map[string]int{
		"/api/v1/anonymise":                              http.StatusBadRequest, // neither name nor format
		"/api/v1/anonymise?format=pdf":                   http.StatusBadRequest, // not a format yet
		"/api/v1/anonymise?name=a.md&format=json":        http.StatusBadRequest,
		"/api/v1/anonymise?name=a.pdf":                   http.StatusBadRequest, // unsupported file
		"/api/v1/anonymise?name=a.json":                  http.StatusBadRequest, // not JSON
		"/api/v1/anonymise?name=a.md&link_registry=yes":  http.StatusBadRequest,
		"/api/v1/anonymise?name=a.md&link_registry=true": http.StatusForbidden, // reader, not admin
		"/api/v1/anonymise?name=a.md&profile=nope":       http.StatusBadRequest,
	} {
		w := post(target, reader.Secret, "", strings.NewReader("not json"))
		if w.Code != want || !strings.Contains(w.Body.String(), `"error"`) {
			t.Errorf("%s = %d %s, want %d", target, w.Code, w.Body, want)
		}
	}
	if w := post("/api/v1/anonymise?name=a.md", "", "", strings.NewReader(md)); w.Code != http.StatusUnauthorized {
		t.Errorf("no key = %d", w.Code)
	}
	if w := post("/api/v1/anonymise?name=a.md", reader.Secret, "", bytes.NewReader(make([]byte, 4096+2<<20))); w.Code != http.StatusRequestEntityTooLarge {
		t.Errorf("too large = %d", w.Code)
	}
	if w := post("/api/v1/anonymise?name=a.md", reader.Secret, "", bytes.NewReader(make([]byte, 5000))); w.Code != http.StatusRequestEntityTooLarge {
		t.Errorf("over the limit = %d", w.Code)
	}

	// Several files: one batch, answered with a zip and its manifest.
	body, ctype := upload(t, map[string]string{"profile": "default"}, map[string][]byte{
		"a.md": []byte(md), "b.csv": []byte("Name\nWendy Moss\n"), "c.pdf": []byte("%PDF"),
	})
	w = post("/api/v1/anonymise", reader.Secret, ctype, body)
	if w.Code != http.StatusOK || w.Header().Get("Content-Type") != "application/zip" || w.Header().Get("X-Exemplar-Unreviewed") != "true" {
		t.Fatalf("multi = %d %v %s", w.Code, w.Header(), w.Body)
	}
	res, err := scrub.ReadZip(w.Body.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Files) != 3 || res.Profile != "default" || res.Linked {
		t.Fatalf("manifest = %+v", res)
	}
	stored := 0
	for _, f := range res.Files {
		if f.Error != "" {
			if !strings.Contains(f.Error, "cannot anonymise .pdf") {
				t.Errorf("error = %s", f.Error)
			}
			continue
		}
		stored++
		if strings.Contains(string(f.Data), "Ottoline") || strings.Contains(string(f.Data), "Wendy") || !strings.HasPrefix(f.Name, "document-") {
			t.Errorf("%s = %q", f.Name, f.Data)
		}
	}
	if stored != 2 {
		t.Errorf("%d files in the zip", stored)
	}
	body, ctype = upload(t, map[string]string{"link_registry": "maybe"}, map[string][]byte{"a.md": []byte(md)})
	if w := post("/api/v1/anonymise", reader.Secret, ctype, body); w.Code != http.StatusBadRequest {
		t.Errorf("bad field = %d", w.Code)
	}
	if w := post("/api/v1/anonymise", reader.Secret, "multipart/form-data", strings.NewReader("x")); w.Code != http.StatusBadRequest {
		t.Errorf("multipart without a boundary = %d", w.Code)
	}

	// While the key has a run in progress, another upload is refused
	// before any of it is read.
	run, err := a.Scrub.Reserve(testutil.Authenticate(t, a, reader.Secret))
	if err != nil {
		t.Fatal(err)
	}
	counted := &countingReader{r: strings.NewReader(md)}
	if w := post("/api/v1/anonymise?name=a.md", reader.Secret, "text/markdown", counted); w.Code != http.StatusTooManyRequests || counted.n != 0 {
		t.Errorf("busy = %d, %d bytes read", w.Code, counted.n)
	}
	run.Close()
	if w := post("/api/v1/anonymise?name=a.md", reader.Secret, "text/markdown", strings.NewReader(md)); w.Code != http.StatusOK {
		t.Errorf("after the run = %d", w.Code)
	}
}

// countingReader counts the bytes read from it.
type countingReader struct {
	r io.Reader
	n int
}

func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.n += n
	return n, err
}

func TestReadZipRefusesUnsafeNames(t *testing.T) {
	var b bytes.Buffer
	err := scrub.WriteZip(&b, &scrub.Result{Files: []scrub.FileResult{{Name: "../escape.md", Data: []byte("x")}}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := scrub.ReadZip(b.Bytes()); err == nil || !strings.Contains(err.Error(), "not a plain file name") {
		t.Errorf("err = %v", err)
	}
	if _, err := scrub.ReadZip([]byte("not a zip")); err == nil {
		t.Error("a broken zip was read")
	}
}

// The rewritten-attachment count reaches the response headers, for a
// profile that rewrites attachments.
func TestAnonymiseRouteReportsRewrittenAttachments(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "keep.toml"), []byte("name = \"keep\"\nattachments = \"anonymise\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	a := testutil.NewAppWith(t, func(c *app.Config) {
		c.AnonDetector = anonymisetest.Detector{Names: []string{"Ottoline"}}
		c.AnonProfilesDir = dir
	})
	_, issued, err := a.Accounts.Bootstrap(context.Background(), account.CreateAccountInput{Name: "root"})
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	RegisterRoutes(mux, a)
	mail := "From: Ottoline Shah <ottoline.shah@acme.com.au>\r\nTo: tom@other.com.au\r\nSubject: x\r\nMIME-Version: 1.0\r\n" +
		"Content-Type: multipart/mixed; boundary=\"B\"\r\n\r\n--B\r\nContent-Type: text/plain\r\n\r\nHi\r\n" +
		"--B\r\nContent-Type: text/csv; name=\"l.csv\"\r\nContent-Disposition: attachment; filename=\"l.csv\"\r\n\r\nName\r\nOttoline Shah\r\n--B--\r\n"
	req := httptest.NewRequest("POST", "/api/v1/anonymise?name=a.eml&profile=keep", strings.NewReader(mail))
	req.Header.Set("X-API-Key", issued.Secret)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusOK || w.Header().Get("X-Exemplar-Attachments-Rewritten") != "1" || w.Header().Get("X-Exemplar-Attachments") != "0" ||
		strings.Contains(w.Body.String(), "Ottoline") {
		t.Errorf("= %d %v\n%s", w.Code, w.Header(), w.Body)
	}
}

// When the detector fails, a single file gets 503 and the zip's manifest a
// generic error per file; neither shows the detector's address.
func TestAnonymiseRouteHidesDetectorFailures(t *testing.T) {
	for name, d := range anonymisetest.FailingPresidios(t) {
		a := testutil.NewAppWith(t, func(c *app.Config) { c.AnonDetector = d })
		_, issued, err := a.Accounts.Bootstrap(context.Background(), account.CreateAccountInput{Name: "root"})
		if err != nil {
			t.Fatal(err)
		}
		mux := http.NewServeMux()
		RegisterRoutes(mux, a)
		post := func(target, ctype string, body io.Reader) *httptest.ResponseRecorder {
			req := httptest.NewRequest("POST", target, body)
			req.Header.Set("X-API-Key", issued.Secret)
			if ctype != "" {
				req.Header.Set("Content-Type", ctype)
			}
			w := httptest.NewRecorder()
			mux.ServeHTTP(w, req)
			return w
		}
		hidden := func(where, msg string) {
			for _, l := range []string{"127.0.0.1", "http://", "/analyze", "Presidio", "dial tcp", "refused", "model", "decoding"} {
				if strings.Contains(msg, l) {
					t.Errorf("%s, %s shows %q: %s", name, where, l, msg)
				}
			}
		}

		w := post("/api/v1/anonymise?name=a.md", "text/markdown", strings.NewReader("Ottoline called.\n"))
		if w.Code != http.StatusServiceUnavailable || !strings.Contains(w.Body.String(), anonymise.DetectorUnavailable) {
			t.Errorf("%s, single = %d %s", name, w.Code, w.Body)
		}
		hidden("single", w.Body.String())

		body, ctype := upload(t, nil, map[string][]byte{"a.md": []byte("Ottoline called.\n"), "b.csv": []byte("Name\nWendy Moss\n")})
		w = post("/api/v1/anonymise", ctype, body)
		if w.Code != http.StatusOK {
			t.Fatalf("%s, multi = %d %s", name, w.Code, w.Body)
		}
		res, err := scrub.ReadZip(w.Body.Bytes())
		if err != nil {
			t.Fatal(err)
		}
		for _, f := range res.Files {
			if f.Error != anonymise.DetectorUnavailable {
				t.Errorf("%s, multi file = %+v", name, f)
			}
			hidden("the manifest", f.Error)
		}
	}
}
