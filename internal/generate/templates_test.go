package generate_test

import (
	"bytes"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/martinsuchenak/exemplar/internal/anonymise/anonymisetest"
	"github.com/martinsuchenak/exemplar/internal/app"
	"github.com/martinsuchenak/exemplar/internal/apperr"
	"github.com/martinsuchenak/exemplar/internal/audit"
	"github.com/martinsuchenak/exemplar/internal/dataset"
	"github.com/martinsuchenak/exemplar/internal/fixture"
	"github.com/martinsuchenak/exemplar/internal/generate"
	"github.com/martinsuchenak/exemplar/internal/ingest"
	"github.com/martinsuchenak/exemplar/internal/person"
	"github.com/martinsuchenak/exemplar/internal/principal"
	"github.com/martinsuchenak/exemplar/internal/synth"
	"github.com/martinsuchenak/exemplar/internal/testutil"
)

// approvedTemplate ingests one real-looking email, approves it and promotes
// it to the global library. It returns the quarantine fixture, its global
// copy, and the registry people the ingest created.
func approvedTemplate(t *testing.T) (a *app.App, admin, alice principal.Principal, quarantined, global fixture.Fixture, ws *dataset.Dataset) {
	t.Helper()
	a = testutil.NewAppWith(t, func(c *app.Config) {
		c.AnonKey = anonymisetest.Key
		c.AnonDetector = anonymisetest.Detector{Names: []string{"Priya", "Tom"}}
	})
	admin = testutil.Admin(t, a)
	alice = testutil.Member(t, a, admin, "alice")
	q, err := a.Datasets.Create(ctx, admin, dataset.CreateInput{Name: "inbox", Scope: dataset.ScopeQuarantine})
	if err != nil {
		t.Fatal(err)
	}
	mail := anonymisetest.Email("Priya Shah", "Tom Reid", "acmeplumbing.com.au", "Leak at the depot",
		"Hi Tom,\r\n\r\nThe leak at the depot is fixed; the invoice follows tomorrow.\r\n\r\nRegards,\r\nPriya Shah")
	res, err := a.Ingest.Ingest(ctx, admin, q.ID, ingest.Request{Batch: "b1"}, []ingest.File{{Name: "a.eml", Data: mail}})
	if err != nil || res.Files[0].Error != "" {
		t.Fatalf("ingest = %+v, %v", res, err)
	}
	f, err := a.Fixtures.Get(ctx, admin, res.Files[0].FixtureID)
	if err != nil {
		t.Fatal(err)
	}
	quarantined = *f
	if _, err := a.Generate.Generate(ctx, admin, mustWorkspace(t, a, admin).ID, generate.Request{Kind: generate.KindEmails, Count: 1, From: []string{f.ID}}); !errors.Is(err, apperr.ErrInvalid) || !strings.Contains(err.Error(), "not approved") {
		t.Fatalf("unapproved template err = %v", err)
	}
	if _, err := a.Fixtures.Review(ctx, admin, f.ID, true); err != nil {
		t.Fatal(err)
	}
	promoted, err := a.Datasets.Promote(ctx, admin, q.ID, dataset.PromoteInput{Name: "inbox"})
	if err != nil || promoted.Copied != 1 {
		t.Fatalf("promote = %+v, %v", promoted, err)
	}
	copies, err := a.Fixtures.List(ctx, alice, promoted.Dataset.ID, fixture.ListFilter{})
	if err != nil || len(copies) != 1 {
		t.Fatalf("global copies = %v, %v", copies, err)
	}
	ws, err = a.Datasets.Create(ctx, alice, dataset.CreateInput{Name: "tests"})
	if err != nil {
		t.Fatal(err)
	}
	return a, admin, alice, quarantined, copies[0], ws
}

func mustWorkspace(t *testing.T, a *app.App, p principal.Principal) *dataset.Dataset {
	t.Helper()
	d, err := a.Datasets.Create(ctx, p, dataset.CreateInput{Name: "scratch"})
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func body(t *testing.T, a *app.App, p principal.Principal, id string) []byte {
	t.Helper()
	rc, _, err := a.Fixtures.Open(ctx, p, id)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rc.Close() }()
	b, _ := io.ReadAll(rc)
	return b
}

