package ingest_test

import (
	"context"
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
		{Name: "notes.txt", Data: []byte("text")},
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
	if !strings.Contains(byName["outlook.msg"].Error, "MSG") || !strings.Contains(byName["notes.txt"].Error, ".eml") {
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
