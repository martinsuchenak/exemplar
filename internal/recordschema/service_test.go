package recordschema_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/martinsuchenak/exemplar/internal/app"
	"github.com/martinsuchenak/exemplar/internal/apperr"
	"github.com/martinsuchenak/exemplar/internal/audit"
	"github.com/martinsuchenak/exemplar/internal/generate"
	"github.com/martinsuchenak/exemplar/internal/principal"
	"github.com/martinsuchenak/exemplar/internal/recordschema"
	"github.com/martinsuchenak/exemplar/internal/synth"
	"github.com/martinsuchenak/exemplar/internal/testutil"
)

const ticket = `name = "ticket"
title = "{reference}"

[[fields]]
name = "reference"
ref = "TK"
`

const part = `name = "part"
step = "catalogue"
title = "{name}"

[catalogue]
size = 2

[[catalogue.fields]]
name = "name"
template = "Part"

[[catalogue.fields]]
name = "unit_price"
money = [5, 5]
`

const order = `name = "order"
title = "x"

[[fields]]
name = "items"
lines = { catalogue = "part", count = [1, 1], qty = [2, 2] }
`

// records makes one record of schema, as any key may.
func records(t *testing.T, a *app.App, schema synth.RecordSchema) (map[string]any, error) {
	t.Helper()
	res, err := a.Generate.Records(context.Background(), principal.Principal{}, generate.Request{Kind: generate.KindRecords, Count: 1, Seed: 1, Schemas: []synth.RecordSchema{schema}})
	if err != nil {
		return nil, err
	}
	var out map[string]any
	if err := json.Unmarshal(res.Records[0], &out); err != nil {
		t.Fatal(err)
	}
	return out, nil
}

func listed(t *testing.T, a *app.App, p principal.Principal, name synth.RecordSchema) *synth.Schema {
	t.Helper()
	all, err := a.Generate.Schemas(p)
	if err != nil {
		t.Fatal(err)
	}
	for _, sc := range all {
		if sc.Name == name {
			return sc
		}
	}
	return nil
}

func events(t *testing.T, a *app.App, admin principal.Principal, action string) []audit.Event {
	t.Helper()
	ev, err := a.Audit.List(context.Background(), admin, audit.Filter{Action: action})
	if err != nil {
		t.Fatal(err)
	}
	return ev
}

