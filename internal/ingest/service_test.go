package ingest_test

import (
	"archive/zip"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/martinsuchenak/exemplar/internal/anonymise"
	"github.com/martinsuchenak/exemplar/internal/anonymise/anonymisetest"
	"github.com/martinsuchenak/exemplar/internal/app"
	"github.com/martinsuchenak/exemplar/internal/apperr"
	"github.com/martinsuchenak/exemplar/internal/audit"
	"github.com/martinsuchenak/exemplar/internal/dataset"
	"github.com/martinsuchenak/exemplar/internal/fixture"
	"github.com/martinsuchenak/exemplar/internal/ingest"
	"github.com/martinsuchenak/exemplar/internal/person"
	"github.com/martinsuchenak/exemplar/internal/principal"
	"github.com/martinsuchenak/exemplar/internal/testutil"
)

var ctx = context.Background()

// secrets are the real values in the test emails; none may be stored.
var secrets = []string{"Priya", "Shah", "Tom", "Reid", "acmeplumbing", "0412 345 678"}

func newApp(t *testing.T, maxBytes int64) *app.App {
	return testutil.NewAppWith(t, func(c *app.Config) {
		c.AnonKey = anonymisetest.Key
		c.AnonDetector = anonymisetest.Detector{Names: []string{"Priya", "Tom"}}
		c.MaxIngestBytes = maxBytes
	})
}

// Ingest draws on the same run budget as the anonymise tool, and the cap
// is checked before the upload is read.
func TestBeginSharesTheRunBudget(t *testing.T) {
	a := testutil.NewAppWith(t, func(c *app.Config) {
		c.AnonKey = anonymisetest.Key
		c.AnonDetector = anonymisetest.Detector{Names: []string{"Priya"}}
		c.MaxConcurrentRuns = 1
	})
	admin := testutil.Admin(t, a)
	release, err := a.Ingest.Begin(admin)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.Ingest.Begin(admin); !errors.Is(err, apperr.ErrBusy) {
		t.Errorf("second begin err = %v", err)
	}
	// The anonymise tool holds the same budget, so it is busy too.
	if _, err := a.Scrub.Reserve(admin); !errors.Is(err, apperr.ErrBusy) {
		t.Errorf("scrub reserve while ingest holds the budget err = %v", err)
	}
	release()
	release2, err := a.Ingest.Begin(admin)
	if err != nil {
		t.Fatalf("begin after release: %v", err)
	}
	release2()
}

// A caller that could never ingest is refused before it takes a slot, so a
// member key cannot hold the shared budget with uploads it may not make.
func TestBeginChecksBeforeTakingASlot(t *testing.T) {
	a := testutil.NewAppWith(t, func(c *app.Config) {
		c.AnonKey = anonymisetest.Key
		c.AnonDetector = anonymisetest.Detector{Names: []string{"Priya"}}
		c.MaxConcurrentRuns = 1
	})
	admin := testutil.Admin(t, a)
	member := testutil.Member(t, a, admin, "alice")
	for range 2 {
		if _, err := a.Ingest.Begin(member); !errors.Is(err, apperr.ErrForbidden) {
			t.Fatalf("member begin err = %v", err)
		}
	}
	release, err := a.Ingest.Begin(admin)
	if err != nil {
		t.Fatalf("admin begin after refused members: %v", err)
	}
	release()

	// A server without the anonymiser takes no slot either.
	bare := testutil.NewAppWith(t, func(c *app.Config) { c.MaxConcurrentRuns = 1 })
	bareAdmin := testutil.Admin(t, bare)
	for range 2 {
		if _, err := bare.Ingest.Begin(bareAdmin); !errors.Is(err, apperr.ErrInvalid) {
			t.Fatalf("unconfigured begin err = %v", err)
		}
	}
}

