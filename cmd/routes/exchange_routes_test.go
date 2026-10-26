package routes

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
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
