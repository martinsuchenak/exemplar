package scrub_test

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/martinsuchenak/exemplar/internal/account"
	"github.com/martinsuchenak/exemplar/internal/anonymise"
	"github.com/martinsuchenak/exemplar/internal/anonymise/anonymisetest"
	"github.com/martinsuchenak/exemplar/internal/app"
	"github.com/martinsuchenak/exemplar/internal/apperr"
	"github.com/martinsuchenak/exemplar/internal/audit"
	"github.com/martinsuchenak/exemplar/internal/dataset"
	"github.com/martinsuchenak/exemplar/internal/ingest"
	"github.com/martinsuchenak/exemplar/internal/person"
	"github.com/martinsuchenak/exemplar/internal/principal"
	"github.com/martinsuchenak/exemplar/internal/scrub"
	"github.com/martinsuchenak/exemplar/internal/testutil"
)

var ctx = context.Background()

// secrets are the real values in the test files; none may come back.
var secrets = []string{"Priya", "Shah", "Tom", "Reid", "acmeplumbing", "0412 345 678"}

func newApp(t *testing.T, withKey bool, maxBytes int64) *app.App {
	return testutil.NewAppWith(t, func(c *app.Config) {
		if withKey {
			c.AnonKey = anonymisetest.Key
		}
		c.AnonDetector = anonymisetest.Detector{Names: []string{"Priya", "Tom"}}
		c.MaxIngestBytes = maxBytes
	})
}

func files() []anonymise.File {
	return []anonymise.File{
		{Name: "one.eml", Data: anonymisetest.Email("Priya Shah", "Tom Reid", "acmeplumbing.com.au", "Leak", "Hi Tom, call me on 0412 345 678. Priya")},
		{Name: "notes.md", Data: []byte("# Leak\n\nTom Reid met Priya Shah. 0412 345 678\n")},
		{Name: "report.pdf", Data: []byte("%PDF")},
		{Name: "notes.md", Data: []byte("Priya again\n")},
	}
}

// readOnly makes a member key that may only read.
func readOnly(t *testing.T, a *app.App, admin principal.Principal) principal.Principal {
	t.Helper()
	acc, err := a.Accounts.CreateAccount(ctx, admin, account.CreateAccountInput{Name: "reader", Kind: account.KindDeveloper})
	if err != nil {
		t.Fatal(err)
	}
	issued, err := a.Accounts.CreateKey(ctx, admin, account.CreateKeyInput{AccountID: acc.ID, Name: "r", Scopes: []principal.Scope{principal.ScopeRead}})
	if err != nil {
		t.Fatal(err)
	}
	return testutil.Authenticate(t, a, issued.Secret)
}

func TestAnyKeyAnonymisesAndNothingIsStored(t *testing.T) {
	a := newApp(t, false, 0)
	admin := testutil.Admin(t, a)
	reader := readOnly(t, a, admin)

	res, err := a.Scrub.Anonymise(ctx, reader, scrub.Request{}, files())
	if err != nil {
		t.Fatal(err)
	}
	if res.Profile != "default" || res.Linked || len(res.Files) != 4 {
		t.Fatalf("result = %+v", res)
	}
	if f := res.Files[2]; f.Error == "" || !strings.Contains(f.Error, "cannot anonymise .pdf") || f.Data != nil {
		t.Errorf("pdf = %+v", f)
	}
	for i, f := range []scrub.FileResult{res.Files[0], res.Files[1], res.Files[3]} {
		if f.Error != "" || f.Replacements == 0 || f.Report != nil {
			t.Errorf("file %d = %+v", i, f)
		}
		for _, s := range secrets {
			if strings.Contains(string(f.Data), s) {
				t.Errorf("%s still holds %q:\n%s", f.Name, s, f.Data)
			}
		}
	}
	// Formats and names: two inputs called notes.md get distinct fakes.
	if res.Files[0].Format != anonymise.FormatEmail || !strings.HasPrefix(res.Files[0].Name, "email-") ||
		res.Files[1].Format != anonymise.FormatMarkdown || res.Files[3].Name != strings.TrimSuffix(res.Files[1].Name, ".md")+"-2.md" {
		t.Errorf("names = %s, %s, %s", res.Files[0].Name, res.Files[1].Name, res.Files[3].Name)
	}

	// Nothing stored: no people, no datasets, no fixtures.
	if people, _ := a.Persons.List(ctx, admin, person.ListFilter{}); len(people) != 0 {
		t.Errorf("the registry has %d people", len(people))
	}
	if ds, _ := a.Datasets.List(ctx, admin, dataset.ListFilter{}); len(ds) != 0 {
		t.Errorf("%d datasets", len(ds))
	}

	// One audit event, counts only.
	events, _ := a.Audit.List(ctx, admin, audit.Filter{Action: audit.ActionFileAnonymise})
	if len(events) != 1 || events[0].AccountID != reader.AccountID || events[0].TargetID != reader.AccountID {
		t.Fatalf("audit = %+v", events)
	}
	d := events[0].Details
	if d["files"] != float64(4) || d["failed"] != float64(1) || d["linked"] != false || d["profile"] != "default" || d["key_id"] != nil {
		t.Errorf("audit details = %v", d)
	}
	if formats, _ := d["formats"].([]any); len(formats) != 2 || formats[0] != "eml" || formats[1] != "md" {
		t.Errorf("audit formats = %v", d["formats"])
	}
	for _, v := range d {
		if s, ok := v.(string); ok && strings.Contains(s, "Priya") {
			t.Errorf("audit holds original data: %v", d)
		}
	}
}

