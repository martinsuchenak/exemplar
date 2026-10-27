package mcp

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"

	mcplib "github.com/paularlott/mcp"

	"github.com/martinsuchenak/exemplar/internal/account"
	"github.com/martinsuchenak/exemplar/internal/app"
	"github.com/martinsuchenak/exemplar/internal/apperr"
	"github.com/martinsuchenak/exemplar/internal/audit"
	"github.com/martinsuchenak/exemplar/internal/dataset"
	"github.com/martinsuchenak/exemplar/internal/fixture"
	"github.com/martinsuchenak/exemplar/internal/generate"
	"github.com/martinsuchenak/exemplar/internal/principal"
	"github.com/martinsuchenak/exemplar/internal/synth"
	"github.com/martinsuchenak/exemplar/internal/testutil"
)

var ctx = context.Background()

// world is an app with data, served over MCP, and keys to call it with.
type world struct {
	a                       *app.App
	url                     string
	admin, alice, bob       principal.Principal
	adminKey, aliceKey      string
	bobKey, aliceReadOnly   string
	workspace, global, quar *dataset.Dataset
	generated               []fixture.Fixture
	personID                string
}

func newWorld(t *testing.T) *world {
	t.Helper()
	w := &world{a: testutil.NewApp(t)}
	w.admin = testutil.Admin(t, w.a)
	w.alice = testutil.Member(t, w.a, w.admin, "alice")
	w.bob = testutil.Member(t, w.a, w.admin, "bob")
	w.adminKey = secret(t, w.a, w.admin, principal.ScopeRead, principal.ScopeWrite, principal.ScopeAdmin)
	w.aliceKey = secret(t, w.a, w.alice, principal.ScopeRead, principal.ScopeWrite)
	w.bobKey = secret(t, w.a, w.bob, principal.ScopeRead, principal.ScopeWrite)
	w.aliceReadOnly = secret(t, w.a, w.alice, principal.ScopeRead)

	w.workspace = mustDataset(t, w.a, w.alice, "mine", dataset.ScopeWorkspace)
	w.global = mustDataset(t, w.a, w.admin, "library", dataset.ScopeGlobal)
	w.quar = mustDataset(t, w.a, w.admin, "inbox", dataset.ScopeQuarantine)

	for _, req := range []generate.Request{
		{Kind: generate.KindEmails, Count: 2, Shapes: []synth.Shape{synth.ShapePlain, synth.ShapeReply}, Seed: 1, People: 3},
		{Kind: generate.KindFiles, Count: 2, Formats: []string{"pdf", "png"}, Seed: 2, People: 3},
		{Kind: generate.KindRecords, Count: 2, Schemas: []synth.RecordSchema{synth.SchemaContact}, Seed: 3, People: 3},
	} {
		res, err := w.a.Generate.Generate(ctx, w.alice, w.workspace.ID, req)
		if err != nil {
			t.Fatal(err)
		}
		w.generated = append(w.generated, res.Fixtures...)
		w.personID = res.PersonIDs[0]
	}

	mux := http.NewServeMux()
	mux.Handle("/mcp", Handler(w.a))
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	w.url = srv.URL + "/mcp"
	return w
}

// secret issues a key for p with scopes and returns its secret.
func secret(t *testing.T, a *app.App, p principal.Principal, scopes ...principal.Scope) string {
	t.Helper()
	issued, err := a.Accounts.CreateKey(ctx, p, account.CreateKeyInput{Name: "mcp", Scopes: scopes})
	if err != nil {
		t.Fatal(err)
	}
	return issued.Secret
}

func mustDataset(t *testing.T, a *app.App, p principal.Principal, name string, scope dataset.Scope) *dataset.Dataset {
	t.Helper()
	d, err := a.Datasets.Create(ctx, p, dataset.CreateInput{Name: name, Scope: scope})
	if err != nil {
		t.Fatal(err)
	}
	return d
}

