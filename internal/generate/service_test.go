package generate_test

import (
	"context"
	"errors"
	"io"
	"net/mail"
	"strings"
	"testing"

	"github.com/martinsuchenak/exemplar/internal/app"
	"github.com/martinsuchenak/exemplar/internal/apperr"
	"github.com/martinsuchenak/exemplar/internal/audit"
	"github.com/martinsuchenak/exemplar/internal/dataset"
	"github.com/martinsuchenak/exemplar/internal/fixture"
	"github.com/martinsuchenak/exemplar/internal/generate"
	"github.com/martinsuchenak/exemplar/internal/person"
	"github.com/martinsuchenak/exemplar/internal/principal"
	"github.com/martinsuchenak/exemplar/internal/synth"
	"github.com/martinsuchenak/exemplar/internal/testutil"
)

var ctx = context.Background()

func setup(t *testing.T) (*app.App, principal.Principal, principal.Principal, *dataset.Dataset) {
	t.Helper()
	a := testutil.NewApp(t)
	admin := testutil.Admin(t, a)
	alice := testutil.Member(t, a, admin, "alice")
	d, err := a.Datasets.Create(ctx, alice, dataset.CreateInput{Name: "gen"})
	if err != nil {
		t.Fatal(err)
	}
	return a, admin, alice, d
}

func TestGenerateFiles(t *testing.T) {
	a, admin, alice, d := setup(t)
	res, err := a.Generate.Generate(ctx, alice, d.ID, generate.Request{
		Kind: generate.KindFiles, Count: 4, Formats: []string{"pdf", "csv"}, People: 3, Seed: 123, Scenario: "smoke", Tags: []string{"gen"},
	})
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	if res.Seed != 123 || len(res.Fixtures) != 4 || len(res.PersonIDs) != 3 {
		t.Fatalf("result = seed %d, %d fixtures, %d people", res.Seed, len(res.Fixtures), len(res.PersonIDs))
	}
	formats := map[string]int{}
	for _, f := range res.Fixtures {
		formats[f.Format]++
		if f.Source != fixture.SourceSynthetic || !strings.Contains(f.Generator, "seed=123") || f.Scenario != "smoke" || !strings.HasPrefix(f.Name, "g00007b-") {
			t.Errorf("fixture = %+v", f)
		}
		if len(f.People) == 0 || f.People[0].Role != "author" && f.People[0].Role != "mentioned" {
			t.Errorf("links = %+v", f.People)
		}
	}
	if formats["pdf"] != 2 || formats["csv"] != 2 {
		t.Errorf("formats = %v, want 2 of each", formats)
	}
	// Fixtures mentioning a person can be found by that person.
	byPerson, err := a.Fixtures.List(ctx, alice, d.ID, fixture.ListFilter{PersonID: res.PersonIDs[0]})
	if err != nil || len(byPerson) == 0 {
		t.Errorf("person filter = %d, %v", len(byPerson), err)
	}
	events, _ := a.Audit.List(ctx, admin, audit.Filter{Action: audit.ActionFixtureGenerate})
	if len(events) != 1 || events[0].TargetID != d.ID || events[0].Details["fixtures"] != float64(4) {
		t.Errorf("audit = %+v", events)
	}
}

func TestGenerateEmailsAllShapes(t *testing.T) {
	a, _, alice, d := setup(t)
	res, err := a.Generate.Generate(ctx, alice, d.ID, generate.Request{Kind: generate.KindEmails, Count: len(synth.Shapes), Seed: 7, People: 4})
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	// A thread yields several messages, so there are more fixtures than requests.
	if len(res.Fixtures) <= len(synth.Shapes) {
		t.Errorf("fixtures = %d, want more than %d", len(res.Fixtures), len(synth.Shapes))
	}
	shapes := map[string]bool{}
	for _, f := range res.Fixtures {
		shapes[f.Scenario] = true
		if f.Kind != fixture.KindEmail || f.Format != "eml" {
			t.Errorf("fixture = %+v", f)
		}
		r, _, err := a.Fixtures.Open(ctx, alice, f.ID)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := mail.ReadMessage(r); err != nil {
			t.Errorf("%s is not a valid message: %v", f.Name, err)
		}
		_ = r.Close()
	}
	for _, s := range synth.Shapes {
		if !shapes[string(s)] {
			t.Errorf("no email with shape %s", s)
		}
	}

	tagged, err := a.Generate.Generate(ctx, alice, d.ID, generate.Request{Kind: generate.KindEmails, Count: 1, Shapes: []synth.Shape{synth.ShapeReply}, Scenario: "regression-42", Seed: 8})
	if err != nil {
		t.Fatal(err)
	}
	f := tagged.Fixtures[0]
	if f.Scenario != "regression-42" || len(f.Tags) != 1 || f.Tags[0] != "shape-reply" {
		t.Errorf("scenario/tags = %q %v", f.Scenario, f.Tags)
	}
	roles := map[string]bool{}
	for _, l := range f.People {
		roles[l.Role] = true
	}
	if !roles["from"] || !roles["to"] {
		t.Errorf("email links = %+v", f.People)
	}
}