// Without linking, each run's fakes are its own: they match neither another
// run's nor the library's, so the tool cannot test whether a name is there.
func TestFakesAreUnlinkedByDefault(t *testing.T) {
	a := newApp(t, true, 0)
	admin := testutil.Admin(t, a)
	member := testutil.Member(t, a, admin, "alice")
	mail := files()[:1]

	first, err := a.Scrub.Anonymise(ctx, member, scrub.Request{}, mail)
	if err != nil {
		t.Fatal(err)
	}
	second, err := a.Scrub.Anonymise(ctx, member, scrub.Request{}, mail)
	if err != nil {
		t.Fatal(err)
	}
	q, err := a.Datasets.Create(ctx, admin, dataset.CreateInput{Name: "q", Scope: dataset.ScopeQuarantine})
	if err != nil {
		t.Fatal(err)
	}
	ingested, err := a.Ingest.Ingest(ctx, admin, q.ID, ingest.Request{DryRun: true}, []ingest.File{{Name: mail[0].Name, Data: mail[0].Data}})
	if err != nil {
		t.Fatal(err)
	}
	libraryFake := ""
	for _, l := range ingested.Files[0].Report {
		if l.Original == "Priya Shah" {
			libraryFake = l.Fake
		}
	}
	if libraryFake == "" {
		t.Fatal("ingest report has no fake for Priya Shah")
	}
	if string(first.Files[0].Data) == string(second.Files[0].Data) {
		t.Error("two unlinked runs gave the same fakes")
	}
	if strings.Contains(string(first.Files[0].Data), libraryFake) || strings.Contains(string(second.Files[0].Data), libraryFake) {
		t.Errorf("an unlinked run used the library's fake %q", libraryFake)
	}
	// File names are keyed too, with each run's own key.
	if first.Files[0].Name == second.Files[0].Name {
		t.Error("two unlinked runs gave the same fake file name")
	}
}

// Linked fakes are ingest's: an admin key, the server's key and registry,
// new people saved.
func TestLinkedFakesMatchTheLibrary(t *testing.T) {
	a := newApp(t, true, 0)
	admin := testutil.Admin(t, a)
	mail := files()[:1]
	res, err := a.Scrub.Anonymise(ctx, admin, scrub.Request{LinkRegistry: true}, mail)
	if err != nil || !res.Linked {
		t.Fatalf("linked = %+v, %v", res, err)
	}
	people, _ := a.Persons.List(ctx, admin, person.ListFilter{})
	if len(people) != 2 {
		t.Fatalf("linked run saved %d people, want the 2 correspondents", len(people))
	}
	again, err := a.Scrub.Anonymise(ctx, admin, scrub.Request{LinkRegistry: true}, mail)
	if err != nil || string(again.Files[0].Data) != string(res.Files[0].Data) {
		t.Errorf("linked runs differ: %v", err)
	}
	for _, p := range people {
		if !strings.Contains(string(res.Files[0].Data), p.Email) {
			t.Errorf("output does not use the registry person %s", p.Email)
		}
	}
	key, _ := anonymise.NewKey(anonymisetest.Key)
	events, _ := a.Audit.List(ctx, admin, audit.Filter{Action: audit.ActionFileAnonymise})
	if len(events) != 2 || events[0].Details["linked"] != true || events[0].Details["key_id"] != key.ID {
		t.Errorf("audit = %+v", events)
	}
}

