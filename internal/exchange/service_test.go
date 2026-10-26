package exchange_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/martinsuchenak/exemplar/internal/anonymise/anonymisetest"
	"github.com/martinsuchenak/exemplar/internal/app"
	"github.com/martinsuchenak/exemplar/internal/apperr"
	"github.com/martinsuchenak/exemplar/internal/archive"
	"github.com/martinsuchenak/exemplar/internal/audit"
	"github.com/martinsuchenak/exemplar/internal/dataset"
	"github.com/martinsuchenak/exemplar/internal/exchange"
	"github.com/martinsuchenak/exemplar/internal/fixture"
	"github.com/martinsuchenak/exemplar/internal/generate"
	"github.com/martinsuchenak/exemplar/internal/ingest"
	"github.com/martinsuchenak/exemplar/internal/person"
	"github.com/martinsuchenak/exemplar/internal/principal"
	"github.com/martinsuchenak/exemplar/internal/testutil"
)

var ctx = context.Background()

// source is an instance with a workspace of generated content, a frozen
// global dataset holding an approved real-derived email, and the quarantine
// it came from, which also holds an unapproved one.
type source struct {
	a                      *app.App
	admin, alice           principal.Principal
	ws, global, quarantine *dataset.Dataset
	approved               fixture.Fixture
}