func quarantine(t *testing.T, a *app.App, admin principal.Principal) string {
	t.Helper()
	d, err := a.Datasets.Create(ctx, admin, dataset.CreateInput{Name: "inbox-sample", Scope: dataset.ScopeQuarantine})
	if err != nil {
		t.Fatal(err)
	}
	return d.ID
}

func emails() []ingest.File {
	return []ingest.File{
		{Name: "one.eml", Data: anonymisetest.Email("Priya Shah", "Tom Reid", "acmeplumbing.com.au", "Leak at site", "Hi Tom, call me on 0412 345 678. Priya")},
		{Name: "outlook.msg", Data: []byte("binary")},
		{Name: "report.pdf", Data: []byte("%PDF")},
		{Name: "TWO.EML", Data: anonymisetest.Email("Tom Reid", "Priya Shah", "acmeplumbing.com.au", "Re Leak at site", "Thanks Priya, fixed. Tom")},
	}
}

func content(t *testing.T, a *app.App, p principal.Principal, id string) string {
	t.Helper()
	rc, _, err := a.Fixtures.Open(ctx, p, id)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rc.Close() }()
	b, _ := io.ReadAll(rc)
	return string(b)
}

// officeText returns every entry of a DOCX or XLSX, so a secret is found
// where compression would hide it; anything else as it is.
func officeText(t *testing.T, body string) string {
	t.Helper()
	zr, err := zip.NewReader(strings.NewReader(body), int64(len(body)))
	if err != nil {
		return body
	}
	var all strings.Builder
	for _, f := range zr.File {
		rc, err := f.Open()
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(rc)
		_ = rc.Close()
		all.WriteString(f.Name + "\n" + string(b) + "\n")
	}
	return all.String()
}

func TestIngestStoresAnonymisedFixtures(t *testing.T) {
	a := newApp(t, 0)
	admin := testutil.Admin(t, a)
	dsID := quarantine(t, a, admin)

	res, err := a.Ingest.Ingest(ctx, admin, dsID, ingest.Request{Batch: "b1"}, emails())
	if err != nil {
		t.Fatal(err)
	}
	if res.Batch != "b1" || res.Profile != "default" || !strings.HasPrefix(res.KeyID, "k-") || res.DryRun || len(res.Files) != 4 {
		t.Fatalf("result = %+v", res)
	}
	byName := map[string]ingest.FileResult{}
	for _, f := range res.Files {
		byName[f.Name] = f
	}
	if !strings.Contains(byName["outlook.msg"].Error, "MSG") || !strings.Contains(byName["report.pdf"].Error, "cannot anonymise .pdf") {
		t.Errorf("rejections = %+v", res.Files)
	}
	if byName["one.eml"].FixtureName != "b1-001.eml" || byName["TWO.EML"].FixtureName != "b1-002.eml" || byName["one.eml"].Replacements == 0 || byName["one.eml"].Report != nil {
		t.Errorf("stored = %+v", res.Files)
	}

	fixtures, err := a.Fixtures.List(ctx, admin, dsID, fixture.ListFilter{})
	if err != nil || len(fixtures) != 2 {
		t.Fatalf("fixtures = %d, %v", len(fixtures), err)
	}
	for _, f := range fixtures {
		if f.Source != fixture.SourceAnonymised || f.ReviewState != fixture.ReviewUnreviewed || f.AnonProfile != "default" ||
			f.AnonKeyID != res.KeyID || f.IngestBatch != "b1" || len(f.People) != 2 {
			t.Errorf("fixture = %+v", f)
		}
		body := content(t, a, admin, f.ID)
		for _, s := range secrets {
			if strings.Contains(body, s) {
				t.Errorf("%s still contains %q:\n%s", f.Name, s, body)
			}
		}
	}

	people, _ := a.Persons.List(ctx, admin, person.ListFilter{})
	if len(people) != 2 {
		t.Fatalf("registry has %d people, want the 2 correspondents", len(people))
	}

	// The same people in a later batch map to the same registry people.
	again, err := a.Ingest.Ingest(ctx, admin, dsID, ingest.Request{Batch: "b2"}, emails()[:1])
	if err != nil || again.Files[0].FixtureName != "b2-001.eml" {
		t.Fatalf("second ingest = %+v, %v", again, err)
	}
	if people, _ := a.Persons.List(ctx, admin, person.ListFilter{}); len(people) != 2 {
		t.Errorf("registry grew to %d people", len(people))
	}
	first := content(t, a, admin, byName["one.eml"].FixtureID)
	if second := content(t, a, admin, again.Files[0].FixtureID); first != second {
		t.Errorf("the same email anonymised differently:\n%s\n---\n%s", first, second)
	}

	events, _ := a.Audit.List(ctx, admin, audit.Filter{Action: audit.ActionFixtureIngest})
	if len(events) != 2 || events[0].TargetID != dsID {
		t.Fatalf("audit = %+v", events)
	}
	var b1 audit.Event
	for _, e := range events {
		if e.Details["batch"] == "b1" {
			b1 = e
		}
	}
	if b1.Details["stored"] != float64(2) || b1.Details["failed"] != float64(2) || b1.Details["files"] != float64(4) || b1.Details["dry_run"] != false {
		t.Errorf("audit details = %v", b1.Details)
	}
	for _, e := range events {
		for _, v := range e.Details {
			if s, ok := v.(string); ok && strings.Contains(s, "Priya") {
				t.Errorf("audit holds original data: %v", e.Details)
			}
		}
	}

	// Review gate: only approved fixtures leave quarantine.
	if _, err := a.Fixtures.Review(ctx, admin, byName["one.eml"].FixtureID, true); err != nil {
		t.Fatal(err)
	}
	promoted, err := a.Datasets.Promote(ctx, admin, dsID, dataset.PromoteInput{Name: "inbox-approved"})
	if err != nil || promoted.Copied != 1 || promoted.Skipped != 2 {
		t.Fatalf("promote = %+v, %v", promoted, err)
	}

	n, err := a.Fixtures.PurgeBatch(ctx, admin, "b2")
	if err != nil || n != 1 {
		t.Errorf("purge = %d, %v", n, err)
	}
}