// An admin adds, changes and removes a schema; every change takes effect
// at once and is audited.
func TestLifecycle(t *testing.T) {
	ctx := context.Background()
	a := testutil.NewApp(t)
	admin := testutil.Admin(t, a)
	alice := testutil.Member(t, a, admin, "alice")
	s := a.RecordSchemas

	if _, err := records(t, a, "ticket"); !errors.Is(err, apperr.ErrInvalid) {
		t.Fatalf("ticket before adding: %v", err)
	}
	d, err := s.Create(ctx, admin, ticket)
	if err != nil {
		t.Fatal(err)
	}
	if d.Name != "ticket" || d.Source != synth.SourceAPI || d.Version != 1 || len(d.Hash) != 8 || d.TOML != ticket || d.CreatedBy != admin.AccountID || d.UpdatedAt == nil || d.Problem != "" {
		t.Errorf("created = %+v", d)
	}
	rec, err := records(t, a, "ticket")
	if err != nil || !strings.HasPrefix(rec["reference"].(string), "TK-") {
		t.Errorf("record = %v, %v", rec, err)
	}
	if sc := listed(t, a, alice, "ticket"); sc == nil || sc.Source != synth.SourceAPI || sc.Version != 1 {
		t.Errorf("listed = %+v", sc)
	}
	got, err := s.Get(ctx, alice, "ticket", false)
	if err != nil || got.TOML != ticket || got.Hash != d.Hash {
		t.Errorf("get = %+v, %v", got, err)
	}

	changed := strings.Replace(ticket, `"TK"`, `"SR"`, 1)
	u, err := s.Update(ctx, admin, "ticket", changed, 1)
	if err != nil {
		t.Fatal(err)
	}
	if u.Version != 2 || u.Hash == d.Hash || u.TOML != changed || u.CreatedAt.IsZero() {
		t.Errorf("updated = %+v", u)
	}
	if rec, _ := records(t, a, "ticket"); !strings.HasPrefix(rec["reference"].(string), "SR-") {
		t.Errorf("record after update = %v", rec)
	}
	// The same file again changes nothing.
	same, err := s.Update(ctx, admin, "ticket", changed, 0)
	if err != nil || same.Version != 2 || same.Hash != u.Hash {
		t.Errorf("same file = %+v, %v", same, err)
	}

	if err := s.Delete(ctx, admin, "ticket"); err != nil {
		t.Fatal(err)
	}
	if _, err := records(t, a, "ticket"); !errors.Is(err, apperr.ErrInvalid) {
		t.Errorf("ticket after removing: %v", err)
	}
	if _, err := s.Get(ctx, alice, "ticket", false); !errors.Is(err, apperr.ErrNotFound) {
		t.Errorf("get after removing: %v", err)
	}

	created := events(t, a, admin, audit.ActionRecordSchemaCreate)
	updated := events(t, a, admin, audit.ActionRecordSchemaUpdate)
	deleted := events(t, a, admin, audit.ActionRecordSchemaDelete)
	if len(created) != 1 || created[0].TargetID != "ticket" || created[0].TargetType != "record_schema" || created[0].Details["hash"] != d.Hash || created[0].AccountID != admin.AccountID {
		t.Errorf("create events = %+v", created)
	}
	if len(updated) != 1 || updated[0].Details["previous_hash"] != d.Hash || updated[0].Details["hash"] != u.Hash || updated[0].Details["version"] != float64(2) {
		t.Errorf("update events = %+v", updated)
	}
	if len(deleted) != 1 || deleted[0].Details["hash"] != u.Hash {
		t.Errorf("delete events = %+v", deleted)
	}
	for _, e := range append(created, updated...) {
		if _, ok := e.Details["toml"]; ok {
			t.Error("an event holds the file")
		}
	}
}

// Only admin keys change schemas; any key that can read reads them.
func TestAccess(t *testing.T) {
	ctx := context.Background()
	a := testutil.NewApp(t)
	admin := testutil.Admin(t, a)
	alice := testutil.Member(t, a, admin, "alice")
	s := a.RecordSchemas
	if _, err := s.Create(ctx, admin, ticket); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Create(ctx, alice, part); !errors.Is(err, apperr.ErrForbidden) {
		t.Errorf("member create: %v", err)
	}
	if _, err := s.Update(ctx, alice, "ticket", ticket, 0); !errors.Is(err, apperr.ErrForbidden) {
		t.Errorf("member update: %v", err)
	}
	if err := s.Delete(ctx, alice, "ticket"); !errors.Is(err, apperr.ErrForbidden) {
		t.Errorf("member delete: %v", err)
	}
	// An admin's account with an everyday key is not an admin.
	everyday := admin
	everyday.Scopes = []principal.Scope{principal.ScopeRead, principal.ScopeWrite}
	if _, err := s.Create(ctx, everyday, part); !errors.Is(err, apperr.ErrForbidden) {
		t.Errorf("admin's write key create: %v", err)
	}
	reader := principal.Principal{AccountID: alice.AccountID, Scopes: []principal.Scope{principal.ScopeRead}}
	if _, err := s.Get(ctx, reader, "ticket", false); err != nil {
		t.Errorf("reader get: %v", err)
	}
	if _, err := s.Get(ctx, principal.Principal{}, "ticket", false); !errors.Is(err, apperr.ErrForbidden) {
		t.Errorf("no-scope get: %v", err)
	}
	if n := len(events(t, a, admin, audit.ActionRecordSchemaCreate)); n != 1 {
		t.Errorf("%d create events, want the admin's 1", n)
	}
}