func newSource(t *testing.T) *source {
	t.Helper()
	a := testutil.NewAppWith(t, func(c *app.Config) {
		c.AnonKey = anonymisetest.Key
		c.AnonDetector = anonymisetest.Detector{Names: []string{"Priya", "Tom"}}
	})
	s := &source{a: a, admin: testutil.Admin(t, a)}
	s.alice = testutil.Member(t, a, s.admin, "alice")
	var err error
	if s.ws, err = a.Datasets.Create(ctx, s.alice, dataset.CreateInput{Name: "work", Tags: []string{"t"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Generate.Generate(ctx, s.alice, s.ws.ID, generate.Request{Kind: generate.KindEmails, Count: 3, Seed: 11, People: 4}); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Generate.Generate(ctx, s.alice, s.ws.ID, generate.Request{Kind: generate.KindRecords, Count: 2, Seed: 11, People: 4}); err != nil {
		t.Fatal(err)
	}
	if s.quarantine, err = a.Datasets.Create(ctx, s.admin, dataset.CreateInput{Name: "inbox", Scope: dataset.ScopeQuarantine}); err != nil {
		t.Fatal(err)
	}
	files := []ingest.File{
		{Name: "a.eml", Data: anonymisetest.Email("Priya Shah", "Tom Reid", "acme.com.au", "Quote", "Hi Tom, Priya")},
		{Name: "b.eml", Data: anonymisetest.Email("Tom Reid", "Priya Shah", "acme.com.au", "Re Quote", "Thanks Priya, Tom")},
	}
	res, err := a.Ingest.Ingest(ctx, s.admin, s.quarantine.ID, ingest.Request{Batch: "b1"}, files)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.Fixtures.Review(ctx, s.admin, res.Files[0].FixtureID, true); err != nil {
		t.Fatal(err)
	}
	promoted, err := a.Datasets.Promote(ctx, s.admin, s.quarantine.ID, dataset.PromoteInput{Name: "inbox"})
	if err != nil {
		t.Fatal(err)
	}
	s.global = promoted.Dataset
	list, _ := a.Fixtures.List(ctx, s.admin, s.global.ID, fixture.ListFilter{})
	s.approved = list[0]
	return s
}

func export(t *testing.T, a *app.App, p principal.Principal, ids ...string) (*exchange.Export, []byte) {
	t.Helper()
	e, err := a.Exchange.PrepareExport(ctx, p, exchange.ExportRequest{DatasetIDs: ids})
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	if err := e.WriteTo(ctx, &buf); err != nil {
		t.Fatal(err)
	}
	return e, buf.Bytes()
}

func content(t *testing.T, a *app.App, p principal.Principal, id string) []byte {
	t.Helper()
	rc, _, err := a.Fixtures.Open(ctx, p, id)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rc.Close() }()
	b, _ := io.ReadAll(rc)
	return b
}

func TestExportImportBetweenInstances(t *testing.T) {
	src := newSource(t)
	exp, data := export(t, src.a, src.admin, src.ws.ID, src.global.ID, src.quarantine.ID, src.ws.ID)
	counts := exp.Counts()
	// work: 3 emails + 2 records; global: 1 approved; quarantine: 1 approved, 1 skipped.
	if counts["datasets"] != 3 || counts["fixtures"] != 7 || exp.Skipped != 1 || counts["persons"] < 4 {
		t.Fatalf("counts = %v, skipped %d", counts, exp.Skipped)
	}
	events, _ := src.a.Audit.List(ctx, src.admin, audit.Filter{Action: audit.ActionDatasetExport})
	if len(events) != 3 {
		t.Errorf("export events = %d", len(events))
	}

	dst := testutil.NewApp(t)
	dstAdmin := testutil.Admin(t, dst)
	res, err := dst.Exchange.Import(ctx, dstAdmin, bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Datasets) != 3 || len(res.Skipped) != 0 || res.PeopleAdded != counts["persons"] {
		t.Fatalf("result = %+v", res)
	}
	for _, d := range res.Datasets {
		if !d.Created || d.Existing != 0 {
			t.Errorf("dataset = %+v", d)
		}
	}

	g, err := dst.Datasets.Get(ctx, dstAdmin, src.global.ID)
	if err != nil || !g.Frozen() || g.Version != src.global.Version || g.OwnerAccountID != dstAdmin.AccountID || g.Scope != dataset.ScopeGlobal {
		t.Fatalf("global = %+v, %v", g, err)
	}
	copied, err := dst.Fixtures.Get(ctx, dstAdmin, src.approved.ID)
	if err != nil {
		t.Fatal(err)
	}
	if copied.ReviewState != fixture.ReviewApproved || copied.ReviewedBy != "" || copied.ImportedReviewer != src.admin.AccountID ||
		copied.ReviewedAt == nil || copied.IngestBatch != "b1" || copied.Source != fixture.SourceAnonymised || copied.AnonKeyID == "" {
		t.Errorf("imported review = %+v", copied)
	}
	if !bytes.Equal(content(t, dst, dstAdmin, copied.ID), content(t, src.a, src.admin, src.approved.ID)) {
		t.Error("content differs")
	}
	ws, _ := dst.Fixtures.List(ctx, dstAdmin, src.ws.ID, fixture.ListFilter{})
	if len(ws) != 5 {
		t.Fatalf("workspace has %d fixtures", len(ws))
	}
	for _, f := range ws {
		for _, l := range f.People {
			if _, err := dst.Persons.Get(ctx, dstAdmin, l.PersonID); err != nil {
				t.Errorf("%s links a missing person: %v", f.Name, err)
			}
		}
	}

	// Importing again adds nothing.
	again, err := dst.Exchange.Import(ctx, dstAdmin, bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range again.Datasets {
		if d.Created || d.Added != 0 || d.Existing == 0 {
			t.Errorf("re-import = %+v", d)
		}
	}
	imports, _ := dst.Audit.List(ctx, dstAdmin, audit.Filter{Action: audit.ActionDatasetImport})
	if len(imports) != 6 {
		t.Errorf("import events = %d", len(imports))
	}

	// Purge on the destination reaches the imported real-derived fixtures.
	if n, err := dst.Fixtures.PurgeBatch(ctx, dstAdmin, "b1"); err != nil || n != 2 {
		t.Errorf("purge = %d, %v", n, err)
	}
}

func TestMemberExportAndImport(t *testing.T) {
	src := newSource(t)
	// alice forks the global dataset, which carries the approved real-derived email.
	fork, err := src.a.Datasets.Fork(ctx, src.alice, src.global.ID, dataset.ForkInput{Name: "mine"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := src.a.Exchange.PrepareExport(ctx, src.alice, exchange.ExportRequest{DatasetIDs: []string{src.quarantine.ID}}); !errors.Is(err, apperr.ErrNotFound) {
		t.Errorf("member exporting quarantine err = %v", err)
	}
	_, data := export(t, src.a, src.alice, src.ws.ID, fork.Dataset.ID)

	dst := testutil.NewApp(t)
	dstAdmin := testutil.Admin(t, dst)
	bob := testutil.Member(t, dst, dstAdmin, "bob")
	// The destination already generated the same people with the same seed.
	if _, _, err := dst.Persons.Generate(ctx, bob, person.GenerateInput{Count: 4, Seed: 11}); err != nil {
		t.Fatal(err)
	}
	res, err := dst.Exchange.Import(ctx, bob, bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Skipped) != 1 || !strings.Contains(res.Skipped[0].Reason, "admin") {
		t.Errorf("skipped = %+v", res.Skipped)
	}
	if res.PeopleMerged == 0 {
		t.Errorf("no people merged by email: %+v", res)
	}
	all, _ := dst.Persons.List(ctx, bob, person.ListFilter{})
	emails := map[string]bool{}
	for _, p := range all {
		if emails[p.Email] {
			t.Errorf("duplicate person %s", p.Email)
		}
		emails[p.Email] = true
	}
	d, err := dst.Datasets.Get(ctx, bob, src.ws.ID)
	if err != nil || d.OwnerAccountID != bob.AccountID {
		t.Errorf("workspace = %+v, %v", d, err)
	}

	// A member cannot import global datasets; nothing is added.
	_, adminData := export(t, src.a, src.admin, src.global.ID)
	carol := testutil.Member(t, dst, dstAdmin, "carol")
	if _, err := dst.Exchange.Import(ctx, carol, bytes.NewReader(adminData)); !errors.Is(err, apperr.ErrForbidden) {
		t.Errorf("member importing global err = %v", err)
	}
	// Nor a dataset already here that belongs to someone else.
	if _, err := dst.Exchange.Import(ctx, carol, bytes.NewReader(data)); !errors.Is(err, apperr.ErrForbidden) {
		t.Errorf("importing over bob's dataset err = %v", err)
	}
}

// craft writes an export archive by hand.
func craft(t *testing.T, counts map[string]int, datasets []dataset.Dataset, persons []person.Person, fixtures []fixture.Fixture, blobs map[string][]byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	w, err := archive.NewWriter(&buf, archive.Manifest{Kind: archive.KindExport, Counts: counts})
	if err != nil {
		t.Fatal(err)
	}
	_ = archive.Records(w, "datasets.jsonl", datasets)
	_ = archive.Records(w, "persons.jsonl", persons)
	_ = archive.Records(w, "fixtures.jsonl", fixtures)
	for h, b := range blobs {
		if err := w.Blob(h, int64(len(b)), bytes.NewReader(b)); err != nil {
			t.Fatal(err)
		}
	}
	_ = w.Close()
	return buf.Bytes()
}

func TestImportRejects(t *testing.T) {
	src := newSource(t)
	exp, data := export(t, src.a, src.admin, src.ws.ID)
	counts := exp.Counts()

	dst := testutil.NewApp(t)
	admin := testutil.Admin(t, dst)
	reader := admin
	reader.Scopes = []principal.Scope{principal.ScopeRead}
	if _, err := dst.Exchange.Import(ctx, reader, bytes.NewReader(data)); !errors.Is(err, apperr.ErrForbidden) {
		t.Errorf("read-only key err = %v", err)
	}
	if _, err := dst.Exchange.Import(ctx, admin, bytes.NewReader(data[:len(data)/2])); !errors.Is(err, apperr.ErrInvalid) {
		t.Errorf("truncated err = %v", err)
	}
	if ds, _ := dst.Datasets.List(ctx, admin, dataset.ListFilter{}); len(ds) != 0 {
		t.Errorf("a failed import left %d datasets", len(ds))
	}
	var backupArchive bytes.Buffer
	if _, err := src.a.Backup.Backup(ctx, &backupArchive); err != nil {
		t.Fatal(err)
	}
	if _, err := dst.Exchange.Import(ctx, admin, &backupArchive); !errors.Is(err, apperr.ErrInvalid) || !strings.Contains(err.Error(), "restore") {
		t.Errorf("backup archive err = %v", err)
	}

	d := *src.ws
	f := fixture.Fixture{ID: "f1", DatasetID: d.ID, Name: "a.txt", Kind: fixture.KindFile, Format: "txt", Source: fixture.SourceUploaded,
		BlobHash: strings.Repeat("a", 64), People: []fixture.PersonLink{{PersonID: "p1", Role: "author"}}}
	p := person.Person{}
	p.ID, p.FirstName, p.Email = "p1", "Ann", "ann@acme.example"
	one := map[string]int{"datasets": 1, "persons": 1, "fixtures": 1, "blobs": 0}
	cases := map[string]struct {
		data []byte
		want error
	}{
		"counts mismatch": {craft(t, counts, []dataset.Dataset{d}, nil, nil, nil), apperr.ErrInvalid},
		"missing content": {craft(t, one, []dataset.Dataset{d}, []person.Person{p}, []fixture.Fixture{f}, nil), apperr.ErrInvalid},
		"unknown dataset": {craft(t, one, []dataset.Dataset{d}, []person.Person{p}, []fixture.Fixture{func() fixture.Fixture { g := f; g.DatasetID = "x"; return g }()}, nil), apperr.ErrInvalid},
		"unknown person":  {craft(t, map[string]int{"datasets": 1, "fixtures": 1}, []dataset.Dataset{d}, nil, []fixture.Fixture{f}, nil), apperr.ErrInvalid},
		"real address":    {craft(t, one, []dataset.Dataset{d}, []person.Person{func() person.Person { q := p; q.Email = "ann@acme.com"; return q }()}, []fixture.Fixture{func() fixture.Fixture { g := f; g.BlobHash = sha("x"); return g }()}, map[string][]byte{sha("x"): []byte("x")}), apperr.ErrInvalid},
		"no datasets":     {craft(t, map[string]int{}, nil, nil, nil, nil), apperr.ErrInvalid},
		"unapproved real": {craft(t, map[string]int{"datasets": 1, "fixtures": 1}, []dataset.Dataset{d}, nil, []fixture.Fixture{func() fixture.Fixture {
			g := f
			g.People = nil
			g.Source = fixture.SourceAnonymised
			g.ReviewState = fixture.ReviewUnreviewed
			return g
		}()}, nil), apperr.ErrInvalid},
		"path in fixture id": {craft(t, map[string]int{"datasets": 1, "fixtures": 1}, []dataset.Dataset{d}, nil, []fixture.Fixture{func() fixture.Fixture { g := f; g.ID, g.People = "../../etc/passwd", nil; return g }()}, nil), apperr.ErrInvalid},
		"path in person id": {craft(t, one, []dataset.Dataset{d}, []person.Person{func() person.Person { q := p; q.ID = "../p1"; return q }()}, []fixture.Fixture{func() fixture.Fixture {
			g := f
			g.People = []fixture.PersonLink{{PersonID: "../p1", Role: "author"}}
			return g
		}()}, nil), apperr.ErrInvalid},
		"bad dataset scope": {craft(t, map[string]int{"datasets": 1}, []dataset.Dataset{func() dataset.Dataset { e := d; e.Scope = "public"; return e }()}, nil, nil, nil), apperr.ErrInvalid},
	}
	for name, c := range cases {
		if _, err := dst.Exchange.Import(ctx, admin, bytes.NewReader(c.data)); !errors.Is(err, c.want) {
			t.Errorf("%s: err = %v", name, err)
		}
	}

	// A workspace of the same name but another ID is a conflict.
	if _, err := dst.Datasets.Create(ctx, admin, dataset.CreateInput{Name: "work"}); err != nil {
		t.Fatal(err)
	}
	if _, err := dst.Exchange.Import(ctx, admin, bytes.NewReader(data)); !errors.Is(err, apperr.ErrConflict) {
		t.Errorf("name clash err = %v", err)
	}

	for _, req := range []exchange.ExportRequest{{}, {DatasetIDs: make([]string, exchange.MaxDatasets+1)}} {
		for i := range req.DatasetIDs {
			req.DatasetIDs[i] = strings.Repeat("x", i+1)
		}
		if _, err := src.a.Exchange.PrepareExport(ctx, src.admin, req); !errors.Is(err, apperr.ErrInvalid) {
			t.Errorf("export of %d datasets err = %v", len(req.DatasetIDs), err)
		}
	}
}

func TestImportIntoFrozenDataset(t *testing.T) {
	src := newSource(t)
	_, first := export(t, src.a, src.admin, src.ws.ID)
	dst := testutil.NewApp(t)
	admin := testutil.Admin(t, dst)
	if _, err := dst.Exchange.Import(ctx, admin, bytes.NewReader(first)); err != nil {
		t.Fatal(err)
	}
	if _, err := dst.Datasets.Freeze(ctx, admin, src.ws.ID); err != nil {
		t.Fatal(err)
	}
	// Unchanged: nothing to add, so a frozen dataset is fine.
	if _, err := dst.Exchange.Import(ctx, admin, bytes.NewReader(first)); err != nil {
		t.Errorf("re-import into frozen = %v", err)
	}
	if _, err := src.a.Generate.Generate(ctx, src.alice, src.ws.ID, generate.Request{Kind: generate.KindFiles, Count: 1, Seed: 99}); err != nil {
		t.Fatal(err)
	}
	_, second := export(t, src.a, src.admin, src.ws.ID)
	if _, err := dst.Exchange.Import(ctx, admin, bytes.NewReader(second)); !errors.Is(err, apperr.ErrConflict) || !strings.Contains(err.Error(), "frozen") {
		t.Errorf("new fixture into frozen err = %v", err)
	}
}

func sha(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])
}

func TestImportKeepsParentLinks(t *testing.T) {
	src := newSource(t)
	fork, err := src.a.Datasets.Fork(ctx, src.alice, src.ws.ID, dataset.ForkInput{Name: "fork"})
	if err != nil {
		t.Fatal(err)
	}
	// The fork comes first in the archive, before its source.
	_, data := export(t, src.a, src.admin, fork.Dataset.ID, src.global.ID, src.ws.ID, src.quarantine.ID)
	dst := testutil.NewApp(t)
	admin := testutil.Admin(t, dst)
	if _, err := dst.Exchange.Import(ctx, admin, bytes.NewReader(data)); err != nil {
		t.Fatal(err)
	}
	for child, parent := range map[string]string{fork.Dataset.ID: src.ws.ID, src.global.ID: src.quarantine.ID} {
		d, err := dst.Datasets.Get(ctx, admin, child)
		if err != nil || d.ParentID != parent {
			t.Errorf("%s parent = %q, want %q (%v)", child, d.ParentID, parent, err)
		}
	}
}

func TestImportFixtureIDInAnotherDataset(t *testing.T) {
	src := newSource(t)
	exp, data := export(t, src.a, src.admin, src.ws.ID)
	dst := testutil.NewApp(t)
	admin := testutil.Admin(t, dst)
	if _, err := dst.Exchange.Import(ctx, admin, bytes.NewReader(data)); err != nil {
		t.Fatal(err)
	}
	// The same fixtures, now claimed by a dataset with another ID.
	ar, err := archive.NewReader(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	var datasets []dataset.Dataset
	var persons []person.Person
	var fixtures []fixture.Fixture
	blobs := map[string][]byte{}
	for {
		e, err := ar.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		switch {
		case e.Name == "datasets.jsonl":
			datasets, _ = archive.ReadRecords[dataset.Dataset](e)
		case e.Name == "persons.jsonl":
			persons, _ = archive.ReadRecords[person.Person](e)
		case e.Name == "fixtures.jsonl":
			fixtures, _ = archive.ReadRecords[fixture.Fixture](e)
		case e.IsBlob():
			blobs[e.BlobHash()], _ = io.ReadAll(e)
		}
	}
	datasets[0].ID, datasets[0].Name = "other-dataset", "other"
	for i := range fixtures {
		fixtures[i].DatasetID = "other-dataset"
	}
	moved := craft(t, exp.Counts(), datasets, persons, fixtures, blobs)
	if _, err := dst.Exchange.Import(ctx, admin, bytes.NewReader(moved)); !errors.Is(err, apperr.ErrConflict) || !strings.Contains(err.Error(), "another dataset") {
		t.Errorf("err = %v", err)
	}
	if _, err := dst.Datasets.Get(ctx, admin, "other-dataset"); !errors.Is(err, apperr.ErrNotFound) {
		t.Errorf("a failed import left the dataset: %v", err)
	}
}

func TestSlowImportDoesNotStallCleanup(t *testing.T) {
	src := newSource(t)
	_, data := export(t, src.a, src.admin, src.ws.ID)
	var blobDir string
	dst := testutil.NewAppWith(t, func(c *app.Config) { blobDir = c.BlobDir })
	admin := testutil.Admin(t, dst)
	scratch, err := dst.Datasets.Create(ctx, admin, dataset.CreateInput{Name: "scratch"})
	if err != nil {
		t.Fatal(err)
	}

	pr, pw := io.Pipe()
	done := make(chan error, 1)
	go func() {
		_, err := dst.Exchange.Import(ctx, admin, pr)
		done <- err
	}()
	// Send all but the end of the archive, then stall like a slow client.
	if _, err := pw.Write(data[:len(data)-64]); err != nil {
		t.Fatal(err)
	}
	// Wait until the import is storing contents.
	for start := time.Now(); ; time.Sleep(10 * time.Millisecond) {
		if stored, _ := filepath.Glob(filepath.Join(blobDir, "objects", "*", "*", "*")); len(stored) > 0 {
			break
		}
		if time.Since(start) > 5*time.Second {
			t.Fatal("the import never stored content")
		}
	}

	deleted := make(chan error, 1)
	go func() { deleted <- dst.Datasets.Delete(ctx, admin, scratch.ID) }()
	select {
	case err := <-deleted:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("deleting a dataset (and collecting content) waited for an import upload")
	}

	if _, err := pw.Write(data[len(data)-64:]); err != nil {
		t.Fatal(err)
	}
	_ = pw.Close()
	if err := <-done; err != nil {
		t.Fatalf("import = %v", err)
	}
}

func TestFailedImportLeavesNoContent(t *testing.T) {
	src := newSource(t)
	_, data := export(t, src.a, src.admin, src.ws.ID)
	// Valid records and contents, but a dataset named like one already here.
	var blobDir string
	dst := testutil.NewAppWith(t, func(c *app.Config) { blobDir = c.BlobDir })
	admin := testutil.Admin(t, dst)
	if _, err := dst.Datasets.Create(ctx, admin, dataset.CreateInput{Name: "work"}); err != nil {
		t.Fatal(err)
	}
	if _, err := dst.Exchange.Import(ctx, admin, bytes.NewReader(data)); !errors.Is(err, apperr.ErrConflict) {
		t.Fatalf("err = %v", err)
	}
	scratch, _ := dst.Datasets.Create(ctx, admin, dataset.CreateInput{Name: "scratch"})
	if err := dst.Datasets.Delete(ctx, admin, scratch.ID); err != nil { // collects content
		t.Fatal(err)
	}
	if left, _ := filepath.Glob(filepath.Join(blobDir, "objects", "*", "*", "*")); len(left) != 0 {
		t.Errorf("%d contents left after a failed import and a collection", len(left))
	}
}