func TestGenerateFromApprovedTemplate(t *testing.T) {
	a, admin, alice, quarantined, global, ws := approvedTemplate(t)
	source := string(body(t, a, admin, global.ID))
	templatePeople := map[string]*person.Person{}
	for _, l := range global.People {
		p, err := a.Persons.Get(ctx, alice, l.PersonID)
		if err != nil {
			t.Fatal(err)
		}
		templatePeople[p.ID] = p
	}

	res, err := a.Generate.Generate(ctx, alice, ws.ID, generate.Request{Kind: generate.KindEmails, Count: 4, From: []string{global.ID, " ", global.ID}, Seed: 7})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Fixtures) != 4 || len(res.PersonIDs) != 10 {
		t.Fatalf("generated %d fixtures about %d people", len(res.Fixtures), len(res.PersonIDs))
	}
	distinct := map[string]bool{}
	for _, f := range res.Fixtures {
		if f.Source != fixture.SourceSynthetic || f.Generator != "template:"+global.ID+" seed=7" || f.IngestBatch != "b1" ||
			f.Scenario != "template" || f.ReviewState != fixture.ReviewNotRequired || !strings.HasPrefix(f.Name, "g000007-") || len(f.People) != 2 {
			t.Errorf("fixture = %+v", f)
		}
		roles := map[string]bool{}
		for _, l := range f.People {
			roles[l.Role] = true
			if templatePeople[l.PersonID] != nil {
				t.Errorf("%s still links a template person", f.Name)
			}
		}
		if !roles["from"] || !roles["to"] {
			t.Errorf("roles = %v", f.People)
		}
		out := string(body(t, a, alice, f.ID))
		distinct[out] = true
		if !strings.Contains(out, "The leak at the depot is fixed; the invoice follows tomorrow.") || !strings.Contains(out, "Subject: Leak at the depot") {
			t.Errorf("wording lost:\n%s", out)
		}
		for _, p := range templatePeople {
			for _, v := range []string{p.FirstName, p.LastName, p.Email} {
				if strings.Contains(out, v) {
					t.Errorf("%s still mentions %q:\n%s", f.Name, v, out)
				}
			}
		}
		if strings.Contains(out, "Date: ") != strings.Contains(source, "Date: ") || !strings.Contains(out, "Date: Mon, 6 Jan 2025 09:30:00 +1100") {
			t.Errorf("date changed:\n%s", out)
		}
	}
	if len(distinct) < 3 {
		t.Errorf("only %d distinct emails from 4", len(distinct))
	}

	// The same seed and people give the same emails.
	ws2 := mustWorkspace(t, a, alice)
	again, err := a.Generate.Generate(ctx, alice, ws2.ID, generate.Request{Kind: generate.KindEmails, Count: 4, From: []string{global.ID}, Seed: 7})
	if err != nil {
		t.Fatal(err)
	}
	for i := range again.Fixtures {
		if !bytes.Equal(body(t, a, alice, again.Fixtures[i].ID), body(t, a, alice, res.Fixtures[i].ID)) {
			t.Errorf("email %d differs for the same seed", i)
		}
	}

	events, _ := a.Audit.List(ctx, admin, audit.Filter{Action: audit.ActionFixtureGenerate})
	if len(events) != 2 || events[0].Details["from"] == nil {
		t.Errorf("audit = %+v", events)
	}

	// Purging the ingest batch removes what was generated from it too.
	n, err := a.Fixtures.PurgeBatch(ctx, admin, "b1")
	if err != nil || n != 10 {
		t.Errorf("purge = %d, %v (want quarantine + global + 8 generated)", n, err)
	}
	if left, _ := a.Fixtures.List(ctx, alice, ws.ID, fixture.ListFilter{}); len(left) != 0 {
		t.Errorf("%d generated fixtures survived the purge", len(left))
	}
	_ = quarantined
}