// call invokes a tool with key and returns the decoded JSON text content.
func (w *world) call(t *testing.T, key, tool string, args map[string]any) (map[string]any, *mcplib.ToolResponse, error) {
	t.Helper()
	c := mcplib.NewClient(w.url, mcplib.NewBearerTokenAuth(key), "")
	resp, err := c.CallTool(ctx, tool, args)
	if err != nil {
		return nil, nil, err
	}
	var out map[string]any
	if err := json.Unmarshal([]byte(resp.Content[0].Text), &out); err != nil {
		t.Fatalf("%s returned non-JSON text: %q", tool, resp.Content[0].Text)
	}
	return out, resp, nil
}

func toolCode(err error) int {
	var te *mcplib.ToolError
	if errors.As(err, &te) {
		return te.Code
	}
	return 0
}

func TestToolsListed(t *testing.T) {
	w := newWorld(t)
	c := mcplib.NewClient(w.url, mcplib.NewBearerTokenAuth(w.aliceKey), "")
	tools, err := c.ListTools(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, tool := range tools {
		names = append(names, tool.Name)
	}
	sort.Strings(names)
	if strings.Join(names, ",") != "get_fixture,get_person,list_datasets,search_fixtures" {
		t.Errorf("tools = %v", names)
	}
}

func TestEveryRequestNeedsAKey(t *testing.T) {
	w := newWorld(t)
	for _, key := range []string{"", "exm_wrong", "not-a-key"} {
		c := mcplib.NewClient(w.url, mcplib.NewBearerTokenAuth(key), "")
		if _, err := c.ListTools(ctx); err == nil {
			t.Errorf("key %q listed tools", key)
		}
	}
	// X-API-Key works as well as a bearer token.
	body := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"list_datasets","arguments":{}}}`
	req, _ := http.NewRequest("POST", w.url, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	req.Header.Set("X-API-Key", w.aliceKey)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	var buf bytes.Buffer
	_, _ = buf.ReadFrom(resp.Body)
	if resp.StatusCode != http.StatusOK || !strings.Contains(buf.String(), "mine") {
		t.Errorf("X-API-Key call: %d %s", resp.StatusCode, buf.String())
	}
}

func TestListDatasets(t *testing.T) {
	w := newWorld(t)
	out, resp, err := w.call(t, w.aliceKey, "list_datasets", nil)
	if err != nil {
		t.Fatal(err)
	}
	if out["count"] != float64(2) || resp.StructuredContent == nil {
		t.Errorf("alice sees %v datasets (want her workspace and the library)", out["count"])
	}
	// Quarantine is never served over MCP, even to an admin key.
	out, _, _ = w.call(t, w.adminKey, "list_datasets", map[string]any{"scope": "quarantine"})
	if out["count"] != float64(0) {
		t.Errorf("admin quarantine = %v, want none over MCP", out["count"])
	}
	out, _, _ = w.call(t, w.adminKey, "list_datasets", nil)
	if out["count"] != float64(2) || strings.Contains(fmt.Sprint(out), w.quar.ID) {
		t.Errorf("admin sees %v datasets (want the library and alice's workspace, not quarantine): %v", out["count"], out)
	}
	out, _, _ = w.call(t, w.aliceKey, "list_datasets", map[string]any{"name": "library"})
	if out["count"] != float64(1) {
		t.Errorf("by name = %v", out["count"])
	}
	if _, _, err := w.call(t, w.aliceKey, "list_datasets", map[string]any{"scope": "public"}); toolCode(err) != mcplib.ErrorCodeInvalidParams {
		t.Errorf("bad scope err = %v", err)
	}
	if _, _, err := w.call(t, w.aliceReadOnly, "list_datasets", nil); err != nil {
		t.Errorf("read-only key: %v", err)
	}
}

func TestSearchFixtures(t *testing.T) {
	w := newWorld(t)
	out, _, err := w.call(t, w.aliceKey, "search_fixtures", map[string]any{"dataset_id": w.workspace.ID})
	if err != nil || out["count"] != float64(len(w.generated)) {
		t.Fatalf("all = %v, %v (want %d)", out["count"], err, len(w.generated))
	}
	for args, want := range map[string]float64{
		`{"kind":"email"}`:              2,
		`{"format":"pdf"}`:              1,
		`{"scenario":"reply"}`:          1,
		`{"record_schema":"contact"}`:   2,
		`{"review_state":"unreviewed"}`: 0,
		`{"limit":1,"offset":1}`:        1,
	} {
		a := map[string]any{"dataset_id": w.workspace.ID}
		_ = json.Unmarshal([]byte(args), &a)
		out, _, err := w.call(t, w.aliceKey, "search_fixtures", a)
		if err != nil || out["count"] != want {
			t.Errorf("%s = %v, %v; want %v", args, out["count"], err, want)
		}
	}
	out, _, _ = w.call(t, w.aliceKey, "search_fixtures", map[string]any{"dataset_id": w.workspace.ID, "person_id": w.personID})
	if out["count"].(float64) < 1 {
		t.Error("person filter found nothing")
	}

	if _, _, err := w.call(t, w.aliceKey, "search_fixtures", nil); toolCode(err) != mcplib.ErrorCodeInvalidParams {
		t.Errorf("missing dataset_id err = %v", err)
	}
	if _, _, err := w.call(t, w.bobKey, "search_fixtures", map[string]any{"dataset_id": w.workspace.ID}); toolCode(err) != errorCodeNotFound {
		t.Errorf("bob searching alice's workspace err = %v", err)
	}
	if _, _, err := w.call(t, w.aliceKey, "search_fixtures", map[string]any{"dataset_id": w.workspace.ID, "kind": "video"}); toolCode(err) != mcplib.ErrorCodeInvalidParams {
		t.Errorf("bad kind err = %v", err)
	}
}

func (w *world) fixtureByFormat(format string) fixture.Fixture {
	for _, f := range w.generated {
		if f.Format == format {
			return f
		}
	}
	panic("no fixture of format " + format)
}

func TestGetFixtureContent(t *testing.T) {
	w := newWorld(t)
	eml, pdf, png, rec := w.fixtureByFormat("eml"), w.fixtureByFormat("pdf"), w.fixtureByFormat("png"), w.fixtureByFormat("json")

	out, _, err := w.call(t, w.aliceKey, "get_fixture", map[string]any{"id": eml.ID})
	if err != nil || !strings.Contains(fmt.Sprint(out["content"]), "Message-ID:") {
		t.Errorf("email content = %v, %v", out["content"], err)
	}
	if f := out["fixture"].(map[string]any); f["id"] != eml.ID || len(f["people"].([]any)) == 0 {
		t.Errorf("fixture metadata = %v", f)
	}
	out, _, _ = w.call(t, w.aliceKey, "get_fixture", map[string]any{"id": rec.ID})
	if !strings.Contains(fmt.Sprint(out["content"]), `"schema": "contact"`) {
		t.Errorf("record content = %v", out["content"])
	}

	out, _, _ = w.call(t, w.aliceKey, "get_fixture", map[string]any{"id": pdf.ID})
	if out["content"] != nil || !strings.Contains(fmt.Sprint(out["content_note"]), "content=base64") {
		t.Errorf("pdf auto = %v", out)
	}
	out, _, _ = w.call(t, w.aliceKey, "get_fixture", map[string]any{"id": pdf.ID, "content": "base64"})
	if data, err := base64.StdEncoding.DecodeString(fmt.Sprint(out["content_base64"])); err != nil || !bytes.HasPrefix(data, []byte("%PDF-")) {
		t.Errorf("pdf base64 decodes to %q, %v", data[:min(8, len(data))], err)
	}

	_, resp, err := w.call(t, w.aliceKey, "get_fixture", map[string]any{"id": png.ID})
	if err != nil || len(resp.Content) != 2 || resp.Content[1].Type != "image" || resp.Content[1].MimeType != "image/png" {
		t.Errorf("png = %+v, %v", resp, err)
	}

	out, _, _ = w.call(t, w.aliceKey, "get_fixture", map[string]any{"id": eml.ID, "content": "none"})
	if out["content"] != nil || out["content_note"] != nil {
		t.Errorf("none = %v", out)
	}
	if _, _, err := w.call(t, w.aliceKey, "get_fixture", map[string]any{"id": eml.ID, "content": "all"}); toolCode(err) != mcplib.ErrorCodeInvalidParams {
		t.Errorf("bad mode err = %v", err)
	}
	if _, _, err := w.call(t, w.aliceKey, "get_fixture", map[string]any{"id": "missing"}); toolCode(err) != errorCodeNotFound {
		t.Errorf("missing err = %v", err)
	}
	if _, _, err := w.call(t, w.bobKey, "get_fixture", map[string]any{"id": eml.ID}); toolCode(err) != errorCodeNotFound {
		t.Errorf("bob err = %v", err)
	}
	if _, _, err := w.call(t, w.aliceKey, "get_fixture", nil); toolCode(err) != mcplib.ErrorCodeInvalidParams {
		t.Errorf("missing id err = %v", err)
	}
}

func TestGetFixtureLimitsAndRealData(t *testing.T) {
	w := newWorld(t)
	big, err := w.a.Fixtures.Create(ctx, w.alice, fixture.CreateInput{
		DatasetID: w.workspace.ID, Name: "big.txt", Content: strings.NewReader(strings.Repeat("x", maxInlineText+1)),
	})
	if err != nil {
		t.Fatal(err)
	}
	out, _, _ := w.call(t, w.aliceKey, "get_fixture", map[string]any{"id": big.ID})
	if out["content"] != nil || !strings.Contains(fmt.Sprint(out["content_note"]), "over the inline limit") {
		t.Errorf("big text = %v", out["content_note"])
	}
	huge, err := w.a.Fixtures.Create(ctx, w.alice, fixture.CreateInput{
		DatasetID: w.workspace.ID, Name: "huge.bin", Content: bytes.NewReader(make([]byte, maxInlineBinary+1)),
	})
	if err != nil {
		t.Fatal(err)
	}
	out, _, _ = w.call(t, w.aliceKey, "get_fixture", map[string]any{"id": huge.ID, "content": "base64"})
	if out["content_base64"] != nil || !strings.Contains(fmt.Sprint(out["content_note"]), "over the inline limit") {
		t.Errorf("huge base64 = %v", out["content_note"])
	}
	latin, err := w.a.Fixtures.Create(ctx, w.alice, fixture.CreateInput{
		DatasetID: w.workspace.ID, Name: "latin1.txt", Content: bytes.NewReader([]byte{0x63, 0x61, 0x66, 0xe9}),
	})
	if err != nil {
		t.Fatal(err)
	}
	out, _, _ = w.call(t, w.aliceKey, "get_fixture", map[string]any{"id": latin.ID})
	if out["content"] != nil || !strings.Contains(fmt.Sprint(out["content_note"]), "UTF-8") {
		t.Errorf("non-UTF-8 text = %v", out)
	}

	// Real-derived content: quarantine is hidden from every key over MCP;
	// once approved and promoted, it is served and every read audited.
	inQuarantine, err := w.a.Fixtures.Create(ctx, w.admin, fixture.CreateInput{
		DatasetID: w.quar.ID, Name: "real.eml", Source: fixture.SourceAnonymised, Content: strings.NewReader("Subject: x\r\n\r\nbody"),
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{w.aliceKey, w.adminKey} {
		if _, _, err := w.call(t, key, "get_fixture", map[string]any{"id": inQuarantine.ID}); toolCode(err) != errorCodeNotFound {
			t.Errorf("reading quarantine over MCP err = %v", err)
		}
		if _, _, err := w.call(t, key, "search_fixtures", map[string]any{"dataset_id": w.quar.ID}); toolCode(err) != errorCodeNotFound {
			t.Errorf("searching quarantine over MCP err = %v", err)
		}
	}
	if _, err := w.a.Fixtures.Review(ctx, w.admin, inQuarantine.ID, true); err != nil {
		t.Fatal(err)
	}
	promoted, err := w.a.Datasets.Promote(ctx, w.admin, w.quar.ID, dataset.PromoteInput{Name: "real-emails"})
	if err != nil {
		t.Fatal(err)
	}
	copies, err := w.a.Fixtures.List(ctx, w.admin, promoted.Dataset.ID, fixture.ListFilter{})
	if err != nil || len(copies) != 1 {
		t.Fatalf("promoted = %v, %v", copies, err)
	}
	real := copies[0]
	if out, _, err := w.call(t, w.aliceKey, "get_fixture", map[string]any{"id": real.ID}); err != nil || !strings.Contains(fmt.Sprint(out["content"]), "body") {
		t.Errorf("promoted read = %v, %v", out, err)
	}
	events, _ := w.a.Audit.List(ctx, w.admin, audit.Filter{Action: audit.ActionFixtureDownload, TargetID: real.ID})
	if len(events) != 1 || events[0].AccountID != w.alice.AccountID {
		t.Errorf("download audit events = %d, want 1", len(events))
	}
	// Metadata only does not read content, so it is not a download.
	if _, _, err := w.call(t, w.aliceKey, "get_fixture", map[string]any{"id": real.ID, "content": "none"}); err != nil {
		t.Fatal(err)
	}
	if events, _ := w.a.Audit.List(ctx, w.admin, audit.Filter{Action: audit.ActionFixtureDownload, TargetID: real.ID}); len(events) != 1 {
		t.Errorf("metadata read was audited as a download")
	}
}

func TestGetPerson(t *testing.T) {
	w := newWorld(t)
	out, _, err := w.call(t, w.aliceReadOnly, "get_person", map[string]any{"id": w.personID})
	if err != nil || out["id"] != w.personID || !strings.HasSuffix(fmt.Sprint(out["email"]), ".example") {
		t.Errorf("person = %v, %v", out, err)
	}
	if _, _, err := w.call(t, w.aliceKey, "get_person", map[string]any{"id": "missing"}); toolCode(err) != errorCodeNotFound {
		t.Errorf("missing err = %v", err)
	}
	if _, _, err := w.call(t, w.aliceKey, "get_person", nil); toolCode(err) != mcplib.ErrorCodeInvalidParams {
		t.Errorf("missing id err = %v", err)
	}
}

func TestToolErrorMapping(t *testing.T) {
	for err, code := range map[error]int{
		fmt.Errorf("%w: x", apperr.ErrInvalid):   mcplib.ErrorCodeInvalidParams,
		fmt.Errorf("x: %w", apperr.ErrNotFound):  errorCodeNotFound,
		fmt.Errorf("%w: x", apperr.ErrForbidden): errorCodeForbidden,
		fmt.Errorf("%w: x", apperr.ErrConflict):  errorCodeConflict,
		apperr.ErrUnauthenticated:                errorCodeUnauthenticated,
		errors.New("disk /secret failed"):        mcplib.ErrorCodeInternalError,
	} {
		got := toolError(err)
		if toolCode(got) != code {
			t.Errorf("toolError(%v) code = %d, want %d", err, toolCode(got), code)
		}
		if code == mcplib.ErrorCodeInternalError && strings.Contains(got.Error(), "secret") {
			t.Error("internal error leaked its message")
		}
	}
	if _, err := caller(ctx); toolCode(err) != errorCodeUnauthenticated {
		t.Errorf("caller without principal err = %v", err)
	}
}

func TestHandlerNeedsAKey(t *testing.T) {
	a := testutil.NewApp(t)
	rec := httptest.NewRecorder()
	Handler(a).ServeHTTP(rec, httptest.NewRequest("POST", "/mcp", strings.NewReader("{}")))
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("/mcp without a key = %d", rec.Code)
	}
}