// TestIngestStoresDocuments checks that documents go through the same
// batch as emails: names from the emails' headers are found in them, and
// each is stored under its own extension, linked to no one.
func TestIngestStoresDocuments(t *testing.T) {
	a := newApp(t, 0)
	admin := testutil.Admin(t, a)
	dsID := quarantine(t, a, admin)
	files := []ingest.File{
		emails()[0],
		{Name: "Site notes.MD", Data: []byte("# Leak\n\nTom Reid met Priya Shah. Call 0412 345 678.\n")},
		{Name: "contacts.csv", Data: []byte("Name,Phone\nTom Reid,0412 345 678\n")},
		{Name: "letter.html.twig", Data: []byte("<p>Dear {{ name }}, from Priya Shah</p>")},
		{Name: "Quote.DOCX", Data: anonymisetest.DOCX("Quote for Tom Reid, 0412 345 678")},
		{Name: "letter.rtf", Data: []byte(`{\rtf1\ansi{\info{\author Priya Shah}}\pard Dear Tom Reid, call 0412 345 678.\par}`)},
		{Name: "Budget.xlsx", Data: anonymisetest.XLSX("Tom Reid owes $1,200, call 0412 345 678")},
	}
	res, err := a.Ingest.Ingest(ctx, admin, dsID, ingest.Request{Batch: "docs"}, files)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"docs-001.eml", "docs-002.md", "docs-003.csv", "docs-004.html.twig", "docs-005.docx", "docs-006.rtf", "docs-007.xlsx"}
	for i, f := range res.Files {
		dropped := 0
		if strings.HasSuffix(want[i], ".docx") || strings.HasSuffix(want[i], ".xlsx") {
			dropped = 1 // its macros
		}
		if f.Error != "" || f.FixtureName != want[i] || f.Replacements == 0 || f.DroppedParts != dropped {
			t.Errorf("file %d = %+v, want %s", i, f, want[i])
		}
	}
	fixtures, err := a.Fixtures.List(ctx, admin, dsID, fixture.ListFilter{})
	if err != nil || len(fixtures) != 7 {
		t.Fatalf("fixtures = %d, %v", len(fixtures), err)
	}
	for _, f := range fixtures {
		body := officeText(t, content(t, a, admin, f.ID))
		for _, s := range secrets {
			if strings.Contains(body, s) {
				t.Errorf("%s still contains %q:\n%s", f.Name, s, body)
			}
		}
		if f.Name == "docs-001.eml" {
			continue
		}
		if f.Kind != fixture.KindFile || f.Source != fixture.SourceAnonymised || f.ReviewState != fixture.ReviewUnreviewed || len(f.People) != 0 {
			t.Errorf("document fixture = %+v", f)
		}
		if f.Name == "docs-004.html.twig" && !strings.Contains(body, "<p>Dear {{ name }}, from ") {
			t.Errorf("template code changed: %s", body)
		}
	}
}