func TestTemplateRules(t *testing.T) {
	a, admin, alice, quarantined, global, ws := approvedTemplate(t)
	upload, err := a.Fixtures.Create(ctx, alice, fixture.CreateInput{DatasetID: ws.ID, Name: "plain.eml", Content: strings.NewReader("Subject: hi\r\n\r\nhi\r\n")})
	if err != nil {
		t.Fatal(err)
	}
	file, err := a.Fixtures.Create(ctx, alice, fixture.CreateInput{DatasetID: ws.ID, Name: "a.pdf", Content: strings.NewReader("%PDF-1.4")})
	if err != nil {
		t.Fatal(err)
	}
	many := make([]string, generate.MaxTemplates+1)
	for i := range many {
		many[i] = string(rune('a'+i%26)) + strings.Repeat("x", i)
	}
	for _, tt := range []struct {
		name  string
		actor principal.Principal
		req   generate.Request
		want  error
		match string
	}{
		{"files", alice, generate.Request{Kind: generate.KindFiles, Count: 1, From: []string{global.ID}}, apperr.ErrInvalid, "only make emails"},
		{"shapes too", alice, generate.Request{Kind: generate.KindEmails, Count: 1, From: []string{global.ID}, Shapes: []synth.Shape{synth.ShapePlain}}, apperr.ErrInvalid, "not both"},
		{"blank ids", alice, generate.Request{Kind: generate.KindEmails, Count: 1, From: []string{" "}}, apperr.ErrInvalid, "1-50"},
		{"too many", alice, generate.Request{Kind: generate.KindEmails, Count: 1, From: many}, apperr.ErrInvalid, "1-50"},
		{"too few people", alice, generate.Request{Kind: generate.KindEmails, Count: 1, From: []string{global.ID}, People: 1}, apperr.ErrInvalid, "at least 2"},
		{"people out of range", alice, generate.Request{Kind: generate.KindEmails, Count: 1, From: []string{global.ID}, People: generate.MaxPeople + 1}, apperr.ErrInvalid, "people"},
		{"not an email", alice, generate.Request{Kind: generate.KindEmails, Count: 1, From: []string{file.ID}}, apperr.ErrInvalid, "not an .eml"},
		{"no people", alice, generate.Request{Kind: generate.KindEmails, Count: 1, From: []string{upload.ID}}, apperr.ErrInvalid, "no linked people"},
		{"unreadable", alice, generate.Request{Kind: generate.KindEmails, Count: 1, From: []string{quarantined.ID}}, apperr.ErrNotFound, ""},
		{"missing", alice, generate.Request{Kind: generate.KindEmails, Count: 1, From: []string{"missing"}}, apperr.ErrNotFound, ""},
	} {
		_, err := a.Generate.Generate(ctx, tt.actor, ws.ID, tt.req)
		if !errors.Is(err, tt.want) || !strings.Contains(err.Error(), tt.match) {
			t.Errorf("%s: err = %v, want %v %q", tt.name, err, tt.want, tt.match)
		}
	}

	// A synthetic email works as a template too, and needs no approval.
	gen, err := a.Generate.Generate(ctx, alice, ws.ID, generate.Request{Kind: generate.KindEmails, Count: 1, Shapes: []synth.Shape{synth.ShapeReply}, Seed: 3})
	if err != nil {
		t.Fatal(err)
	}
	res, err := a.Generate.Generate(ctx, alice, ws.ID, generate.Request{Kind: generate.KindEmails, Count: 2, From: []string{gen.Fixtures[0].ID}, People: 4, Scenario: "regression"})
	if err != nil || len(res.Fixtures) != 2 || res.Fixtures[0].IngestBatch != "" || res.Fixtures[0].Scenario != "regression" {
		t.Fatalf("synthetic template = %+v, %v", res, err)
	}
	if _, err := a.Generate.Generate(ctx, admin, ws.ID, generate.Request{Kind: generate.KindEmails, Count: 1, From: []string{quarantined.ID}}); err != nil {
		t.Errorf("an admin may use the approved quarantine original: %v", err)
	}
}
