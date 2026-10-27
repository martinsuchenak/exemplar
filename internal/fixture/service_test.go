package fixture_test

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/martinsuchenak/exemplar/internal/app"
	"github.com/martinsuchenak/exemplar/internal/apperr"
	"github.com/martinsuchenak/exemplar/internal/audit"
	"github.com/martinsuchenak/exemplar/internal/dataset"
	"github.com/martinsuchenak/exemplar/internal/fixture"
	"github.com/martinsuchenak/exemplar/internal/person"
	"github.com/martinsuchenak/exemplar/internal/principal"
	"github.com/martinsuchenak/exemplar/internal/testutil"
)

var ctx = context.Background()

const sampleEML = "From: a@example.com\r\nTo: b@example.com\r\nSubject: hi\r\n\r\nhello\r\n"

func newDataset(t *testing.T, a *app.App, p principal.Principal, name string, scope dataset.Scope) *dataset.Dataset {
	t.Helper()
	d, err := a.Datasets.Create(ctx, p, dataset.CreateInput{Name: name, Scope: scope})
	if err != nil {
		t.Fatalf("create dataset %s: %v", name, err)
	}
	return d
}

func readAll(t *testing.T, a *app.App, p principal.Principal, id string) string {
	t.Helper()
	r, _, err := a.Fixtures.Open(ctx, p, id)
	if err != nil {
		t.Fatalf("open %s: %v", id, err)
	}
	defer func() { _ = r.Close() }()
	b, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	return string(b)
}