// A profile with attachments = "anonymise" rewrites a readable attachment
// in place instead of replacing it.
func TestIngestRewritesAttachmentsWhenTheProfileSays(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "keep.toml"), []byte("name = \"keep-attachments\"\nattachments = \"anonymise\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	a := testutil.NewAppWith(t, func(c *app.Config) {
		c.AnonKey = anonymisetest.Key
		c.AnonDetector = anonymisetest.Detector{Names: []string{"Priya", "Tom"}}
		c.AnonProfilesDir = dir
	})
	admin := testutil.Admin(t, a)
	dsID := quarantine(t, a, admin)
	mail := "From: Priya Shah <priya.shah@acmeplumbing.com.au>\r\nTo: Tom Reid <tom.reid@acmeplumbing.com.au>\r\nSubject: List\r\n" +
		"MIME-Version: 1.0\r\nContent-Type: multipart/mixed; boundary=\"B\"\r\n\r\n--B\r\nContent-Type: text/plain\r\n\r\nHi Tom\r\n" +
		"--B\r\nContent-Type: text/csv; name=\"list.csv\"\r\nContent-Disposition: attachment; filename=\"list.csv\"\r\n\r\nName,Phone\r\nPriya Shah,0412 345 678\r\n--B--\r\n"
	files := []ingest.File{{Name: "a.eml", Data: []byte(mail)}}
	for profile, want := range map[string][2]int{"default": {1, 0}, "keep-attachments": {0, 1}} {
		res, err := a.Ingest.Ingest(ctx, admin, dsID, ingest.Request{Profile: profile, DryRun: true}, files)
		if err != nil {
			t.Fatal(err)
		}
		if f := res.Files[0]; f.Error != "" || f.Attachments != want[0] || f.AttachmentsRewritten != want[1] {
			t.Errorf("%s: %+v", profile, f)
		}
	}
}

