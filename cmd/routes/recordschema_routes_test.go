package routes

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/martinsuchenak/exemplar/internal/recordschema"
)

const routeTicket = "name = \"ticket\"\ntitle = \"{reference}\"\n\n[[fields]]\nname = \"reference\"\nref = \"TK\"\n"

func schemaBody(t *testing.T, toml string, version int) string {
	t.Helper()
	b, err := json.Marshal(recordschema.Body{TOML: toml, Version: version})
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestRecordSchemaRoutes(t *testing.T) {
	mux, key, dsID := testServer(t)
	changed := strings.Replace(routeTicket, "TK", "SR", 1)
	for _, r := range []struct {
		method, path, body string
		want               int
	}{
		{"POST", "/api/v1/record-schemas", schemaBody(t, routeTicket, 0), http.StatusCreated},
		{"POST", "/api/v1/record-schemas", schemaBody(t, routeTicket, 0), http.StatusConflict},
		{"POST", "/api/v1/record-schemas", schemaBody(t, strings.Replace(routeTicket, `"ticket"`, `"quote"`, 1), 0), http.StatusConflict},
		{"POST", "/api/v1/record-schemas", schemaBody(t, routeTicket+"#"+strings.Repeat("x", 300<<10), 0), http.StatusRequestEntityTooLarge},
		{"GET", "/api/v1/record-schemas/ticket", "", http.StatusOK},
		{"GET", "/api/v1/record-schemas/quote", "", http.StatusOK},
		{"GET", "/api/v1/record-schemas/missing", "", http.StatusNotFound},
		{"GET", "/api/v1/record-schemas/ticket?source=api", "", http.StatusOK},
		{"GET", "/api/v1/record-schemas/quote?source=api", "", http.StatusNotFound},
		{"GET", "/api/v1/record-schemas/ticket?source=file", "", http.StatusBadRequest},
		{"PUT", "/api/v1/record-schemas/ticket", schemaBody(t, changed, 5), http.StatusConflict},
		{"PUT", "/api/v1/record-schemas/ticket", schemaBody(t, changed, 1), http.StatusOK},
		{"PUT", "/api/v1/record-schemas/missing", schemaBody(t, strings.Replace(routeTicket, `"ticket"`, `"missing"`, 1), 0), http.StatusNotFound},
		{"PUT", "/api/v1/record-schemas/quote", schemaBody(t, strings.Replace(routeTicket, `"ticket"`, `"quote"`, 1), 0), http.StatusConflict},
		{"POST", "/api/v1/datasets/" + dsID + "/generate", `{"kind":"records","count":1,"schemas":["ticket"]}`, http.StatusCreated},
		{"DELETE", "/api/v1/record-schemas/ticket", "", http.StatusNoContent},
		{"DELETE", "/api/v1/record-schemas/ticket", "", http.StatusNotFound},
		{"DELETE", "/api/v1/record-schemas/quote", "", http.StatusConflict},
	} {
		if w := call(mux, r.method, r.path, key, r.body); w.Code != r.want {
			t.Errorf("%s %s: %d, want %d (%s)", r.method, r.path, w.Code, r.want, w.Body)
		}
	}
}

func TestRecordSchemaShowsItsFile(t *testing.T) {
	mux, key, _ := testServer(t)
	if w := call(mux, "POST", "/api/v1/record-schemas", key, schemaBody(t, routeTicket, 0)); w.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", w.Code, w.Body)
	}
	w := call(mux, "GET", "/api/v1/record-schemas/ticket", key, "")
	var d recordschema.Definition
	if err := json.Unmarshal(w.Body.Bytes(), &d); err != nil || d.TOML != routeTicket || d.Source != "api" || d.Version != 1 {
		t.Errorf("get = %+v, %v", d, err)
	}
}