func TestRefused(t *testing.T) {
	ctx := context.Background()
	a := testutil.NewApp(t)
	admin := testutil.Admin(t, a)
	s := a.RecordSchemas
	if _, err := s.Create(ctx, admin, ticket); err != nil {
		t.Fatal(err)
	}
	quote := strings.Replace(ticket, `"ticket"`, `"quote"`, 1)
	big := ticket + "# " + strings.Repeat("x", synth.MaxStoredSchemaBytes) + "\n"
	for _, c := range []struct {
		name string
		err  error
		want error
		msg  string
	}{
		{"too large", create(s, admin, big), apperr.ErrTooLarge, "at most"},
		{"not TOML", create(s, admin, "name = "), apperr.ErrInvalid, ""},
		{"bad field", create(s, admin, strings.NewReplacer(`ref = "TK"`, `value = "person.nope"`, `"ticket"`, `"case"`).Replace(ticket)), apperr.ErrInvalid, "field 1 (reference)"},
		{"built-in name", create(s, admin, quote), apperr.ErrConflict, "is a built-in schema"},
		{"taken", create(s, admin, ticket), apperr.ErrConflict, "already exists"},
		{"no catalogue", create(s, admin, order), apperr.ErrInvalid, `"part" is not a catalogue schema`},
		{"update renames", update(s, admin, "ticket", strings.Replace(ticket, `"ticket"`, `"case"`, 1), 0), apperr.ErrInvalid, "keeps its name"},
		{"update unknown", update(s, admin, "case", strings.Replace(ticket, `"ticket"`, `"case"`, 1), 0), apperr.ErrNotFound, "no record schema"},
		{"update built-in", update(s, admin, "quote", quote, 0), apperr.ErrConflict, "only schemas added through the API"},
		{"update stale", update(s, admin, "ticket", strings.Replace(ticket, "TK", "XX", 1), 7), apperr.ErrConflict, "at version 1, not 7"},
		{"update broken", update(s, admin, "ticket", strings.Replace(ticket, `ref = "TK"`, `ref = 1`, 1), 0), apperr.ErrInvalid, ""},
		{"update too large", update(s, admin, "ticket", big, 0), apperr.ErrTooLarge, ""},
		{"delete unknown", s.Delete(ctx, admin, "case"), apperr.ErrNotFound, ""},
		{"delete built-in", s.Delete(ctx, admin, "quote"), apperr.ErrConflict, "only schemas added through the API"},
	} {
		if !errors.Is(c.err, c.want) || !strings.Contains(c.err.Error(), c.msg) {
			t.Errorf("%s: %v, want %v containing %q", c.name, c.err, c.want, c.msg)
		}
	}
	// Nothing refused was stored or changed.
	if _, err := s.Get(ctx, admin, "order", false); !errors.Is(err, apperr.ErrNotFound) {
		t.Errorf("order was stored: %v", err)
	}
	if d, _ := s.Get(ctx, admin, "ticket", false); d.Version != 1 || d.TOML != ticket {
		t.Errorf("ticket = %+v", d)
	}
	if n := len(events(t, a, admin, audit.ActionRecordSchemaCreate)) + len(events(t, a, admin, audit.ActionRecordSchemaUpdate)); n != 1 {
		t.Errorf("%d events, want ticket's create", n)
	}
}

func create(s *recordschema.Service, p principal.Principal, text string) error {
	_, err := s.Create(context.Background(), p, text)
	return err
}

func update(s *recordschema.Service, p principal.Principal, name, text string, version int) error {
	_, err := s.Update(context.Background(), p, name, text, version)
	return err
}