func TestAnonymiseRules(t *testing.T) {
	a := newApp(t, false, 1024)
	admin := testutil.Admin(t, a)
	member := testutil.Member(t, a, admin, "alice")
	one := files()[:1]
	for _, tt := range []struct {
		name  string
		actor principal.Principal
		req   scrub.Request
		files []anonymise.File
		want  error
		msg   string
	}{
		{"no key", principal.Principal{}, scrub.Request{}, one, apperr.ErrForbidden, "can read"},
		{"unknown profile", member, scrub.Request{Profile: "nope"}, one, apperr.ErrInvalid, "unknown profile"},
		{"no files", member, scrub.Request{}, nil, apperr.ErrInvalid, "send 1-1000"},
		{"too many files", member, scrub.Request{}, make([]anonymise.File, scrub.MaxFiles+1), apperr.ErrInvalid, "send 1-1000"},
		{"too large", member, scrub.Request{}, []anonymise.File{{Name: "big.txt", Data: make([]byte, 2048)}}, apperr.ErrTooLarge, "over 1024 bytes"},
		{"report", member, scrub.Request{Report: true}, one, apperr.ErrInvalid, "never returns original data"},
		{"link as member", member, scrub.Request{LinkRegistry: true}, one, apperr.ErrForbidden, "admin key"},
		{"link without key", admin, scrub.Request{LinkRegistry: true}, one, apperr.ErrInvalid, "none is configured"},
	} {
		_, err := a.Scrub.Anonymise(ctx, tt.actor, tt.req, tt.files)
		if !errors.Is(err, tt.want) || !strings.Contains(err.Error(), tt.msg) {
			t.Errorf("%s: err = %v, want %v (%q)", tt.name, err, tt.want, tt.msg)
		}
	}
	if a.Scrub.MaxBytes() != 1024 {
		t.Errorf("max bytes = %d", a.Scrub.MaxBytes())
	}

	// A server with no Presidio has no tool, even with a key.
	plain := testutil.NewAppWith(t, func(c *app.Config) { c.AnonKey = anonymisetest.Key })
	padmin := testutil.Admin(t, plain)
	if _, err := plain.Scrub.Anonymise(ctx, padmin, scrub.Request{}, one); !errors.Is(err, apperr.ErrInvalid) || !strings.Contains(err.Error(), "not configured") {
		t.Errorf("unconfigured err = %v", err)
	}
}

// blockingDetector holds every call until release is closed.
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