func TestGenerateRecordsShareThePeople(t *testing.T) {
	a, _, alice, d := setup(t)
	res, err := a.Generate.Generate(ctx, alice, d.ID, generate.Request{Kind: generate.KindRecords, Count: 10, People: 2, Seed: 3})
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	schemas := map[string]int{}
	ids := map[string]bool{}
	for _, id := range res.PersonIDs {
		ids[id] = true
	}
	for _, f := range res.Fixtures {
		schemas[f.RecordSchema]++
		if f.Kind != fixture.KindRecord || f.Format != "json" || f.MimeType != "application/json" {
			t.Errorf("fixture = %+v", f)
		}
		for _, l := range f.People {
			if !ids[l.PersonID] {
				t.Errorf("%s links to %s, not one of the request's people", f.Name, l.PersonID)
			}
		}
	}
	for _, s := range synth.Schemas {
		if schemas[string(s)] != 2 {
			t.Errorf("schema %s: %d records, want 2", s, schemas[string(s)])
		}
	}
	contacts, _ := a.Fixtures.List(ctx, alice, d.ID, fixture.ListFilter{RecordSchema: "contact"})
	if len(contacts) != 2 {
		t.Errorf("contact filter = %d", len(contacts))
	}
	r, _, _ := a.Fixtures.Open(ctx, alice, contacts[0].ID)
	body, _ := io.ReadAll(r)
	_ = r.Close()
	p, err := a.Persons.Get(ctx, alice, contacts[0].People[0].PersonID)
	if err != nil || !strings.Contains(string(body), p.Email) {
		t.Errorf("contact record does not match the registry person: %s / %+v", body, p)
	}
}

func TestGenerateReusesRegistryPeople(t *testing.T) {
	a, admin, alice, d := setup(t)
	existing, _, err := a.Persons.Generate(ctx, admin, person.GenerateInput{Count: 5, Seed: 1})
	if err != nil {
		t.Fatal(err)
	}
	res, err := a.Generate.Generate(ctx, alice, d.ID, generate.Request{Kind: generate.KindRecords, Count: 1, Schemas: []synth.RecordSchema{synth.SchemaContact}, People: 2})
	if err != nil {
		t.Fatal(err)
	}
	known := map[string]bool{}
	for _, p := range existing {
		known[p.ID] = true
	}
	for _, id := range res.PersonIDs {
		if !known[id] {
			t.Errorf("person %s was created although the registry had enough", id)
		}
	}
	if res.Seed == 0 {
		t.Error("random seed not reported")
	}
}

