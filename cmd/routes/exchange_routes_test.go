package routes

import (
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/martinsuchenak/exemplar/internal/account"
	"github.com/martinsuchenak/exemplar/internal/app"
	"github.com/martinsuchenak/exemplar/internal/archive"
	"github.com/martinsuchenak/exemplar/internal/rest"
	"github.com/martinsuchenak/exemplar/internal/testutil"
)

func TestExportAndImportRoutes(t *testing.T) {
	mux, key, datasetID := testServer(t)
	w := call(mux, "POST", "/api/v1/export", key, `{"dataset_ids":["`+datasetID+`"]}`)
	if w.Code != http.StatusOK || w.Header().Get("Content-Type") != "application/gzip" || w.Header().Get("Cache-Control") != "no-store" ||
		!strings.HasPrefix(w.Header().Get("Content-Disposition"), "attachment; filename=\"exemplar-export-") ||
		w.Header().Get("X-Exemplar-Datasets") != "1" || w.Header().Get("X-Exemplar-Skipped") != "0" {
		t.Fatalf("export = %d %v", w.Code, w.Header())
	}
	archive := w.Body.Bytes()

	for _, tt := range []struct {
		name, path, body string
		want             int
	}{
		{"bad json", "/api/v1/export", "{", http.StatusBadRequest},
		{"no datasets", "/api/v1/export", `{"dataset_ids":[]}`, http.StatusBadRequest},
		{"unknown dataset", "/api/v1/export", `{"dataset_ids":["missing"]}`, http.StatusNotFound},
		{"not an archive", "/api/v1/import", "plain text", http.StatusBadRequest},
	} {
		if w := call(mux, "POST", tt.path, key, tt.body); w.Code != tt.want {
			t.Errorf("%s = %d %s", tt.name, w.Code, w.Body)
		}
	}

	req := httptest.NewRequest("POST", "/api/v1/import", bytes.NewReader(archive))
	req.Header.Set("X-API-Key", key)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated || !strings.Contains(rec.Body.String(), `"created":false`) {
		t.Errorf("import = %d %s", rec.Code, rec.Body)
	}
}

func TestImportSizeLimit(t *testing.T) {
	a := testutil.NewAppWith(t, func(c *app.Config) { c.MaxImportBytes = 512 })
	_, issued, err := a.Accounts.Bootstrap(context.Background(), account.CreateAccountInput{Name: "root"})
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	RegisterRoutes(mux, a)
	// Random-looking records, so compression cannot shrink them under the limit.
	var buf bytes.Buffer
	w, _ := archive.NewWriter(&buf, archive.Manifest{Kind: archive.KindExport})
	records := make([]map[string]string, 200)
	for i := range records {
		records[i] = map[string]string{"description": fmt.Sprintf("%x", sha256.Sum256([]byte{byte(i)}))}
	}
	_ = archive.Records(w, "datasets.jsonl", records)
	_ = w.Close()
	big := buf.Bytes()
	req := httptest.NewRequest("POST", "/api/v1/import", bytes.NewReader(big))
	req.Header.Set("X-API-Key", issued.Secret)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Errorf("oversized import = %d %s", rec.Code, rec.Body)
	}
}

func TestOversizedJSONIs413(t *testing.T) {
	mux, key, datasetID := testServer(t)
	big := `{"name":"` + strings.Repeat("a", rest.MaxBodyBytes) + `"}`
	for _, path := range []string{"/api/v1/datasets", "/api/v1/datasets/" + datasetID + "/fork", "/api/v1/export"} {
		if w := call(mux, "POST", path, key, big); w.Code != http.StatusRequestEntityTooLarge {
			t.Errorf("%s = %d %s", path, w.Code, w.Body)
		}
	}
	if w := call(mux, "POST", "/api/v1/datasets", key, `{"name":"a"} {"name":"b"}`); w.Code != http.StatusBadRequest {
		t.Errorf("trailing data = %d", w.Code)
	}
	// Optional bodies may still be empty (the fork then clashes on its default name).
	if w := call(mux, "POST", "/api/v1/datasets/"+datasetID+"/fork", key, ""); w.Code != http.StatusConflict {
		t.Errorf("empty fork body = %d %s", w.Code, w.Body)
	}
}

func TestImportRefusesGzipBombs(t *testing.T) {
	for name, cfg := range map[string]func(*app.Config){
		// 64 KiB compressed limit: at most 640 KiB uncompressed.
		"uncompressed total": func(c *app.Config) { c.MaxImportBytes = 64 << 10 },
		// Content entries within the fixture limit, whatever the archive limit.
		"content entry": func(c *app.Config) { c.MaxFixtureBytes = 1 << 20 },
	} {
		a := testutil.NewAppWith(t, cfg)
		_, issued, err := a.Accounts.Bootstrap(context.Background(), account.CreateAccountInput{Name: "root"})
		if err != nil {
			t.Fatal(err)
		}
		mux := http.NewServeMux()
		RegisterRoutes(mux, a)

		zeros := make([]byte, 8<<20)
		hash := sha256.Sum256(zeros)
		var buf bytes.Buffer
		w, _ := archive.NewWriter(&buf, archive.Manifest{Kind: archive.KindExport, Counts: map[string]int{"datasets": 1, "blobs": 1}})
		_ = archive.Records(w, "datasets.jsonl", []map[string]any{{"id": "d1", "name": "bomb", "scope": "workspace", "version": 1}})
		_ = w.Blob(fmt.Sprintf("%x", hash), int64(len(zeros)), bytes.NewReader(zeros))
		_ = w.Close()
		if buf.Len() > 64<<10 {
			t.Fatalf("bomb is %d bytes compressed", buf.Len())
		}

		req := httptest.NewRequest("POST", "/api/v1/import", bytes.NewReader(buf.Bytes()))
		req.Header.Set("X-API-Key", issued.Secret)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		if rec.Code != http.StatusRequestEntityTooLarge {
			t.Errorf("%s: import of an 8 MiB bomb = %d %s", name, rec.Code, rec.Body)
		}
	}
}