func TestOneRunAtATimePerKey(t *testing.T) {
	d := &blockingDetector{started: make(chan struct{}), release: make(chan struct{})}
	a := testutil.NewAppWith(t, func(c *app.Config) { c.AnonDetector = d })
	admin := testutil.Admin(t, a)
	alice := testutil.Member(t, a, admin, "alice")
	doc := []anonymise.File{{Name: "a.txt", Data: []byte("text")}}

	done := make(chan error)
	go func() {
		_, err := a.Scrub.Anonymise(ctx, alice, scrub.Request{}, doc)
		done <- err
	}()
	<-d.started
	if _, err := a.Scrub.Anonymise(ctx, alice, scrub.Request{}, doc); !errors.Is(err, apperr.ErrBusy) {
		t.Errorf("second run for the same key err = %v", err)
	}
	// Another key is not held up by alice's run.
	other := make(chan error)
	go func() {
		_, err := a.Scrub.Anonymise(ctx, admin, scrub.Request{}, doc)
		other <- err
	}()
	close(d.release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if err := <-other; err != nil {
		t.Errorf("another key's run: %v", err)
	}
	// Once done, the key may run again.
	if _, err := a.Scrub.Anonymise(ctx, alice, scrub.Request{}, doc); err != nil {
		t.Errorf("after the run: %v", err)
	}
}

// A run can be claimed before its files are read (the handler does, before
// reading the upload), and only one at a time per key.
func TestReserveClaimsTheRun(t *testing.T) {
	a := newApp(t, false, 0)
	admin := testutil.Admin(t, a)
	alice := testutil.Member(t, a, admin, "alice")
	run, err := a.Scrub.Reserve(alice)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.Scrub.Reserve(alice); !errors.Is(err, apperr.ErrBusy) {
		t.Errorf("second reserve err = %v", err)
	}
	if _, err := a.Scrub.Anonymise(ctx, alice, scrub.Request{}, files()[1:2]); !errors.Is(err, apperr.ErrBusy) {
		t.Errorf("anonymise while reserved err = %v", err)
	}
	res, err := run.Anonymise(ctx, scrub.Request{}, files()[1:2])
	if err != nil || len(res.Files) != 1 || res.Files[0].Error != "" {
		t.Fatalf("reserved run = %+v, %v", res, err)
	}
	run.Close()
	run.Close() // closing twice is harmless
	if _, err := run.Anonymise(ctx, scrub.Request{}, files()[1:2]); err == nil {
		t.Error("a closed run ran")
	}
	next, err := a.Scrub.Reserve(alice)
	if err != nil {
		t.Fatalf("after close: %v", err)
	}
	next.Close()

	// What needs no files is checked when reserving.
	if _, err := newApp(t, false, 0).Scrub.Reserve(principal.Principal{}); !errors.Is(err, apperr.ErrForbidden) {
		t.Errorf("no read scope err = %v", err)
	}
	unconfigured := testutil.NewAppWith(t, func(*app.Config) {})
	if _, err := unconfigured.Scrub.Reserve(testutil.Admin(t, unconfigured)); !errors.Is(err, apperr.ErrInvalid) {
		t.Errorf("unconfigured err = %v", err)
	}
}

func TestLocalMode(t *testing.T) {
	local, err := app.LocalAnonymiser(app.LocalConfig{Detector: anonymisetest.Detector{Names: []string{"Priya", "Tom"}}})
	if err != nil {
		t.Fatal(err)
	}
	// No key, no principal: the person running it owns the files.
	res, err := local.Anonymise(ctx, principal.Principal{}, scrub.Request{Report: true}, files()[:2])
	if err != nil {
		t.Fatal(err)
	}
	report := res.Files[1].Report
	if len(report) == 0 || res.Files[0].Error != "" {
		t.Fatalf("local result = %+v", res)
	}
	found := false
	for _, l := range report {
		found = found || l.Original == "0412 345 678"
	}
	if !found {
		t.Errorf("report = %+v", report)
	}
	if _, err := local.Anonymise(ctx, principal.Principal{}, scrub.Request{LinkRegistry: true}, files()[:1]); !errors.Is(err, apperr.ErrInvalid) || !strings.Contains(err.Error(), "drop --local") {
		t.Errorf("local link err = %v", err)
	}
	// Without a key file each run has its own fakes; with one they repeat.
	again, _ := local.Anonymise(ctx, principal.Principal{}, scrub.Request{}, files()[1:2])
	if string(again.Files[0].Data) == string(res.Files[1].Data) {
		t.Error("two keyless local runs gave the same fakes")
	}
	keyed, err := app.LocalAnonymiser(app.LocalConfig{Key: anonymisetest.Key, Detector: anonymisetest.Detector{Names: []string{"Priya"}}})
	if err != nil {
		t.Fatal(err)
	}
	x, _ := keyed.Anonymise(ctx, principal.Principal{}, scrub.Request{}, files()[1:2])
	y, _ := keyed.Anonymise(ctx, principal.Principal{}, scrub.Request{}, files()[1:2])
	if string(x.Files[0].Data) != string(y.Files[0].Data) {
		t.Error("local runs with one key file differ")
	}
	// No detector: say how to get one.
	bare, _ := app.LocalAnonymiser(app.LocalConfig{})
	if _, err := bare.Anonymise(ctx, principal.Principal{}, scrub.Request{}, files()[:1]); err == nil || !strings.Contains(err.Error(), "--patterns-only") {
		t.Errorf("no detector err = %v", err)
	}
	// Patterns only still catches addresses and numbers, and header names.
	po, _ := app.LocalAnonymiser(app.LocalConfig{PatternsOnly: true})
	out, err := po.Anonymise(ctx, principal.Principal{}, scrub.Request{}, files()[:2])
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range out.Files {
		for _, s := range []string{"acmeplumbing", "0412 345 678", "Priya Shah", "Tom Reid"} {
			if strings.Contains(string(f.Data), s) {
				t.Errorf("patterns only kept %q in %s:\n%s", s, f.Name, f.Data)
			}
		}
	}
}

func TestPlainName(t *testing.T) {
	for name, want := range map[string]bool{
		"document-3f9a1c.md": true, "a.html.twig": true,
		"": false, ".": false, "..": false, "../x": false, "a/b": false, `a\b`: false, "/etc/passwd": false, "a\x00b": false,
	} {
		if scrub.PlainName(name) != want {
			t.Errorf("PlainName(%q) = %v", name, !want)
		}
	}
}