// A schema may use another's catalogue; a change that would break it, or
// removing the catalogue, is refused and leaves both as they were.
func TestCatalogueDependents(t *testing.T) {
	ctx := context.Background()
	a := testutil.NewApp(t)
	admin := testutil.Admin(t, a)
	s := a.RecordSchemas
	if _, err := s.Create(ctx, admin, part); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Create(ctx, admin, order); err != nil {
		t.Fatal(err)
	}
	rec, err := records(t, a, "order")
	if err != nil || rec["items"].([]any)[0].(map[string]any)["amount"] != "$10.00" {
		t.Fatalf("order = %v, %v", rec, err)
	}
	noPrice := strings.Replace(part, "name = \"unit_price\"", "name = \"price\"", 1)
	if err := update(s, admin, "part", noPrice, 0); !errors.Is(err, apperr.ErrInvalid) || !strings.Contains(err.Error(), "breaks record schema order") {
		t.Errorf("update part: %v", err)
	}
	if err := s.Delete(ctx, admin, "part"); !errors.Is(err, apperr.ErrInvalid) || !strings.Contains(err.Error(), "breaks record schema order") {
		t.Errorf("delete part: %v", err)
	}
	if d, _ := s.Get(ctx, admin, "part", false); d.Version != 1 {
		t.Errorf("part = %+v", d)
	}
	// A price change the order can use is fine, and the order uses it at
	// once.
	if err := update(s, admin, "part", strings.Replace(part, "[5, 5]", "[7, 7]", 1), 1); err != nil {
		t.Fatal(err)
	}
	if rec, _ := records(t, a, "order"); rec["items"].([]any)[0].(map[string]any)["amount"] != "$14.00" {
		t.Errorf("order after the price change = %v", rec)
	}
	// Removed in order, both go.
	if err := s.Delete(ctx, admin, "order"); err != nil {
		t.Fatal(err)
	}
	if err := s.Delete(ctx, admin, "part"); err != nil {
		t.Fatal(err)
	}
}

func TestGetAnySource(t *testing.T) {
	ctx := context.Background()
	a := testutil.NewApp(t)
	admin := testutil.Admin(t, a)
	d, err := a.RecordSchemas.Get(ctx, admin, "quote", false)
	if err != nil || d.Source != synth.SourceBuiltin || d.Version != 0 || len(d.Hash) != 8 || !strings.Contains(d.TOML, `name = "quote"`) || d.UpdatedAt != nil {
		t.Errorf("quote = %+v, %v", d, err)
	}
	if _, err := a.RecordSchemas.Get(ctx, admin, "nope", false); !errors.Is(err, apperr.ErrNotFound) {
		t.Errorf("unknown: %v", err)
	}
	if _, err := a.RecordSchemas.Get(ctx, admin, "quote", true); !errors.Is(err, apperr.ErrNotFound) {
		t.Errorf("a built-in asked for as an API schema: %v", err)
	}
}