func TestGenerateRules(t *testing.T) {
	a, admin, alice, d := setup(t)
	bob := testutil.Member(t, a, admin, "bob")
	ro := alice
	ro.Scopes = []principal.Scope{principal.ScopeRead}
	ok := generate.Request{Kind: generate.KindFiles, Count: 1}

	tests := []struct {
		name  string
		actor principal.Principal
		ds    string
		req   generate.Request
		want  error
	}{
		{"read-only key", ro, d.ID, ok, apperr.ErrForbidden},
		{"someone else's workspace", bob, d.ID, ok, apperr.ErrNotFound},
		{"missing dataset", alice, "missing", ok, apperr.ErrNotFound},
		{"no kind", alice, d.ID, generate.Request{Count: 1}, apperr.ErrInvalid},
		{"zero count", alice, d.ID, generate.Request{Kind: generate.KindFiles}, apperr.ErrInvalid},
		{"too many", alice, d.ID, generate.Request{Kind: generate.KindFiles, Count: generate.MaxCount + 1}, apperr.ErrInvalid},
		{"bad format", alice, d.ID, generate.Request{Kind: generate.KindFiles, Count: 1, Formats: []string{"exe"}}, apperr.ErrInvalid},
		{"bad shape", alice, d.ID, generate.Request{Kind: generate.KindEmails, Count: 1, Shapes: []synth.Shape{"fax"}}, apperr.ErrInvalid},
		{"bad schema", alice, d.ID, generate.Request{Kind: generate.KindRecords, Count: 1, Schemas: []synth.RecordSchema{"invoice"}}, apperr.ErrInvalid},
		{"bad profile", alice, d.ID, generate.Request{Kind: generate.KindFiles, Count: 1, Profile: "legal"}, apperr.ErrInvalid},
		{"one person emails", alice, d.ID, generate.Request{Kind: generate.KindEmails, Count: 1, People: 1}, apperr.ErrInvalid},
		{"too many people", alice, d.ID, generate.Request{Kind: generate.KindFiles, Count: 1, People: generate.MaxPeople + 1}, apperr.ErrInvalid},
	}
	for _, tt := range tests {
		if _, err := a.Generate.Generate(ctx, tt.actor, tt.ds, tt.req); !errors.Is(err, tt.want) {
			t.Errorf("%s: err = %v, want %v", tt.name, err, tt.want)
		}
	}

	if _, err := a.Datasets.Freeze(ctx, alice, d.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Generate.Generate(ctx, alice, d.ID, ok); !errors.Is(err, apperr.ErrConflict) {
		t.Errorf("frozen dataset err = %v, want ErrConflict", err)
	}
}

func TestSameSeedSamePeopleSameContent(t *testing.T) {
	a, _, alice, d := setup(t)
	req := generate.Request{Kind: generate.KindEmails, Count: 3, People: 2, Seed: 55}
	first, err := a.Generate.Generate(ctx, alice, d.ID, req)
	if err != nil {
		t.Fatal(err)
	}
	// The first run created the two people; the second reuses exactly them.
	other, err := a.Datasets.Create(ctx, alice, dataset.CreateInput{Name: "again"})
	if err != nil {
		t.Fatal(err)
	}
	second, err := a.Generate.Generate(ctx, alice, other.ID, req)
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Fixtures) != len(second.Fixtures) {
		t.Fatalf("runs made %d and %d fixtures", len(first.Fixtures), len(second.Fixtures))
	}
	for i := range first.Fixtures {
		f, g := first.Fixtures[i], second.Fixtures[i]
		if f.Name != g.Name || f.BlobHash != g.BlobHash {
			t.Errorf("fixture %d differs: %s/%s vs %s/%s", i, f.Name, f.BlobHash[:8], g.Name, g.BlobHash[:8])
		}
	}

	// Running the same seed into the same dataset clashes on names.
	_, err = a.Generate.Generate(ctx, alice, d.ID, req)
	if !errors.Is(err, apperr.ErrConflict) || !strings.Contains(err.Error(), "created 0 of") {
		t.Errorf("rerun into the same dataset err = %v, want a name conflict", err)
	}
}

func TestRecordsSpreadAcrossPeople(t *testing.T) {
	a, _, alice, d := setup(t)
	res, err := a.Generate.Generate(ctx, alice, d.ID, generate.Request{Kind: generate.KindRecords, Count: 30, People: 20, Seed: 2026})
	if err != nil {
		t.Fatal(err)
	}
	blobs := map[string]string{}
	linked := map[string]bool{}
	perSchema := map[string]map[string]bool{} // schema → first linked person (or company record person set)
	for _, f := range res.Fixtures {
		if other, dup := blobs[f.BlobHash]; dup {
			t.Errorf("%s has the same content as %s", f.Name, other)
		}
		blobs[f.BlobHash] = f.Name
		if perSchema[f.RecordSchema] == nil {
			perSchema[f.RecordSchema] = map[string]bool{}
		}
		key := f.People[0].PersonID
		if perSchema[f.RecordSchema][key] {
			t.Errorf("%s repeats a %s about the same person", f.Name, f.RecordSchema)
		}
		perSchema[f.RecordSchema][key] = true
		for _, l := range f.People {
			linked[l.PersonID] = true
		}
	}
	if len(linked) != 20 {
		t.Errorf("records link %d of the 20 people", len(linked))
	}
	for schema, people := range perSchema {
		if len(people) != 6 {
			t.Errorf("%s records are about %d different people or companies, want 6", schema, len(people))
		}
	}
}