func TestCreateInfersAndServesContent(t *testing.T) {
	a := testutil.NewApp(t)
	admin := testutil.Admin(t, a)
	alice := testutil.Member(t, a, admin, "alice")
	ws := newDataset(t, a, alice, "mine", dataset.ScopeWorkspace)

	f, err := a.Fixtures.Create(ctx, alice, fixture.CreateInput{
		DatasetID: ws.ID, Name: "Hello.EML", Tags: []string{"Reply"}, Content: strings.NewReader(sampleEML),
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if f.Kind != fixture.KindEmail || f.Format != "eml" || f.MimeType != "message/rfc822" {
		t.Errorf("inferred kind/format/mime = %s/%s/%s", f.Kind, f.Format, f.MimeType)
	}
	if f.Source != fixture.SourceUploaded || f.ReviewState != fixture.ReviewNotRequired || f.Size != int64(len(sampleEML)) {
		t.Errorf("fixture = %+v", f)
	}
	if got := readAll(t, a, alice, f.ID); got != sampleEML {
		t.Errorf("content = %q", got)
	}

	// Same content under another name shares the blob.
	g, err := a.Fixtures.Create(ctx, alice, fixture.CreateInput{DatasetID: ws.ID, Name: "copy.eml", Content: strings.NewReader(sampleEML)})
	if err != nil || g.BlobHash != f.BlobHash {
		t.Errorf("second create = %+v, %v; want the same blob hash", g, err)
	}
	if _, err := a.Fixtures.Create(ctx, alice, fixture.CreateInput{DatasetID: ws.ID, Name: "Hello.EML", Content: strings.NewReader("x")}); !errors.Is(err, apperr.ErrConflict) {
		t.Errorf("duplicate name err = %v, want ErrConflict", err)
	}

	for _, tag := range []string{"reply", " Reply ", "REPLY"} {
		tagged, err := a.Fixtures.List(ctx, alice, ws.ID, fixture.ListFilter{Tag: tag})
		if err != nil || len(tagged) != 1 || tagged[0].ID != f.ID {
			t.Errorf("tag filter %q = %+v, %v", tag, tagged, err)
		}
	}
}

func TestAccessFollowsDataset(t *testing.T) {
	a := testutil.NewApp(t)
	admin := testutil.Admin(t, a)
	alice := testutil.Member(t, a, admin, "alice")
	bob := testutil.Member(t, a, admin, "bob")
	ws := newDataset(t, a, alice, "mine", dataset.ScopeWorkspace)
	global := newDataset(t, a, admin, "shared", dataset.ScopeGlobal)

	f, err := a.Fixtures.Create(ctx, alice, fixture.CreateInput{DatasetID: ws.ID, Name: "a.txt", Content: strings.NewReader("a")})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := a.Fixtures.Get(ctx, bob, f.ID); !errors.Is(err, apperr.ErrNotFound) {
		t.Errorf("bob get err = %v, want ErrNotFound", err)
	}
	if _, err := a.Fixtures.Create(ctx, bob, fixture.CreateInput{DatasetID: ws.ID, Name: "b.txt", Content: strings.NewReader("b")}); !errors.Is(err, apperr.ErrNotFound) {
		t.Errorf("bob writing alice's workspace err = %v, want ErrNotFound", err)
	}
	if _, err := a.Fixtures.Create(ctx, alice, fixture.CreateInput{DatasetID: global.ID, Name: "c.txt", Content: strings.NewReader("c")}); !errors.Is(err, apperr.ErrForbidden) {
		t.Errorf("member writing global err = %v, want ErrForbidden", err)
	}

	g, err := a.Fixtures.Create(ctx, admin, fixture.CreateInput{DatasetID: global.ID, Name: "g.txt", Content: strings.NewReader("g")})
	if err != nil {
		t.Fatalf("admin create in global: %v", err)
	}
	if got := readAll(t, a, bob, g.ID); got != "g" {
		t.Errorf("bob reading global = %q", got)
	}

	if _, err := a.Datasets.Freeze(ctx, admin, global.ID); err != nil {
		t.Fatalf("freeze: %v", err)
	}
	if err := a.Fixtures.Delete(ctx, admin, g.ID); !errors.Is(err, apperr.ErrConflict) {
		t.Errorf("delete from frozen err = %v, want ErrConflict", err)
	}
}

func TestRealDerivedDataGoesThroughQuarantine(t *testing.T) {
	a := testutil.NewApp(t)
	admin := testutil.Admin(t, a)
	alice := testutil.Member(t, a, admin, "alice")
	ws := newDataset(t, a, alice, "mine", dataset.ScopeWorkspace)
	q := newDataset(t, a, admin, "inbox", dataset.ScopeQuarantine)

	anon := fixture.CreateInput{Name: "real.eml", Source: fixture.SourceAnonymised, Content: strings.NewReader(sampleEML)}

	anon.DatasetID = ws.ID
	if _, err := a.Fixtures.Create(ctx, alice, anon); !errors.Is(err, apperr.ErrInvalid) {
		t.Errorf("anonymised into workspace err = %v, want ErrInvalid", err)
	}
	if _, err := a.Fixtures.Create(ctx, admin, fixture.CreateInput{DatasetID: q.ID, Name: "x.txt", Content: strings.NewReader("x")}); !errors.Is(err, apperr.ErrInvalid) {
		t.Errorf("uploaded into quarantine err = %v, want ErrInvalid", err)
	}

	anon.DatasetID = q.ID
	anon.Content = strings.NewReader(sampleEML)
	f, err := a.Fixtures.Create(ctx, admin, anon)
	if err != nil {
		t.Fatalf("create in quarantine: %v", err)
	}
	if f.ReviewState != fixture.ReviewUnreviewed {
		t.Errorf("review state = %s, want unreviewed", f.ReviewState)
	}
	if _, err := a.Fixtures.Get(ctx, alice, f.ID); !errors.Is(err, apperr.ErrNotFound) {
		t.Errorf("member sees quarantine fixture: %v", err)
	}
	if _, err := a.Fixtures.Review(ctx, alice, f.ID, true); !errors.Is(err, apperr.ErrForbidden) {
		t.Errorf("member review err = %v, want ErrForbidden", err)
	}

	// Not through an entry point that hides quarantine (MCP), even as admin.
	viaMCP := admin
	viaMCP.NoQuarantine = true
	if _, _, err := a.Fixtures.Open(ctx, viaMCP, f.ID); !errors.Is(err, apperr.ErrNotFound) {
		t.Errorf("open quarantine without quarantine err = %v, want ErrNotFound", err)
	}
	if _, err := a.Fixtures.List(ctx, viaMCP, q.ID, fixture.ListFilter{}); !errors.Is(err, apperr.ErrNotFound) {
		t.Errorf("list quarantine without quarantine err = %v, want ErrNotFound", err)
	}

	// Admin reads it to review: the read is audited.
	readAll(t, a, admin, f.ID)
	reviewed, err := a.Fixtures.Review(ctx, admin, f.ID, true)
	if err != nil || reviewed.ReviewState != fixture.ReviewApproved || reviewed.ReviewedBy != admin.AccountID {
		t.Fatalf("review = %+v, %v", reviewed, err)
	}
	if _, err := a.Fixtures.Review(ctx, admin, f.ID, false); !errors.Is(err, apperr.ErrConflict) {
		t.Errorf("second review err = %v, want ErrConflict", err)
	}

	events, err := a.Audit.List(ctx, admin, audit.Filter{TargetID: f.ID})
	if err != nil {
		t.Fatalf("audit: %v", err)
	}
	var actions []string
	for _, e := range events {
		actions = append(actions, e.Action)
	}
	want := audit.ActionFixtureCreate + "," + audit.ActionFixtureDownload + "," + audit.ActionFixtureApprove
	if strings.Join(actions, ",") != want {
		t.Errorf("audit = %v, want %s", actions, want)
	}
}

func TestDeleteCollectsUnusedBlobs(t *testing.T) {
	a := testutil.NewApp(t)
	admin := testutil.Admin(t, a)
	alice := testutil.Member(t, a, admin, "alice")
	ws := newDataset(t, a, alice, "mine", dataset.ScopeWorkspace)
	other := newDataset(t, a, alice, "other", dataset.ScopeWorkspace)

	f1, err := a.Fixtures.Create(ctx, alice, fixture.CreateInput{DatasetID: ws.ID, Name: "a.txt", Content: strings.NewReader("shared")})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	f2, err := a.Fixtures.Create(ctx, alice, fixture.CreateInput{DatasetID: other.ID, Name: "a.txt", Content: strings.NewReader("shared")})
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	// The blob survives while another fixture still uses it.
	if err := a.Datasets.Delete(ctx, alice, ws.ID); err != nil {
		t.Fatalf("delete dataset: %v", err)
	}
	if got := readAll(t, a, alice, f2.ID); got != "shared" {
		t.Errorf("content after deleting the other dataset = %q", got)
	}
	if _, err := a.Fixtures.Get(ctx, alice, f1.ID); !errors.Is(err, apperr.ErrNotFound) {
		t.Errorf("fixture of deleted dataset err = %v, want ErrNotFound", err)
	}

	if err := a.Fixtures.Delete(ctx, alice, f2.ID); err != nil {
		t.Fatalf("delete fixture: %v", err)
	}
	// Re-adding the same bytes works after the blob was collected.
	f3, err := a.Fixtures.Create(ctx, alice, fixture.CreateInput{DatasetID: other.ID, Name: "b.txt", Content: strings.NewReader("shared")})
	if err != nil {
		t.Fatalf("re-create: %v", err)
	}
	if got := readAll(t, a, alice, f3.ID); got != "shared" {
		t.Errorf("content after re-create = %q", got)
	}
}

func TestInvalidNames(t *testing.T) {
	a := testutil.NewApp(t)
	admin := testutil.Admin(t, a)
	ws := newDataset(t, a, admin, "mine", dataset.ScopeWorkspace)
	for _, name := range []string{"", "a/b.txt", `a\b.txt`, "noext", ".."} {
		_, err := a.Fixtures.Create(ctx, admin, fixture.CreateInput{DatasetID: ws.ID, Name: name, Content: strings.NewReader("x")})
		if !errors.Is(err, apperr.ErrInvalid) {
			t.Errorf("name %q err = %v, want ErrInvalid", name, err)
		}
	}
}

func TestListFiltersAndPaging(t *testing.T) {
	a := testutil.NewApp(t)
	admin := testutil.Admin(t, a)
	alice := testutil.Member(t, a, admin, "alice")
	ws := newDataset(t, a, alice, "mine", dataset.ScopeWorkspace)
	for _, in := range []fixture.CreateInput{
		{Name: "a.eml", Scenario: "reply"},
		{Name: "b.eml", Scenario: "forward"},
		{Name: "c.pdf"},
		{Name: "d.json", Kind: fixture.KindRecord, RecordSchema: "contact", Source: fixture.SourceSynthetic, Generator: "test"},
	} {
		in.DatasetID = ws.ID
		in.Content = strings.NewReader(in.Name)
		if _, err := a.Fixtures.Create(ctx, alice, in); err != nil {
			t.Fatalf("create %s: %v", in.Name, err)
		}
	}

	tests := []struct {
		name  string
		f     fixture.ListFilter
		names string
	}{
		{"all, by name", fixture.ListFilter{}, "a.eml,b.eml,c.pdf,d.json"},
		{"kind", fixture.ListFilter{Kind: fixture.KindEmail}, "a.eml,b.eml"},
		{"format", fixture.ListFilter{Format: "pdf"}, "c.pdf"},
		{"scenario", fixture.ListFilter{Scenario: "forward"}, "b.eml"},
		{"review state", fixture.ListFilter{ReviewState: fixture.ReviewNotRequired}, "a.eml,b.eml,c.pdf,d.json"},
		{"limit", fixture.ListFilter{Limit: 2}, "a.eml,b.eml"},
		{"offset", fixture.ListFilter{Limit: 2, Offset: 2}, "c.pdf,d.json"},
		{"negative offset", fixture.ListFilter{Offset: -5}, "a.eml,b.eml,c.pdf,d.json"},
		{"limit capped", fixture.ListFilter{Limit: 1_000_000}, "a.eml,b.eml,c.pdf,d.json"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := a.Fixtures.List(ctx, alice, ws.ID, tt.f)
			if err != nil {
				t.Fatalf("list: %v", err)
			}
			var names []string
			for _, f := range got {
				names = append(names, f.Name)
			}
			if strings.Join(names, ",") != tt.names {
				t.Errorf("names = %v, want %s", names, tt.names)
			}
		})
	}

	for _, f := range []fixture.ListFilter{{Kind: "video"}, {ReviewState: "pending"}} {
		if _, err := a.Fixtures.List(ctx, alice, ws.ID, f); !errors.Is(err, apperr.ErrInvalid) {
			t.Errorf("List(%+v) err = %v, want ErrInvalid", f, err)
		}
	}
	bob := testutil.Member(t, a, admin, "bob")
	if _, err := a.Fixtures.List(ctx, bob, ws.ID, fixture.ListFilter{}); !errors.Is(err, apperr.ErrNotFound) {
		t.Errorf("bob list err = %v, want ErrNotFound", err)
	}
}

func TestCreateInputRules(t *testing.T) {
	a := testutil.NewApp(t)
	admin := testutil.Admin(t, a)
	ws := newDataset(t, a, admin, "mine", dataset.ScopeWorkspace)

	tests := []struct {
		name string
		in   fixture.CreateInput
	}{
		{"no content", fixture.CreateInput{Name: "a.txt"}},
		{"unknown kind", fixture.CreateInput{Name: "a.txt", Kind: "video", Content: strings.NewReader("x")}},
		{"unknown source", fixture.CreateInput{Name: "a.txt", Source: "scraped", Content: strings.NewReader("x")}},
	}
	for _, tt := range tests {
		tt.in.DatasetID = ws.ID
		if _, err := a.Fixtures.Create(ctx, admin, tt.in); !errors.Is(err, apperr.ErrInvalid) {
			t.Errorf("%s: err = %v, want ErrInvalid", tt.name, err)
		}
	}
	if _, err := a.Fixtures.Create(ctx, admin, fixture.CreateInput{DatasetID: "missing", Name: "a.txt", Content: strings.NewReader("x")}); !errors.Is(err, apperr.ErrNotFound) {
		t.Errorf("missing dataset err = %v, want ErrNotFound", err)
	}
}

func TestFormatAndMimeInference(t *testing.T) {
	a := testutil.NewApp(t)
	admin := testutil.Admin(t, a)
	ws := newDataset(t, a, admin, "mine", dataset.ScopeWorkspace)

	tests := []struct {
		in         fixture.CreateInput
		kind       fixture.Kind
		format     string
		mimePrefix string
	}{
		{fixture.CreateInput{Name: "x.msg"}, fixture.KindEmail, "msg", "application/vnd.ms-outlook"},
		{fixture.CreateInput{Name: "x.pdf"}, fixture.KindFile, "pdf", "application/pdf"},
		{fixture.CreateInput{Name: "x.unknownext"}, fixture.KindFile, "unknownext", "application/octet-stream"},
		{fixture.CreateInput{Name: "raw", Format: ".EML"}, fixture.KindEmail, "eml", "message/rfc822"},
		{fixture.CreateInput{Name: "y.bin", MimeType: "image/png"}, fixture.KindFile, "bin", "image/png"},
	}
	for _, tt := range tests {
		tt.in.DatasetID = ws.ID
		tt.in.Content = strings.NewReader(tt.in.Name)
		f, err := a.Fixtures.Create(ctx, admin, tt.in)
		if err != nil {
			t.Errorf("%s: %v", tt.in.Name, err)
			continue
		}
		if f.Kind != tt.kind || f.Format != tt.format || !strings.HasPrefix(f.MimeType, tt.mimePrefix) {
			t.Errorf("%s: kind/format/mime = %s/%s/%s, want %s/%s/%s*", tt.in.Name, f.Kind, f.Format, f.MimeType, tt.kind, tt.format, tt.mimePrefix)
		}
	}
}

func TestRejectAndDeleteRules(t *testing.T) {
	a := testutil.NewApp(t)
	admin := testutil.Admin(t, a)
	alice := testutil.Member(t, a, admin, "alice")
	bob := testutil.Member(t, a, admin, "bob")
	q := newDataset(t, a, admin, "inbox", dataset.ScopeQuarantine)
	ws := newDataset(t, a, alice, "mine", dataset.ScopeWorkspace)

	f, err := a.Fixtures.Create(ctx, admin, fixture.CreateInput{
		DatasetID: q.ID, Name: "real.eml", Source: fixture.SourceAnonymised, IngestBatch: "b1",
		AnonProfile: "default", AnonKeyID: "k1", Content: strings.NewReader(sampleEML),
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if f.IngestBatch != "b1" || f.AnonProfile != "default" || f.AnonKeyID != "k1" {
		t.Errorf("provenance not stored: %+v", f)
	}
	rejected, err := a.Fixtures.Review(ctx, admin, f.ID, false)
	if err != nil || rejected.ReviewState != fixture.ReviewRejected {
		t.Errorf("reject = %+v, %v", rejected, err)
	}
	if _, err := a.Fixtures.Review(ctx, admin, "missing", true); !errors.Is(err, apperr.ErrNotFound) {
		t.Errorf("review missing err = %v, want ErrNotFound", err)
	}

	plain, err := a.Fixtures.Create(ctx, alice, fixture.CreateInput{DatasetID: ws.ID, Name: "a.txt", Content: strings.NewReader("a")})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := a.Fixtures.Review(ctx, admin, plain.ID, true); !errors.Is(err, apperr.ErrConflict) {
		t.Errorf("reviewing a fixture that needs no review err = %v, want ErrConflict", err)
	}
	if err := a.Fixtures.Delete(ctx, bob, plain.ID); !errors.Is(err, apperr.ErrNotFound) {
		t.Errorf("bob delete err = %v, want ErrNotFound", err)
	}
	if err := a.Fixtures.Delete(ctx, alice, "missing"); !errors.Is(err, apperr.ErrNotFound) {
		t.Errorf("delete missing err = %v, want ErrNotFound", err)
	}
	if _, _, err := a.Fixtures.Open(ctx, bob, plain.ID); !errors.Is(err, apperr.ErrNotFound) {
		t.Errorf("bob open err = %v, want ErrNotFound", err)
	}
}

func TestValidValues(t *testing.T) {
	for _, k := range []fixture.Kind{fixture.KindEmail, fixture.KindFile, fixture.KindRecord} {
		if !k.Valid() {
			t.Errorf("kind %s invalid", k)
		}
	}
	for _, s := range []fixture.Source{fixture.SourceSynthetic, fixture.SourceUploaded, fixture.SourcePublicCorpus, fixture.SourceAnonymised} {
		if !s.Valid() {
			t.Errorf("source %s invalid", s)
		}
	}
	for _, r := range []fixture.ReviewState{fixture.ReviewNotRequired, fixture.ReviewUnreviewed, fixture.ReviewApproved, fixture.ReviewRejected} {
		if !r.Valid() {
			t.Errorf("review state %s invalid", r)
		}
	}
	if (&fixture.Fixture{Source: fixture.SourcePublicCorpus}).RealDerived() {
		t.Error("public corpus counted as real-derived")
	}
}

func TestPurgeBatch(t *testing.T) {
	a := testutil.NewApp(t)
	admin := testutil.Admin(t, a)
	alice := testutil.Member(t, a, admin, "alice")
	q := newDataset(t, a, admin, "inbox", dataset.ScopeQuarantine)

	for i, name := range []string{"one.eml", "two.eml"} {
		f, err := a.Fixtures.Create(ctx, admin, fixture.CreateInput{DatasetID: q.ID, Name: name, Source: fixture.SourceAnonymised, IngestBatch: "batch-1", Content: strings.NewReader(name)})
		if err != nil {
			t.Fatalf("add: %v", err)
		}
		if i == 0 {
			if _, err := a.Fixtures.Review(ctx, admin, f.ID, true); err != nil {
				t.Fatalf("approve: %v", err)
			}
		}
	}
	other, err := a.Fixtures.Create(ctx, admin, fixture.CreateInput{DatasetID: q.ID, Name: "other.eml", Source: fixture.SourceAnonymised, IngestBatch: "batch-2", Content: strings.NewReader("other")})
	if err != nil {
		t.Fatalf("add other: %v", err)
	}
	// The approved one is copied into a frozen global dataset; purge must reach it too.
	promoted, err := a.Datasets.Promote(ctx, admin, q.ID, dataset.PromoteInput{Name: "lib"})
	if err != nil || promoted.Copied != 1 {
		t.Fatalf("promote = %+v, %v", promoted, err)
	}

	if _, err := a.Fixtures.PurgeBatch(ctx, alice, "batch-1"); !errors.Is(err, apperr.ErrForbidden) {
		t.Errorf("member purge err = %v, want ErrForbidden", err)
	}
	if _, err := a.Fixtures.PurgeBatch(ctx, admin, "  "); !errors.Is(err, apperr.ErrInvalid) {
		t.Errorf("empty batch err = %v, want ErrInvalid", err)
	}
	n, err := a.Fixtures.PurgeBatch(ctx, admin, "batch-1")
	if err != nil || n != 3 {
		t.Fatalf("purge = %d, %v; want 3 (two in quarantine, one copy)", n, err)
	}
	if left, _ := a.Fixtures.List(ctx, admin, promoted.Dataset.ID, fixture.ListFilter{}); len(left) != 0 {
		t.Errorf("frozen dataset still holds %d purged fixtures", len(left))
	}
	if _, err := a.Fixtures.Get(ctx, admin, other.ID); err != nil {
		t.Errorf("fixture from another batch was purged: %v", err)
	}
	if n, err := a.Fixtures.PurgeBatch(ctx, admin, "batch-1"); err != nil || n != 0 {
		t.Errorf("second purge = %d, %v; want 0", n, err)
	}
	events, _ := a.Audit.List(ctx, admin, audit.Filter{Action: audit.ActionFixturePurge})
	if len(events) != 2 || events[0].TargetID != "batch-1" || events[0].Details["fixtures"] != float64(3) {
		t.Errorf("purge audit = %+v", events)
	}
}

func TestSizeLimit(t *testing.T) {
	a := testutil.NewAppWith(t, func(c *app.Config) { c.MaxFixtureBytes = 10 })
	admin := testutil.Admin(t, a)
	ws := newDataset(t, a, admin, "mine", dataset.ScopeWorkspace)

	if _, err := a.Fixtures.Create(ctx, admin, fixture.CreateInput{DatasetID: ws.ID, Name: "ok.txt", Content: strings.NewReader("0123456789")}); err != nil {
		t.Errorf("content at the limit: %v", err)
	}
	_, err := a.Fixtures.Create(ctx, admin, fixture.CreateInput{DatasetID: ws.ID, Name: "big.txt", Content: strings.NewReader("0123456789x")})
	if !errors.Is(err, apperr.ErrTooLarge) {
		t.Errorf("content over the limit err = %v, want ErrTooLarge", err)
	}
	if list, _ := a.Fixtures.List(ctx, admin, ws.ID, fixture.ListFilter{}); len(list) != 1 {
		t.Errorf("dataset holds %d fixtures, want only the one at the limit", len(list))
	}
}

func TestPersonLinksAndRecordSchemas(t *testing.T) {
	a := testutil.NewApp(t)
	admin := testutil.Admin(t, a)
	alice := testutil.Member(t, a, admin, "alice")
	ws := newDataset(t, a, alice, "mine", dataset.ScopeWorkspace)
	people, _, err := a.Persons.Generate(ctx, alice, person.GenerateInput{Count: 2, Seed: 1})
	if err != nil {
		t.Fatal(err)
	}
	p1, p2 := people[0].ID, people[1].ID

	f, err := a.Fixtures.Create(ctx, alice, fixture.CreateInput{
		DatasetID: ws.ID, Name: "c.json", RecordSchema: "contact", Content: strings.NewReader("{}"),
		People: []fixture.PersonLink{{PersonID: p1, Role: "contact"}, {PersonID: p1, Role: "contact"}, {PersonID: p2, Role: "manager"}},
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if f.Kind != fixture.KindRecord || f.RecordSchema != "contact" || len(f.People) != 2 {
		t.Errorf("record = %+v (duplicate links should collapse)", f)
	}
	got, err := a.Fixtures.Get(ctx, alice, f.ID)
	if err != nil || len(got.People) != 2 {
		t.Errorf("get links = %+v, %v", got, err)
	}
	plain, err := a.Fixtures.Create(ctx, alice, fixture.CreateInput{DatasetID: ws.ID, Name: "p.txt", Content: strings.NewReader("x")})
	if err != nil || plain.People == nil || len(plain.People) != 0 {
		t.Errorf("fixture without links has People = %#v, %v", plain.People, err)
	}

	byP2, _ := a.Fixtures.List(ctx, alice, ws.ID, fixture.ListFilter{PersonID: p2})
	if len(byP2) != 1 || byP2[0].ID != f.ID || len(byP2[0].People) != 2 {
		t.Errorf("person filter = %+v", byP2)
	}
	if records, _ := a.Fixtures.List(ctx, alice, ws.ID, fixture.ListFilter{RecordSchema: "contact"}); len(records) != 1 {
		t.Errorf("schema filter = %d", len(records))
	}

	fork, err := a.Datasets.Fork(ctx, alice, ws.ID, dataset.ForkInput{Name: "copy"})
	if err != nil {
		t.Fatal(err)
	}
	copies, _ := a.Fixtures.List(ctx, alice, fork.Dataset.ID, fixture.ListFilter{PersonID: p1})
	if len(copies) != 1 || copies[0].RecordSchema != "contact" || len(copies[0].People) != 2 {
		t.Errorf("fork lost record schema or links: %+v", copies)
	}

	tests := []struct {
		name string
		in   fixture.CreateInput
	}{
		{"record without schema", fixture.CreateInput{Name: "r.json", Kind: fixture.KindRecord}},
		{"schema on a file", fixture.CreateInput{Name: "f.txt", Kind: fixture.KindFile, RecordSchema: "contact"}},
		{"bad schema", fixture.CreateInput{Name: "r.json", RecordSchema: "Contact Card"}},
		{"link without person", fixture.CreateInput{Name: "x.txt", People: []fixture.PersonLink{{Role: "to"}}}},
		{"bad role", fixture.CreateInput{Name: "x.txt", People: []fixture.PersonLink{{PersonID: p1, Role: "To Whom"}}}},
		{"unknown person", fixture.CreateInput{Name: "x.txt", People: []fixture.PersonLink{{PersonID: "ghost", Role: "to"}}}},
	}
	for _, tt := range tests {
		tt.in.DatasetID = ws.ID
		tt.in.Content = strings.NewReader("x")
		if _, err := a.Fixtures.Create(ctx, alice, tt.in); !errors.Is(err, apperr.ErrInvalid) {
			t.Errorf("%s: err = %v, want ErrInvalid", tt.name, err)
		}
	}
}