func TestDryRunLeavesNoTrace(t *testing.T) {
	a := newApp(t, 0)
	admin := testutil.Admin(t, a)
	dsID := quarantine(t, a, admin)

	res, err := a.Ingest.Ingest(ctx, admin, dsID, ingest.Request{DryRun: true}, emails())
	if err != nil {
		t.Fatal(err)
	}
	if !res.DryRun || !strings.HasPrefix(res.Batch, "ingest-") {
		t.Errorf("result = %+v", res)
	}
	var originals []string
	for _, f := range res.Files {
		if f.FixtureID != "" || f.FixtureName != "" {
			t.Errorf("dry run stored %+v", f)
		}
		for i, l := range f.Report {
			originals = append(originals, l.Original)
			if l.Entity == "" || l.Fake == "" || l.Fake == l.Original {
				t.Errorf("report line = %+v", l)
			}
			if i > 0 && f.Report[i-1].Entity > l.Entity {
				t.Errorf("report not sorted by entity")
			}
		}
	}
	if joined := strings.Join(originals, "|"); !strings.Contains(joined, "Priya") || !strings.Contains(joined, "0412 345 678") {
		t.Errorf("report misses originals: %s", joined)
	}

	if fx, _ := a.Fixtures.List(ctx, admin, dsID, fixture.ListFilter{}); len(fx) != 0 {
		t.Errorf("dry run left %d fixtures", len(fx))
	}
	if people, _ := a.Persons.List(ctx, admin, person.ListFilter{}); len(people) != 0 {
		t.Errorf("dry run left %d people", len(people))
	}
	events, _ := a.Audit.List(ctx, admin, audit.Filter{Action: audit.ActionFixtureIngest})
	if len(events) != 1 || events[0].Details["dry_run"] != true || events[0].Details["stored"] != float64(0) {
		t.Errorf("audit = %+v", events)
	}
}

func TestIngestRules(t *testing.T) {
	a := newApp(t, 1024)
	admin := testutil.Admin(t, a)
	alice := testutil.Member(t, a, admin, "alice")
	dsID := quarantine(t, a, admin)
	ws, err := a.Datasets.Create(ctx, admin, dataset.CreateInput{Name: "workspace"})
	if err != nil {
		t.Fatal(err)
	}
	one := emails()[:1]

	if a.Ingest.MaxBytes() != 1024 || strings.Join(a.Ingest.Profiles(), ",") != "default" {
		t.Errorf("max bytes %d, profiles %v", a.Ingest.MaxBytes(), a.Ingest.Profiles())
	}
	for _, tt := range []struct {
		name  string
		actor principal.Principal
		ds    string
		req   ingest.Request
		files []ingest.File
		want  error
	}{
		{"member", alice, dsID, ingest.Request{}, one, apperr.ErrForbidden},
		{"unknown profile", admin, dsID, ingest.Request{Profile: "nope"}, one, apperr.ErrInvalid},
		{"bad batch", admin, dsID, ingest.Request{Batch: "../x"}, one, apperr.ErrInvalid},
		{"no files", admin, dsID, ingest.Request{}, nil, apperr.ErrInvalid},
		{"too many files", admin, dsID, ingest.Request{}, make([]ingest.File, ingest.MaxFiles+1), apperr.ErrInvalid},
		{"too large", admin, dsID, ingest.Request{}, []ingest.File{{Name: "big.eml", Data: make([]byte, 2048)}}, apperr.ErrTooLarge},
		{"not quarantine", admin, ws.ID, ingest.Request{}, one, apperr.ErrInvalid},
		{"missing dataset", admin, "missing", ingest.Request{}, one, apperr.ErrNotFound},
	} {
		if _, err := a.Ingest.Ingest(ctx, tt.actor, tt.ds, tt.req, tt.files); !errors.Is(err, tt.want) {
			t.Errorf("%s: err = %v, want %v", tt.name, err, tt.want)
		}
	}

	bad, err := a.Ingest.Ingest(ctx, admin, dsID, ingest.Request{}, []ingest.File{{Name: "junk.eml", Data: []byte("not an email")}})
	if err != nil || bad.Files[0].Error == "" {
		t.Errorf("a bad email should fail alone: %+v, %v", bad, err)
	}

	plain := testutil.NewApp(t)
	padmin := testutil.Admin(t, plain)
	if _, err := plain.Ingest.Ingest(ctx, padmin, "x", ingest.Request{}, one); !errors.Is(err, apperr.ErrInvalid) || !strings.Contains(err.Error(), "not configured") {
		t.Errorf("unconfigured err = %v", err)
	}
	if plain.Ingest.Profiles() != nil {
		t.Error("unconfigured server lists profiles")
	}
}