// Stored schemas load when the server starts. One that no longer loads (a
// directory file took its name) is listed with its problem and a warning,
// the server starts anyway, and an admin can remove it.
func TestLoadAtStart(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	cfg := app.Config{DBPath: filepath.Join(dir, "x.db"), BlobDir: filepath.Join(dir, "blobs")}
	a, err := app.Open(ctx, nil, cfg)
	if err != nil {
		t.Fatal(err)
	}
	admin := testutil.Admin(t, a)
	for _, f := range []string{ticket, part, order} {
		if _, err := a.RecordSchemas.Create(ctx, admin, f); err != nil {
			t.Fatal(err)
		}
	}
	_ = a.Close()

	a, err = app.Open(ctx, nil, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := records(t, a, "order"); err != nil {
		t.Errorf("order after restart: %v", err)
	}
	_ = a.Close()

	// A directory file now takes "part": the stored part is listed with
	// its problem, and the order uses the file's catalogue instead.
	schemas := filepath.Join(dir, "schemas")
	if err := os.Mkdir(schemas, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(schemas, "part.toml"), []byte(strings.Replace(part, "[5, 5]", "[9, 9]", 1)), 0o600); err != nil {
		t.Fatal(err)
	}
	logs := testutil.NewLogs()
	cfg.RecordSchemasDir = schemas
	a, err = app.Open(ctx, logs, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = a.Close() }()
	var problem *synth.Schema
	all, _ := a.Generate.Schemas(admin)
	for _, sc := range all {
		if sc.Name == "part" && sc.Source == synth.SourceAPI {
			problem = sc
		}
	}
	if problem == nil || !strings.Contains(problem.Problem, "taken by a file schema") {
		t.Errorf("stored part = %+v", problem)
	}
	if rec, err := records(t, a, "order"); err != nil || rec["items"].([]any)[0].(map[string]any)["amount"] != "$18.00" {
		t.Errorf("order = %v, %v", rec, err)
	}
	warned := false
	for _, l := range logs.Lines() {
		if strings.Contains(l, "not loaded") && strings.Contains(l, "part") {
			warned = true
		}
	}
	if !warned {
		t.Errorf("no warning in %v", logs.Lines())
	}
	d, err := a.RecordSchemas.Get(ctx, admin, "part", false)
	if err != nil || d.Source != synth.SourceFile {
		t.Errorf("get part = %+v, %v", d, err)
	}
	// The admin reads the stored one back before removing it.
	stored, err := a.RecordSchemas.Get(ctx, admin, "part", true)
	if err != nil || stored.Source != synth.SourceAPI || stored.TOML != part || !strings.Contains(stored.Problem, "taken by a file schema") {
		t.Errorf("get stored part = %+v, %v", stored, err)
	}
	// A server file is the deployment's: members see it listed, not its text.
	member := testutil.Member(t, a, admin, "alice")
	if _, err := a.RecordSchemas.Get(ctx, member, "part", false); !errors.Is(err, apperr.ErrForbidden) {
		t.Errorf("member reads a server file: %v", err)
	}
	if _, err := a.RecordSchemas.Get(ctx, member, "quote", false); err != nil {
		t.Errorf("member reads a built-in: %v", err)
	}
	if err := a.RecordSchemas.Delete(ctx, admin, "part"); err != nil {
		t.Errorf("removing the stored part: %v", err)
	}
	for _, sc := range mustList(t, a, admin) {
		if sc.Problem != "" {
			t.Errorf("still listed: %+v", sc)
		}
	}
}

func mustList(t *testing.T, a *app.App, p principal.Principal) []*synth.Schema {
	t.Helper()
	all, err := a.Generate.Schemas(p)
	if err != nil {
		t.Fatal(err)
	}
	return all
}

// Changes made at once all land: each is checked against the one before.
func TestConcurrentChanges(t *testing.T) {
	ctx := context.Background()
	a := testutil.NewApp(t)
	admin := testutil.Admin(t, a)
	errs := make(chan error, 8)
	for i := range 8 {
		go func() {
			_, err := a.RecordSchemas.Create(ctx, admin, strings.Replace(ticket, `"ticket"`, fmt.Sprintf(`"ticket-%d"`, i), 1))
			errs <- err
		}()
	}
	for range 8 {
		if err := <-errs; err != nil {
			t.Error(err)
		}
	}
	n := 0
	for _, sc := range mustList(t, a, admin) {
		if sc.Source == synth.SourceAPI {
			n++
		}
	}
	if n != 8 {
		t.Errorf("%d API schemas loaded, want 8", n)
	}
}

func TestStoredLimit(t *testing.T) {
	ctx := context.Background()
	a := testutil.NewApp(t)
	admin := testutil.Admin(t, a)
	for i := range synth.MaxStoredSchemas {
		if _, err := a.RecordSchemas.Create(ctx, admin, strings.Replace(ticket, `"ticket"`, fmt.Sprintf(`"t%d"`, i), 1)); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := a.RecordSchemas.Create(ctx, admin, ticket); !errors.Is(err, apperr.ErrInvalid) || !strings.Contains(err.Error(), "at most") {
		t.Errorf("one more: %v", err)
	}
	if _, err := a.RecordSchemas.Get(ctx, admin, "ticket", false); !errors.Is(err, apperr.ErrNotFound) {
		t.Errorf("the one more was stored: %v", err)
	}
}
