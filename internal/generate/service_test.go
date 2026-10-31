package generate_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"math"
	"net/mail"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/martinsuchenak/exemplar/internal/anonymise"
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
	"github.com/martinsuchenak/exemplar/internal/runlimit"
	"github.com/martinsuchenak/exemplar/internal/scrub"
	"github.com/martinsuchenak/exemplar/internal/synth"
	"github.com/martinsuchenak/exemplar/internal/testutil"
)

var ctx = context.Background()

// blockingDetector holds every call until release is closed, so a test can
// hold a run slot open.
type blockingDetector struct {
	started chan struct{}
	release chan struct{}
	once    sync.Once
}

func (d *blockingDetector) Detect(context.Context, string, []string, float64) ([]anonymise.Span, error) {
	d.once.Do(func() { close(d.started) })
	<-d.release
	return nil, nil
}

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
	all, err := a.Generate.Schemas(alice)
	if err != nil {
		t.Fatal(err)
	}
	res, err := a.Generate.Generate(ctx, alice, d.ID, generate.Request{Kind: generate.KindRecords, Count: 2 * len(all), People: 2, Seed: 3})
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
	for _, s := range all {
		if schemas[string(s.Name)] != 2 {
			t.Errorf("schema %s: %d records, want 2", s.Name, schemas[string(s.Name)])
		}
	}
	for _, f := range res.Fixtures {
		if !strings.HasPrefix(f.Generator, generate.GeneratorName+" schema="+f.RecordSchema+"@") || !strings.HasSuffix(f.Generator, " seed=3") {
			t.Errorf("%s generator = %q", f.Name, f.Generator)
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

// A records generation holds the memory its records are built in, so it
// takes a run slot: past the cap the caller is told the server is busy,
// before any record is made. The slots are generation's own, so a long
// anonymise run never makes generation busy.
func TestGenerateRecordsTakeARunSlot(t *testing.T) {
	d := &blockingDetector{started: make(chan struct{}), release: make(chan struct{})}
	a := testutil.NewAppWith(t, func(c *app.Config) {
		c.AnonDetector = d
		c.MaxConcurrentRuns = 1
	})
	admin := testutil.Admin(t, a)
	alice := testutil.Member(t, a, admin, "alice")
	ds, err := a.Datasets.Create(ctx, alice, dataset.CreateInput{Name: "gen"})
	if err != nil {
		t.Fatal(err)
	}
	doc := []anonymise.File{{Name: "a.txt", Data: []byte("text")}}
	done := make(chan error)
	go func() {
		_, err := a.Scrub.Anonymise(ctx, admin, scrub.Request{}, doc)
		done <- err
	}()
	<-d.started
	defer func() {
		close(d.release)
		if err := <-done; err != nil {
			t.Errorf("anonymise: %v", err)
		}
	}()
	// The anonymise run holds the one anonymise slot; generation has its own.
	if _, err := a.Generate.Generate(ctx, alice, ds.ID, generate.Request{Kind: generate.KindRecords, Count: 1, People: 2}); err != nil {
		t.Errorf("records while an anonymise run holds its slot: %v", err)
	}

	// A generation holding generation's one slot makes the next busy.
	src := &blockingSource{started: make(chan struct{}), release: make(chan struct{})}
	g := generate.NewService(nil, nil, nil, nil, nil, src, runlimit.New(1))
	req := generate.Request{Kind: generate.KindRecords, Count: 1, People: 2, Seed: 1}
	first := make(chan error)
	go func() {
		_, err := g.Records(ctx, principal.Principal{}, req)
		first <- err
	}()
	<-src.started
	if _, err := g.Records(ctx, principal.Principal{}, req); !errors.Is(err, apperr.ErrBusy) {
		t.Errorf("records over the run cap err = %v, want busy", err)
	}
	close(src.release)
	if err := <-first; err != nil {
		t.Errorf("first generation: %v", err)
	}
	if _, err := g.Records(ctx, principal.Principal{}, req); err != nil {
		t.Errorf("records once the slot is free: %v", err)
	}
}

// blockingSource holds the first generation that asks for the schemas
// until release is closed, so a test can keep a generation's run slot.
type blockingSource struct {
	started chan struct{}
	release chan struct{}
	once    sync.Once
}

func (b *blockingSource) Current() *synth.SchemaSet {
	b.once.Do(func() {
		close(b.started)
		<-b.release
	})
	return synth.BuiltinSchemas()
}

// A cancelled caller stops the build loop instead of generating on: the
// ctx is checked once per item.
func TestGenerateStopsWhenCancelled(t *testing.T) {
	a, _, alice, d := setup(t)
	cctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := a.Generate.Generate(cctx, alice, d.ID, generate.Request{Kind: generate.KindRecords, Count: 500, People: 2, Seed: 11})
	if err == nil {
		t.Error("a cancelled generate succeeded")
	}
	// Nothing was made: the build loop checks the ctx once per item, so a
	// caller that goes away stops the work instead of paying for it.
	fs, err := a.Fixtures.List(ctx, alice, d.ID, fixture.ListFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(fs) != 0 {
		t.Errorf("cancelled generate left %d fixtures", len(fs))
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
	people := []synth.RecordSchema{synth.SchemaContact, synth.SchemaCompany, synth.SchemaProject, synth.SchemaOpportunity, synth.SchemaAddress}
	res, err := a.Generate.Generate(ctx, alice, d.ID, generate.Request{Kind: generate.KindRecords, Count: 30, People: 20, Seed: 2026, Schemas: people})
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

// TestQuotesUseTheProductCatalogue checks records that refer to each other
// agree: every quote line is a product record, with its SKU, name and price,
// and every total adds up to the cent.
func TestQuotesUseTheProductCatalogue(t *testing.T) {
	a, _, alice, d := setup(t)
	res, err := a.Generate.Generate(ctx, alice, d.ID, generate.Request{
		Kind: generate.KindRecords, Count: 48, People: 4, Seed: 9, Profile: synth.ProfileManufacturing,
		Schemas: []synth.RecordSchema{synth.SchemaProduct, synth.SchemaQuote},
	})
	if err != nil {
		t.Fatal(err)
	}
	type line struct {
		SKU       string  `json:"sku"`
		Name      string  `json:"name"`
		Qty       int     `json:"qty"`
		UnitPrice float64 `json:"unit_price"`
		Amount    float64 `json:"amount"`
	}
	type doc struct {
		line
		Items    []line  `json:"items"`
		Subtotal float64 `json:"subtotal"`
		GST      float64 `json:"gst"`
		Total    float64 `json:"total"`
	}
	read := func(id string) doc {
		r, _, err := a.Fixtures.Open(ctx, alice, id)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = r.Close() }()
		var v doc
		if err := json.NewDecoder(r).Decode(&v); err != nil {
			t.Fatal(err)
		}
		return v
	}
	cents := func(v float64) int64 { return int64(math.Round(v * 100)) }
	products := map[string]line{}
	var quotes []doc
	for _, f := range res.Fixtures {
		switch f.RecordSchema {
		case "product":
			p := read(f.ID)
			if len(f.People) != 0 {
				t.Errorf("product %s links people: %v", f.Name, f.People)
			}
			products[p.SKU] = p.line
		case "quote":
			quotes = append(quotes, read(f.ID))
			if len(f.People) != 1 || f.People[0].Role != "contact" {
				t.Errorf("quote %s links %v", f.Name, f.People)
			}
		}
	}
	if len(products) != 24 || len(quotes) != 24 {
		t.Fatalf("%d products, %d quotes; want the whole catalogue of 24 and 24 quotes", len(products), len(quotes))
	}
	for i, q := range quotes {
		if len(q.Items) < 1 || len(q.Items) > 6 {
			t.Errorf("quote %d has %d lines", i, len(q.Items))
		}
		var sum int64
		seen := map[string]bool{}
		for _, l := range q.Items {
			p, ok := products[l.SKU]
			if !ok || p.Name != l.Name || cents(p.UnitPrice) != cents(l.UnitPrice) {
				t.Errorf("quote %d line %+v is not the catalogue's product %+v", i, l, p)
			}
			if seen[l.SKU] {
				t.Errorf("quote %d lists %s twice", i, l.SKU)
			}
			seen[l.SKU] = true
			if cents(l.Amount) != cents(l.UnitPrice)*int64(l.Qty) {
				t.Errorf("quote %d line %s: %v x %d != %v", i, l.SKU, l.UnitPrice, l.Qty, l.Amount)
			}
			sum += cents(l.Amount)
		}
		gst := int64(math.Round(float64(sum) / 10))
		if cents(q.Subtotal) != sum || cents(q.GST) != gst || cents(q.Total) != sum+gst {
			t.Errorf("quote %d: subtotal %v, gst %v, total %v; lines add to %d cents", i, q.Subtotal, q.GST, q.Total, sum)
		}
	}

	// The catalogue is the same in another request with another seed.
	again, err := a.Generate.Generate(ctx, alice, d.ID, generate.Request{
		Kind: generate.KindRecords, Count: 3, People: 1, Seed: 10, Profile: synth.ProfileManufacturing,
		Schemas: []synth.RecordSchema{synth.SchemaProduct},
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range again.Fixtures {
		p := read(f.ID)
		if products[p.SKU] != p.line {
			t.Errorf("product %s differs between requests: %+v vs %+v", p.SKU, p.line, products[p.SKU])
		}
	}
}

// TestRecordsForPeopleWithoutACompany uses the people ingest makes for
// senders at a public mail provider: they have no company. A quote or
// project about one is named after them, its company values are empty
// rather than invented, and company records, which need a company, are
// refused as invalid.
func TestRecordsForPeopleWithoutACompany(t *testing.T) {
	a := testutil.NewAppWith(t, func(c *app.Config) {
		c.AnonKey = anonymisetest.Key
		c.AnonDetector = anonymisetest.Detector{}
	})
	admin := testutil.Admin(t, a)
	q, err := a.Datasets.Create(ctx, admin, dataset.CreateInput{Name: "inbox", Scope: dataset.ScopeQuarantine})
	if err != nil {
		t.Fatal(err)
	}
	mail := []ingest.File{{Name: "a.eml", Data: anonymisetest.Email("Jo Bloggs", "Sam Smith", "gmail.com", "Hello", "Hi Sam")}}
	if _, err := a.Ingest.Ingest(ctx, admin, q.ID, ingest.Request{Batch: "b1"}, mail); err != nil {
		t.Fatal(err)
	}
	people, _ := a.Persons.List(ctx, admin, person.ListFilter{})
	if len(people) != 2 || people[0].Company != "" || people[1].Company != "" {
		t.Fatalf("registry = %+v; want the two senders, without companies", people)
	}

	d, err := a.Datasets.Create(ctx, admin, dataset.CreateInput{Name: "gen"})
	if err != nil {
		t.Fatal(err)
	}
	res, err := a.Generate.Generate(ctx, admin, d.ID, generate.Request{
		Kind: generate.KindRecords, Count: 2, People: 2, Seed: 5,
		Schemas: []synth.RecordSchema{synth.SchemaQuote, synth.SchemaProject},
	})
	if err != nil {
		t.Fatal(err)
	}
	names := map[string]string{}
	for _, p := range people {
		names[p.ID] = p.FirstName + " " + p.LastName
	}
	for _, f := range res.Fixtures {
		r, _, err := a.Fixtures.Open(ctx, admin, f.ID)
		if err != nil {
			t.Fatal(err)
		}
		var doc map[string]any
		_ = json.NewDecoder(r).Decode(&doc)
		_ = r.Close()
		who := names[f.People[0].PersonID]
		if !strings.HasSuffix(doc["name"].(string), " - "+who) || doc["company"] != "" {
			t.Errorf("%s: name %q, company %q; want it named after %s, with no company", f.RecordSchema, doc["name"], doc["company"], who)
		}
	}

	_, err = a.Generate.Generate(ctx, admin, d.ID, generate.Request{
		Kind: generate.KindRecords, Count: 1, People: 2, Seed: 6, Schemas: []synth.RecordSchema{synth.SchemaCompany},
	})
	if !errors.Is(err, apperr.ErrInvalid) || !strings.Contains(err.Error(), "work for a company") {
		t.Errorf("company records about individuals err = %v", err)
	}
}

// accountSchema is a schema file a request sends: a company's record with
// its contacts listed, and an owner picked from a list the request gives.
const accountSchema = `name = "account"
step = "company"
title = "{companyName}"
request_lists = ["owners"]

[[fields]]
name = "companyName"
value = "company.name"

[[fields]]
name = "owner"
pick = "@owners"

[[fields]]
name = "contacts"
list = { each = "company.contacts" }

  [[fields.fields]]
  name = "email"
  value = "person.email"
`

// Schema files and lists sent with a request make records there and
// nowhere else, and their provenance names the file's hash.
func TestGenerateFromRequestSchemas(t *testing.T) {
	a, _, alice, d := setup(t)
	owners := map[string][]any{"owners": {map[string]any{"id": 7.0, "name": "Sam"}}}
	res, err := a.Generate.Generate(ctx, alice, d.ID, generate.Request{
		Kind: generate.KindRecords, Count: 2, People: 6, Seed: 5, SchemaFiles: []string{accountSchema}, Lists: owners,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Fixtures) != 2 {
		t.Fatalf("%d fixtures", len(res.Fixtures))
	}
	for _, f := range res.Fixtures {
		if f.RecordSchema != "account" || !strings.Contains(f.Generator, "schema=account@") {
			t.Errorf("fixture = %+v", f)
		}
		rc, _, err := a.Fixtures.Open(ctx, alice, f.ID)
		if err != nil {
			t.Fatal(err)
		}
		data, _ := io.ReadAll(rc)
		_ = rc.Close()
		var rec map[string]any
		if err := json.Unmarshal(data, &rec); err != nil || rec["owner"].(map[string]any)["name"] != "Sam" || len(rec["contacts"].([]any)) == 0 {
			t.Errorf("record = %s, %v", data, err)
		}
	}
	if schemas, _ := a.Generate.Schemas(alice); len(schemas) != len(synth.BuiltinSchemas().Names()) {
		t.Error("a request's schema stayed on the server")
	}

	for _, c := range []struct {
		req  generate.Request
		want string
	}{
		{generate.Request{Kind: generate.KindRecords, Count: 1, SchemaFiles: []string{accountSchema}}, `needs list "owners"`},
		{generate.Request{Kind: generate.KindRecords, Count: 1, SchemaFiles: []string{"name = "}}, "schema file 1"},
		{generate.Request{Kind: generate.KindRecords, Count: 1, Schemas: []synth.RecordSchema{"contact"}, Lists: map[string][]any{"owners": {"x"}}}, "no schema in the request picks from it"},
		{generate.Request{Kind: generate.KindFiles, Count: 1, Lists: owners}, "for records"},
		{generate.Request{Kind: generate.KindEmails, Count: 1, SchemaFiles: []string{accountSchema}}, "for records"},
	} {
		if _, err := a.Generate.Generate(ctx, alice, d.ID, c.req); !errors.Is(err, apperr.ErrInvalid) || !strings.Contains(err.Error(), c.want) {
			t.Errorf("err = %v, want %q", err, c.want)
		}
	}
}

// Local records need no database: people are made from the seed, a few to
// a company, and the same seed gives the same records.
func TestLocalRecords(t *testing.T) {
	g, err := app.LocalGenerator("")
	if err != nil {
		t.Fatal(err)
	}
	req := generate.Request{Kind: generate.KindRecords, Count: 4, People: 6, Seed: 11, SchemaFiles: []string{accountSchema},
		Lists: map[string][]any{"owners": {"Sam", "Kim"}}}
	res, err := g.Records(ctx, principal.Principal{}, req)
	if err != nil {
		t.Fatal(err)
	}
	if res.Seed != 11 || len(res.Records) != 4 {
		t.Fatalf("result = %+v", res)
	}
	var rec map[string]any
	if err := json.Unmarshal(res.Records[0], &rec); err != nil || rec["schema"] != "account" || len(rec["contacts"].([]any)) == 0 {
		t.Errorf("record = %s, %v", res.Records[0], err)
	}
	again, _ := g.Records(ctx, principal.Principal{}, req)
	for i := range res.Records {
		if string(res.Records[i]) != string(again.Records[i]) {
			t.Errorf("record %d differs for the same seed", i)
		}
	}
	builtin, err := g.Records(ctx, principal.Principal{}, generate.Request{Kind: generate.KindRecords, Count: 3, Schemas: []synth.RecordSchema{"contact"}})
	if err != nil || builtin.Seed == 0 || len(builtin.Records) != 3 {
		t.Errorf("built-in = %+v, %v", builtin, err)
	}
	if _, err := g.Records(ctx, principal.Principal{}, generate.Request{Kind: generate.KindFiles, Count: 1}); !errors.Is(err, apperr.ErrInvalid) {
		t.Errorf("files err = %v", err)
	}
	if _, err := g.Records(ctx, principal.Principal{}, generate.Request{Kind: generate.KindRecords, Count: 0}); !errors.Is(err, apperr.ErrInvalid) {
		t.Errorf("count err = %v", err)
	}
	bad := t.TempDir()
	if err := os.WriteFile(filepath.Join(bad, "x.toml"), []byte("name = "), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := app.LocalGenerator(bad); err == nil {
		t.Error("a bad schema directory loaded")
	}
}

// A request's records may come to at most a set size, checked as they are
// made, so a schema of large records cannot fill memory.
func TestRecordsSizeCap(t *testing.T) {
	defer generate.SetMaxRecordsBytes(4 << 10)()
	g, _ := app.LocalGenerator("")
	big := `name = "big"
title = "x"

[[fields]]
name = "notes"
list = { count = [20, 20] }

  [[fields.fields]]
  name = "text"
  text = { paragraph = [2, 0] }
`
	_, err := g.Records(ctx, principal.Principal{}, generate.Request{Kind: generate.KindRecords, Count: 50, SchemaFiles: []string{big}})
	if !errors.Is(err, apperr.ErrTooLarge) || !strings.Contains(err.Error(), "over 0 MiB after") {
		t.Errorf("err = %v", err)
	}
	if _, err := g.Records(ctx, principal.Principal{}, generate.Request{Kind: generate.KindRecords, Count: 1, Schemas: []synth.RecordSchema{"contact"}}); err != nil {
		t.Errorf("a small request: %v", err)
	}
}

// Lists decoded from JSON keep their numbers' digits.
func TestListsKeepNumbers(t *testing.T) {
	var req generate.Request
	if err := json.Unmarshal([]byte(`{"lists": {"owners": [{"id": 12345678901234567}, 3.5, "x"]}}`), &req); err != nil {
		t.Fatal(err)
	}
	owner := req.Lists["owners"][0].(map[string]any)["id"]
	if owner != json.Number("12345678901234567") || req.Lists["owners"][1] != json.Number("3.5") {
		t.Errorf("lists = %#v", req.Lists)
	}
	if err := json.Unmarshal([]byte(`{"lists": ["x"]}`), &req); err == nil {
		t.Error("a list that is not an object decoded")
	}
}

// A request that names no schemas makes every schema whose request lists
// it gives, so a server schema that needs a target system's lists does not
// fail requests that are not about it.
func TestDefaultSchemasSkipThoseNeedingLists(t *testing.T) {
	a := testutil.NewApp(t)
	admin := testutil.Admin(t, a)
	needs := "name = \"seeded\"\ntitle = \"x\"\nrequest_lists = [\"owners\"]\n\n[[fields]]\nname = \"owner\"\npick = \"@owners\"\n"
	if _, err := a.RecordSchemas.Create(ctx, admin, needs); err != nil {
		t.Fatal(err)
	}
	schemaOf := func(req generate.Request) map[string]int {
		t.Helper()
		res, err := a.Generate.Records(ctx, principal.Principal{}, req)
		if err != nil {
			t.Fatal(err)
		}
		n := map[string]int{}
		for _, r := range res.Records {
			var rec map[string]any
			if err := json.Unmarshal(r, &rec); err != nil {
				t.Fatal(err)
			}
			n[rec["schema"].(string)]++
		}
		return n
	}
	count := len(synth.BuiltinSchemas().Names()) + 1
	if got := schemaOf(generate.Request{Kind: generate.KindRecords, Count: count, Seed: 3}); got["seeded"] != 0 || len(got) != count-1 {
		t.Errorf("without lists = %v", got)
	}
	if got := schemaOf(generate.Request{Kind: generate.KindRecords, Count: count, Seed: 3, Lists: generate.Lists{"owners": {"Sam"}}}); got["seeded"] != 1 || len(got) != count {
		t.Errorf("with lists = %v", got)
	}
	if _, err := a.Generate.Records(ctx, principal.Principal{}, generate.Request{Kind: generate.KindRecords, Count: 1, Schemas: []synth.RecordSchema{"seeded"}}); !errors.Is(err, apperr.ErrInvalid) {
		t.Errorf("named without its list: %v", err)
	}
	// A request giving one of a schema's lists is meant for it: it is told
	// which list is missing, not that the one it gave is unused.
	both := "name = \"paired\"\ntitle = \"x\"\nrequest_lists = [\"owners\", \"users\"]\n\n[[fields]]\nname = \"owner\"\npick = \"@owners\"\n\n[[fields]]\nname = \"user\"\npick = \"@users\"\n"
	if _, err := a.RecordSchemas.Create(ctx, admin, both); err != nil {
		t.Fatal(err)
	}
	_, err := a.Generate.Records(ctx, principal.Principal{}, generate.Request{Kind: generate.KindRecords, Count: count, Lists: generate.Lists{"users": {"Kim"}}})
	if !errors.Is(err, apperr.ErrInvalid) || !strings.Contains(err.Error(), `paired needs list "owners"`) {
		t.Errorf("one of two lists: %v", err)
	}
}

// A request that names no schemas, where every schema needs lists it does
// not give, is refused rather than left with none to make.
func TestNoDefaultSchemaLeft(t *testing.T) {
	dir := t.TempDir()
	for _, n := range synth.BuiltinSchemas().Names() {
		body := "name = \"" + string(n) + "\"\ntitle = \"x\"\nrequest_lists = [\"owners\"]\n\n[[fields]]\nname = \"owner\"\npick = \"@owners\"\n"
		if err := os.WriteFile(filepath.Join(dir, string(n)+".toml"), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	g, err := app.LocalGenerator(dir)
	if err != nil {
		t.Fatal(err)
	}
	_, err = g.Records(ctx, principal.Principal{}, generate.Request{Kind: generate.KindRecords, Count: 2})
	if !errors.Is(err, apperr.ErrInvalid) || !strings.Contains(err.Error(), "every record schema needs lists") {
		t.Errorf("err = %v", err)
	}
	if _, err := g.Records(ctx, principal.Principal{}, generate.Request{Kind: generate.KindRecords, Count: 2, Lists: generate.Lists{"owners": {"Sam"}}}); err != nil {
		t.Errorf("with the list: %v", err)
	}
}