// When the detector fails, the admin learns only that it is unavailable: the
// error's detail (the detector's address and what the transport made of it)
// stays on the server, and nothing is stored.
func TestDetectorFailureIsGeneric(t *testing.T) {
	leaks := []string{"127.0.0.1", "http://", "/analyze", "Presidio", "dial tcp", "connection refused", "model", "decoding", "reading the headers"}
	for name, d := range anonymisetest.FailingPresidios(t) {
		a := testutil.NewAppWith(t, func(c *app.Config) {
			c.AnonKey = anonymisetest.Key
			c.AnonDetector = d
		})
		admin := testutil.Admin(t, a)
		dsID := quarantine(t, a, admin)
		files := []ingest.File{emails()[0], {Name: "notes.md", Data: []byte("Priya called.\n")}}
		for _, dry := range []bool{false, true} {
			res, err := a.Ingest.Ingest(ctx, admin, dsID, ingest.Request{DryRun: dry}, files)
			if err != nil {
				t.Fatalf("%s: %v", name, err)
			}
			for _, f := range res.Files {
				if f.Error != anonymise.DetectorUnavailable || f.FixtureID != "" || f.Report != nil {
					t.Errorf("%s (dry run %v): file = %+v", name, dry, f)
				}
				for _, l := range leaks {
					if strings.Contains(f.Error, l) {
						t.Errorf("%s: the error shows %q: %s", name, l, f.Error)
					}
				}
			}
		}
		if fx, _ := a.Fixtures.List(ctx, admin, dsID, fixture.ListFilter{}); len(fx) != 0 {
			t.Errorf("%s: %d fixtures stored", name, len(fx))
		}
	}
}

// TestUnreadableEmailFailsAlone checks that an email over the parser's
// limits fails on its own while the rest of the batch is stored; it used to
// fail the whole batch when the batch's headers were read.
func TestUnreadableEmailFailsAlone(t *testing.T) {
	a := newApp(t, 0)
	admin := testutil.Admin(t, a)
	dsID := quarantine(t, a, admin)
	var deep strings.Builder
	deep.WriteString("From: a@b.com.au\r\nTo: c@d.com.au\r\nContent-Type: multipart/mixed; boundary=q0\r\n\r\n")
	for i := 0; i < 50; i++ {
		fmt.Fprintf(&deep, "--q%d\r\nContent-Type: multipart/mixed; boundary=q%d\r\n\r\n", i, i+1)
	}
	files := emails()
	files = []ingest.File{files[0], {Name: "deep.eml", Data: []byte(deep.String())}, files[3]}

	res, err := a.Ingest.Ingest(ctx, admin, dsID, ingest.Request{Batch: "b1"}, files)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Files[1].Error, "levels deep") || res.Files[1].FixtureID != "" {
		t.Errorf("deep email = %+v", res.Files[1])
	}
	if res.Files[0].FixtureID == "" || res.Files[2].FixtureID == "" {
		t.Errorf("the readable emails were not stored: %+v", res.Files)
	}
}

func TestReviewInFrozenQuarantine(t *testing.T) {
	a := newApp(t, 0)
	admin := testutil.Admin(t, a)
	dsID := quarantine(t, a, admin)
	res, err := a.Ingest.Ingest(ctx, admin, dsID, ingest.Request{}, emails()[:1])
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.Datasets.Freeze(ctx, admin, dsID); err != nil {
		t.Fatal(err)
	}
	f, err := a.Fixtures.Review(ctx, admin, res.Files[0].FixtureID, true)
	if err != nil || f.ReviewState != fixture.ReviewApproved {
		t.Errorf("review in a frozen quarantine = %+v, %v", f, err)
	}
	if _, err := a.Ingest.Ingest(ctx, admin, dsID, ingest.Request{}, emails()[:1]); err == nil {
		// Ingest into a frozen dataset must still fail.
		t.Error("ingest into a frozen quarantine succeeded")
	}
}
