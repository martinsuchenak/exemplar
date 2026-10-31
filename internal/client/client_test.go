package client_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/martinsuchenak/exemplar/cmd/routes"
	"github.com/martinsuchenak/exemplar/internal/account"
	"github.com/martinsuchenak/exemplar/internal/anonymise"
	"github.com/martinsuchenak/exemplar/internal/anonymise/anonymisetest"
	"github.com/martinsuchenak/exemplar/internal/app"
	"github.com/martinsuchenak/exemplar/internal/apperr"
	"github.com/martinsuchenak/exemplar/internal/audit"
	"github.com/martinsuchenak/exemplar/internal/client"
	"github.com/martinsuchenak/exemplar/internal/dataset"
	"github.com/martinsuchenak/exemplar/internal/fixture"
	"github.com/martinsuchenak/exemplar/internal/generate"
	"github.com/martinsuchenak/exemplar/internal/ingest"
	"github.com/martinsuchenak/exemplar/internal/person"
	"github.com/martinsuchenak/exemplar/internal/principal"
	"github.com/martinsuchenak/exemplar/internal/scrub"
	"github.com/martinsuchenak/exemplar/internal/synth"
	"github.com/martinsuchenak/exemplar/internal/testutil"
)

var ctx = context.Background()

// server starts the real routes on a test app and returns clients for the
// bootstrapped admin and a member.
func server(t *testing.T, configure func(*app.Config)) (admin, member *client.Client, a *app.App, url string) {
	t.Helper()
	a = testutil.NewAppWith(t, configure)
	_, issued, err := a.Accounts.Bootstrap(ctx, account.CreateAccountInput{Name: "root"})
	if err != nil {
		t.Fatalf("bootstrap: %v", err)
	}
	mux := http.NewServeMux()
	routes.RegisterRoutes(mux, a)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	admin = newClient(t, srv.URL, issued.Secret)
	acc, err := admin.CreateAccount(ctx, account.CreateAccountInput{Name: "alice", Kind: account.KindDeveloper})
	if err != nil {
		t.Fatalf("create alice: %v", err)
	}
	key, err := admin.CreateKey(ctx, account.CreateKeyInput{AccountID: acc.ID, Name: "laptop"})
	if err != nil {
		t.Fatalf("key for alice: %v", err)
	}
	return admin, newClient(t, srv.URL, key.Secret), a, srv.URL
}

func newClient(t *testing.T, url, key string) *client.Client {
	t.Helper()
	c, err := client.New(url, key)
	if err != nil {
		t.Fatalf("client.New: %v", err)
	}
	return c
}

func TestNewValidates(t *testing.T) {
	for _, tt := range []struct{ url, key string }{
		{"localhost:8080", "exm_x"},
		{"ftp://host", "exm_x"},
		{"http://", "exm_x"},
		{"http://localhost:8080", ""},
	} {
		if _, err := client.New(tt.url, tt.key); !errors.Is(err, apperr.ErrInvalid) {
			t.Errorf("New(%q, %q) err = %v, want ErrInvalid", tt.url, tt.key, err)
		}
	}
	if _, err := client.New("https://exemplar.example/base/", "exm_x"); err != nil {
		t.Errorf("valid URL with a path: %v", err)
	}
}

func TestAccountsAndKeys(t *testing.T) {
	admin, alice, _, url := server(t, nil)

	me, err := alice.Me(ctx)
	if err != nil || me.Account.Name != "alice" || me.Principal.IsAdmin() {
		t.Fatalf("me = %+v, %v", me, err)
	}
	accounts, err := admin.ListAccounts(ctx)
	if err != nil || len(accounts) != 2 {
		t.Errorf("admin accounts = %+v, %v", accounts, err)
	}
	if own, _ := alice.ListAccounts(ctx); len(own) != 1 {
		t.Errorf("alice sees %d accounts, want 1", len(own))
	}
	if _, err := alice.CreateAccount(ctx, account.CreateAccountInput{Name: "x", Kind: account.KindApp}); !errors.Is(err, apperr.ErrForbidden) {
		t.Errorf("member create account err = %v, want ErrForbidden", err)
	}

	spare, err := alice.CreateKey(ctx, account.CreateKeyInput{Name: "ci", Scopes: []principal.Scope{principal.ScopeRead}})
	if err != nil || spare.Secret == "" {
		t.Fatalf("create key = %+v, %v", spare, err)
	}
	keys, err := alice.ListKeys(ctx, "")
	if err != nil || len(keys) != 2 {
		t.Errorf("alice keys = %+v, %v", keys, err)
	}
	if adminView, err := admin.ListKeys(ctx, me.Account.ID); err != nil || len(adminView) != 2 {
		t.Errorf("admin listing alice's keys = %d, %v", len(adminView), err)
	}
	ro := newClient(t, url, spare.Secret)
	if _, err := ro.CreateDataset(ctx, dataset.CreateInput{Name: "x"}); !errors.Is(err, apperr.ErrForbidden) {
		t.Errorf("read-only key create dataset err = %v, want ErrForbidden", err)
	}
	if err := alice.RevokeKey(ctx, spare.Key.ID); err != nil {
		t.Fatalf("revoke: %v", err)
	}
	if _, err := ro.Me(ctx); !errors.Is(err, apperr.ErrUnauthenticated) {
		t.Errorf("revoked key err = %v, want ErrUnauthenticated", err)
	}

	if err := admin.DisableAccount(ctx, me.Account.ID); err != nil {
		t.Fatalf("disable: %v", err)
	}
	if _, err := alice.Me(ctx); !errors.Is(err, apperr.ErrUnauthenticated) {
		t.Errorf("disabled account err = %v, want ErrUnauthenticated", err)
	}
}

func TestDatasetsFixturesAndCopies(t *testing.T) {
	admin, alice, _, _ := server(t, nil)

	global, err := admin.CreateDataset(ctx, dataset.CreateInput{Name: "emails", Scope: dataset.ScopeGlobal})
	if err != nil {
		t.Fatalf("create global: %v", err)
	}
	up, err := admin.Upload(ctx, global.ID, client.UploadInput{Name: "hello.eml", Scenario: "greeting", Tags: []string{"a", "b"}}, strings.NewReader("Subject: hi\r\n\r\nhello"))
	if err != nil {
		t.Fatalf("upload: %v", err)
	}
	if up.Kind != fixture.KindEmail || up.Scenario != "greeting" || len(up.Tags) != 2 {
		t.Errorf("uploaded = %+v", up)
	}

	list, err := alice.ListFixtures(ctx, global.ID, fixture.ListFilter{Tag: "a", Kind: fixture.KindEmail, Format: "eml", Scenario: "greeting", ReviewState: fixture.ReviewNotRequired, Limit: 5, Offset: 0})
	if err != nil || len(list) != 1 {
		t.Fatalf("alice list = %+v, %v", list, err)
	}
	if list, err := alice.ListFixtures(ctx, global.ID, fixture.ListFilter{Query: "HELLO eml"}); err != nil || len(list) != 1 {
		t.Errorf("fixtures by query = %+v, %v", list, err)
	}
	if list, err := alice.ListDatasets(ctx, dataset.ListFilter{Query: "emai"}); err != nil || len(list) != 1 {
		t.Errorf("datasets by query = %+v, %v", list, err)
	}
	if got, err := alice.GetDataset(ctx, "emails"); err != nil || got.ID != global.ID {
		t.Errorf("dataset by name = %+v, %v", got, err)
	}
	if got, err := alice.GetFixture(ctx, up.ID); err != nil || got.Name != "hello.eml" {
		t.Errorf("get fixture = %+v, %v", got, err)
	}
	body, err := alice.Download(ctx, up.ID)
	if err != nil {
		t.Fatalf("download: %v", err)
	}
	b, _ := io.ReadAll(body)
	_ = body.Close()
	if string(b) != "Subject: hi\r\n\r\nhello" {
		t.Errorf("downloaded %q", b)
	}

	if _, err := alice.Upload(ctx, global.ID, client.UploadInput{Name: "x.txt"}, strings.NewReader("x")); !errors.Is(err, apperr.ErrForbidden) {
		t.Errorf("member upload to global err = %v, want ErrForbidden", err)
	}

	fork, err := alice.Fork(ctx, global.ID, dataset.ForkInput{Name: "my-emails"})
	if err != nil || fork.Copied != 1 || fork.Dataset.Scope != dataset.ScopeWorkspace {
		t.Fatalf("fork = %+v, %v", fork, err)
	}
	if got, err := alice.GetDataset(ctx, fork.Dataset.ID); err != nil || got.Name != "my-emails" {
		t.Errorf("get fork = %+v, %v", got, err)
	}
	if _, err := alice.Promote(ctx, fork.Dataset.ID, dataset.PromoteInput{}); !errors.Is(err, apperr.ErrForbidden) {
		t.Errorf("member promote err = %v, want ErrForbidden", err)
	}
	promoted, err := admin.Promote(ctx, fork.Dataset.ID, dataset.PromoteInput{Name: "emails", Description: "reviewed"})
	if err != nil || promoted.Dataset.Version != 2 || promoted.Dataset.FrozenAt == nil || promoted.Dataset.Description != "reviewed" {
		t.Errorf("promote = %+v, %v", promoted, err)
	}

	mine, err := alice.ListDatasets(ctx, dataset.ListFilter{Scope: dataset.ScopeWorkspace})
	if err != nil || len(mine) != 1 {
		t.Errorf("alice workspaces = %+v, %v", mine, err)
	}
	named, err := alice.ListDatasets(ctx, dataset.ListFilter{Name: "emails"})
	if err != nil || len(named) != 2 {
		t.Errorf("datasets named emails = %d, %v; want 2 versions", len(named), err)
	}
	frozen, err := alice.FreezeDataset(ctx, fork.Dataset.ID)
	if err != nil || frozen.FrozenAt == nil {
		t.Errorf("freeze = %+v, %v", frozen, err)
	}
	if err := alice.DeleteFixture(ctx, fork.Dataset.ID); !errors.Is(err, apperr.ErrNotFound) {
		t.Errorf("delete with a dataset id err = %v, want ErrNotFound", err)
	}
	if err := alice.DeleteDataset(ctx, fork.Dataset.ID); err != nil {
		t.Errorf("delete dataset: %v", err)
	}
	// The promoted version is frozen; the original global dataset is not.
	inPromoted, err := admin.ListFixtures(ctx, promoted.Dataset.ID, fixture.ListFilter{})
	if err != nil || len(inPromoted) != 1 {
		t.Fatalf("promoted fixtures = %+v, %v", inPromoted, err)
	}
	if err := admin.DeleteFixture(ctx, inPromoted[0].ID); !errors.Is(err, apperr.ErrConflict) {
		t.Errorf("delete from frozen promoted version err = %v, want ErrConflict", err)
	}
	if err := admin.DeleteFixture(ctx, up.ID); err != nil {
		t.Errorf("admin delete from unfrozen global: %v", err)
	}
}

func TestQuarantineReviewPurgeAndAudit(t *testing.T) {
	admin, alice, a, _ := server(t, nil)

	q, err := admin.CreateDataset(ctx, dataset.CreateInput{Name: "inbox", Scope: dataset.ScopeQuarantine})
	if err != nil {
		t.Fatalf("create quarantine: %v", err)
	}
	// Ingest is a later step; add a real-derived fixture through the service.
	adminP := testutil.Authenticate(t, a, mustAdminKey(t, admin))
	f, err := a.Fixtures.Create(ctx, adminP, fixture.CreateInput{DatasetID: q.ID, Name: "real.eml", Source: fixture.SourceAnonymised, IngestBatch: "b1", Content: strings.NewReader("x")})
	if err != nil {
		t.Fatalf("create anonymised: %v", err)
	}
	if _, err := alice.Review(ctx, f.ID, true); !errors.Is(err, apperr.ErrForbidden) {
		t.Errorf("member review err = %v, want ErrForbidden", err)
	}
	reviewed, err := admin.Review(ctx, f.ID, true)
	if err != nil || reviewed.ReviewState != fixture.ReviewApproved {
		t.Errorf("review = %+v, %v", reviewed, err)
	}
	if _, err := admin.Review(ctx, f.ID, false); !errors.Is(err, apperr.ErrConflict) {
		t.Errorf("second review err = %v, want ErrConflict", err)
	}

	if _, err := alice.Purge(ctx, "b1"); !errors.Is(err, apperr.ErrForbidden) {
		t.Errorf("member purge err = %v, want ErrForbidden", err)
	}
	res, err := admin.Purge(ctx, "b1")
	if err != nil || res.Deleted != 1 || res.IngestBatch != "b1" {
		t.Errorf("purge = %+v, %v", res, err)
	}

	events, err := admin.ListAudit(ctx, audit.Filter{Action: audit.ActionFixturePurge})
	if err != nil || len(events) != 1 {
		t.Errorf("purge events = %+v, %v", events, err)
	}
	all, err := admin.ListAudit(ctx, audit.Filter{})
	if err != nil {
		t.Fatalf("audit: %v", err)
	}
	page, err := admin.ListAudit(ctx, audit.Filter{AfterSeq: all[0].Seq, Limit: 2, AccountID: all[1].AccountID, TargetType: all[1].TargetType, TargetID: all[1].TargetID})
	if err != nil || len(page) != 1 || page[0].Seq != all[1].Seq {
		t.Errorf("filtered page = %+v, %v", page, err)
	}
	if _, err := alice.ListAudit(ctx, audit.Filter{}); !errors.Is(err, apperr.ErrForbidden) {
		t.Errorf("member audit err = %v, want ErrForbidden", err)
	}
}

// mustAdminKey issues an extra admin key, since the bootstrap secret is not
// kept by server().
func mustAdminKey(t *testing.T, admin *client.Client) string {
	t.Helper()
	k, err := admin.CreateKey(ctx, account.CreateKeyInput{Name: "svc", Scopes: []principal.Scope{principal.ScopeRead, principal.ScopeWrite, principal.ScopeAdmin}})
	if err != nil {
		t.Fatalf("admin key: %v", err)
	}
	return k.Secret
}

func TestErrorMapping(t *testing.T) {
	admin, alice, _, url := server(t, func(c *app.Config) { c.MaxFixtureBytes = 4 })

	ws, err := alice.CreateDataset(ctx, dataset.CreateInput{Name: "mine"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	tests := []struct {
		name string
		err  error
		kind error
	}{
		{"not found", func() error { _, err := alice.GetDataset(ctx, "missing"); return err }(), apperr.ErrNotFound},
		{"invalid", func() error { _, err := alice.CreateDataset(ctx, dataset.CreateInput{Name: ""}); return err }(), apperr.ErrInvalid},
		{"conflict", func() error { _, err := alice.CreateDataset(ctx, dataset.CreateInput{Name: "mine"}); return err }(), apperr.ErrConflict},
		{"forbidden", func() error {
			_, err := alice.CreateDataset(ctx, dataset.CreateInput{Name: "g", Scope: dataset.ScopeGlobal})
			return err
		}(), apperr.ErrForbidden},
		{"too large", func() error {
			_, err := alice.Upload(ctx, ws.ID, client.UploadInput{Name: "big.txt"}, strings.NewReader("12345"))
			return err
		}(), apperr.ErrTooLarge},
		{"unauthenticated", func() error { _, err := newClient(t, url, "exm_wrong").Me(ctx); return err }(), apperr.ErrUnauthenticated},
	}
	for _, tt := range tests {
		if !errors.Is(tt.err, tt.kind) {
			t.Errorf("%s: err = %v, want %v", tt.name, tt.err, tt.kind)
		}
		var apiErr *client.Error
		if !errors.As(tt.err, &apiErr) || apiErr.Message == "" {
			t.Errorf("%s: not an *client.Error with a message: %v", tt.name, tt.err)
		}
	}
	_ = admin
}

func TestErrorWithoutJSONBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "upstream down", http.StatusBadGateway)
	}))
	defer srv.Close()
	_, err := newClient(t, srv.URL, "exm_x").Me(ctx)
	var apiErr *client.Error
	if !errors.As(err, &apiErr) || apiErr.Status != http.StatusBadGateway || apiErr.Message != "502 Bad Gateway" || errors.Unwrap(apiErr) != nil {
		t.Errorf("err = %#v", err)
	}
}

func TestUnreachableServer(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	url := srv.URL
	srv.Close()
	if _, err := newClient(t, url, "exm_x").Me(ctx); err == nil {
		t.Error("call to a closed server succeeded")
	}
}

func TestMalformedSuccessBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("not json"))
	}))
	defer srv.Close()
	if _, err := newClient(t, srv.URL, "exm_x").Me(ctx); err == nil || !strings.Contains(err.Error(), "decoding") {
		t.Errorf("err = %v, want a decoding error", err)
	}
}

func TestPersonsAndGenerate(t *testing.T) {
	admin, alice, _, _ := server(t, nil)

	gen, err := alice.GeneratePersons(ctx, person.GenerateInput{Count: 4, Seed: 9})
	if err != nil || gen.Seed != 9 || len(gen.People) != 4 {
		t.Fatalf("generate persons = %+v, %v", gen, err)
	}
	list, err := admin.ListPersons(ctx, person.ListFilter{Company: gen.People[0].Company, Limit: 10, Offset: 0})
	if err != nil || len(list) == 0 {
		t.Errorf("list by company = %d, %v", len(list), err)
	}
	if page, _ := admin.ListPersons(ctx, person.ListFilter{Limit: 2, Offset: 2}); len(page) != 2 {
		t.Errorf("page = %d", len(page))
	}
	if got, err := alice.GetPerson(ctx, gen.People[0].ID); err != nil || got.Email != gen.People[0].Email {
		t.Errorf("get person = %+v, %v", got, err)
	}
	if _, err := alice.GetPerson(ctx, "missing"); !errors.Is(err, apperr.ErrNotFound) {
		t.Errorf("missing person err = %v", err)
	}

	schemas, err := alice.RecordSchemas(ctx)
	if err != nil || len(schemas) != len(synth.BuiltinSchemas().Names()) || schemas[0].Name != "address" || schemas[0].Hash == "" || len(schemas[0].Fields) == 0 {
		t.Errorf("record schemas = %+v, %v", schemas, err)
	}

	ticket := "name = \"ticket\"\ntitle = \"{reference}\"\n\n[[fields]]\nname = \"reference\"\nref = \"TK\"\n"
	if _, err := alice.CreateRecordSchema(ctx, ticket); !errors.Is(err, apperr.ErrForbidden) {
		t.Errorf("member add schema err = %v", err)
	}
	added, err := admin.CreateRecordSchema(ctx, ticket)
	if err != nil || added.Version != 1 || added.Source != synth.SourceAPI {
		t.Fatalf("add schema = %+v, %v", added, err)
	}
	if got, err := alice.RecordSchema(ctx, "ticket", false); err != nil || got.TOML != ticket {
		t.Errorf("schema = %+v, %v", got, err)
	}
	changed := strings.Replace(ticket, "TK", "SR", 1)
	if _, err := admin.UpdateRecordSchema(ctx, "ticket", changed, 9); !errors.Is(err, apperr.ErrConflict) {
		t.Errorf("stale update err = %v", err)
	}
	if got, err := admin.UpdateRecordSchema(ctx, "ticket", changed, 1); err != nil || got.Version != 2 {
		t.Errorf("update schema = %+v, %v", got, err)
	}
	if err := admin.DeleteRecordSchema(ctx, "ticket"); err != nil {
		t.Errorf("remove schema: %v", err)
	}
	if _, err := alice.RecordSchema(ctx, "ticket", false); !errors.Is(err, apperr.ErrNotFound) {
		t.Errorf("removed schema err = %v", err)
	}

	ds, err := alice.CreateDataset(ctx, dataset.CreateInput{Name: "gen"})
	if err != nil {
		t.Fatal(err)
	}
	res, err := alice.Generate(ctx, ds.ID, generate.Request{Kind: generate.KindRecords, Count: 3, Schemas: []synth.RecordSchema{synth.SchemaContact}, People: 3, Seed: 4})
	if err != nil || len(res.Fixtures) != 3 || res.Seed != 4 {
		t.Fatalf("generate = %+v, %v", res, err)
	}
	byPerson, err := alice.ListFixtures(ctx, ds.ID, fixture.ListFilter{PersonID: res.PersonIDs[0], RecordSchema: "contact"})
	if err != nil || len(byPerson) != 1 {
		t.Errorf("fixtures for person = %d, %v", len(byPerson), err)
	}
	up, err := alice.Upload(ctx, ds.ID, client.UploadInput{Name: "extra.json", RecordSchema: "company"}, strings.NewReader("{}"))
	if err != nil || up.Kind != fixture.KindRecord || up.RecordSchema != "company" {
		t.Errorf("record upload = %+v, %v", up, err)
	}
	if _, err := alice.Generate(ctx, ds.ID, generate.Request{Kind: "videos", Count: 1}); !errors.Is(err, apperr.ErrInvalid) {
		t.Errorf("bad kind err = %v", err)
	}
}

func TestIngest(t *testing.T) {
	admin, member, _, _ := server(t, func(c *app.Config) {
		c.AnonKey = anonymisetest.Key
		c.AnonDetector = anonymisetest.Detector{Names: []string{"Ottoline"}}
	})
	d, err := admin.CreateDataset(ctx, dataset.CreateInput{Name: "q", Scope: dataset.ScopeQuarantine})
	if err != nil {
		t.Fatal(err)
	}
	mail := anonymisetest.Email("Ottoline Shah", "Tom Reid", "acme.com.au", "Hi", "Hello Tom, Ottoline")
	file := func(name string, data []byte) client.IngestFile {
		return client.IngestFile{Name: name, Open: func() (io.ReadCloser, error) { return io.NopCloser(strings.NewReader(string(data))), nil }}
	}

	dry, err := admin.Ingest(ctx, d.ID, ingest.Request{DryRun: true, Profile: "default"}, []client.IngestFile{file("a.eml", mail)})
	if err != nil || !dry.DryRun || len(dry.Files[0].Report) == 0 {
		t.Fatalf("dry run = %+v, %v", dry, err)
	}
	res, err := admin.Ingest(ctx, d.ID, ingest.Request{Batch: "b"}, []client.IngestFile{file("a.eml", mail), file("b.eml", mail)})
	if err != nil || len(res.Files) != 2 || res.Files[1].FixtureName != "b-002.eml" {
		t.Fatalf("ingest = %+v, %v", res, err)
	}
	if fx, _ := admin.ListFixtures(ctx, d.ID, fixture.ListFilter{}); len(fx) != 2 {
		t.Errorf("stored %d fixtures", len(fx))
	}

	if _, err := member.Ingest(ctx, d.ID, ingest.Request{}, []client.IngestFile{file("a.eml", mail)}); !errors.Is(err, apperr.ErrForbidden) {
		t.Errorf("member err = %v", err)
	}
	broken := client.IngestFile{Name: "x.eml", Open: func() (io.ReadCloser, error) { return nil, errors.New("disk gone") }}
	if _, err := admin.Ingest(ctx, d.ID, ingest.Request{}, []client.IngestFile{broken}); err == nil {
		t.Error("unreadable file accepted")
	}
}

func TestExportAndImport(t *testing.T) {
	admin, member, _, _ := server(t, nil)
	d, err := member.CreateDataset(ctx, dataset.CreateInput{Name: "travel"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := member.Generate(ctx, d.ID, generate.Request{Kind: generate.KindEmails, Count: 2, Seed: 4}); err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	counts, err := member.Export(ctx, []string{d.ID}, &buf)
	if err != nil || counts["datasets"] != 1 || counts["fixtures"] != 2 || counts["blobs"] != 2 {
		t.Fatalf("export = %v, %v", counts, err)
	}

	other, _, _, _ := server(t, nil)
	res, err := other.Import(ctx, bytes.NewReader(buf.Bytes()))
	if err != nil || len(res.Datasets) != 1 || !res.Datasets[0].Created || res.Datasets[0].Added != 2 {
		t.Fatalf("import = %+v, %v", res, err)
	}
	if _, err := other.Import(ctx, strings.NewReader("nope")); !errors.Is(err, apperr.ErrInvalid) {
		t.Errorf("garbage err = %v", err)
	}
	if _, err := admin.Export(ctx, []string{"missing"}, io.Discard); !errors.Is(err, apperr.ErrNotFound) {
		t.Errorf("missing dataset err = %v", err)
	}
}

func TestExportFailingMidStreamIsAnError(t *testing.T) {
	var blobDir string
	_, member, _, _ := server(t, func(c *app.Config) { blobDir = c.BlobDir })
	d, err := member.CreateDataset(ctx, dataset.CreateInput{Name: "broken"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := member.Generate(ctx, d.ID, generate.Request{Kind: generate.KindFiles, Count: 2, Formats: []string{"txt"}, Seed: 4}); err != nil {
		t.Fatal(err)
	}
	objects, _ := filepath.Glob(filepath.Join(blobDir, "objects", "*", "*", "*"))
	if len(objects) == 0 {
		t.Fatal("no stored content")
	}
	for _, o := range objects {
		if err := os.Remove(o); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := member.Export(ctx, []string{d.ID}, io.Discard); err == nil {
		t.Error("an export that failed while streaming reported success")
	}
}

func TestAnonymise(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "keep.toml"), []byte("name = \"keep\"\nattachments = \"anonymise\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, member, _, url := server(t, func(c *app.Config) {
		c.AnonDetector = anonymisetest.Detector{Names: []string{"Ottoline"}}
		c.AnonProfilesDir = dir
	})
	mail := "From: Ottoline Shah <ottoline@acme.com.au>\r\nTo: t@o.com.au\r\nSubject: x\r\nMIME-Version: 1.0\r\nContent-Type: multipart/mixed; boundary=\"B\"\r\n\r\n" +
		"--B\r\nContent-Type: text/plain\r\n\r\nHi\r\n--B\r\nContent-Type: text/csv; name=\"l.csv\"\r\nContent-Disposition: attachment; filename=\"l.csv\"\r\n\r\nName\r\nOttoline\r\n--B--\r\n"
	if m, err := member.AnonymiseFile(ctx, scrub.Request{Profile: "keep"}, "a.eml", strings.NewReader(mail), io.Discard); err != nil || m.AttachmentsRewritten != 1 || m.Attachments != 0 {
		t.Errorf("rewritten attachments = %+v, %v", m, err)
	}
	file := func(name, data string) client.IngestFile {
		return client.IngestFile{Name: name, Open: func() (io.ReadCloser, error) { return io.NopCloser(strings.NewReader(data)), nil }}
	}

	var out strings.Builder
	f, err := member.AnonymiseFile(ctx, scrub.Request{Profile: "default"}, "notes.md", strings.NewReader("Ottoline on 0412 345 678\n"), &out)
	if err != nil {
		t.Fatal(err)
	}
	if f.Format != anonymise.FormatMarkdown || !strings.HasPrefix(f.Name, "document-") || !strings.HasSuffix(f.Name, ".md") ||
		f.Replacements != 2 || strings.Contains(out.String(), "Ottoline") || strings.Contains(out.String(), "345 678") {
		t.Errorf("file = %+v, %q", f, out.String())
	}
	var doc bytes.Buffer
	d, err := member.AnonymiseFile(ctx, scrub.Request{}, "q.docx", bytes.NewReader(anonymisetest.DOCX("Ottoline")), &doc)
	if err != nil || d.Format != anonymise.FormatDOCX || strings.Join(d.DroppedParts, ",") != "word/vbaProject.bin" || !bytes.HasPrefix(doc.Bytes(), []byte("PK")) {
		t.Errorf("docx = %+v, %v", d, err)
	}
	// An error is the server's message, as its kind.
	if _, err := member.AnonymiseFile(ctx, scrub.Request{}, "a.xls", strings.NewReader("xls"), io.Discard); !errors.Is(err, apperr.ErrInvalid) || !strings.Contains(err.Error(), "cannot anonymise .xls") {
		t.Errorf("xls err = %v", err)
	}
	if _, err := member.AnonymiseFile(ctx, scrub.Request{LinkRegistry: true}, "a.md", strings.NewReader("x"), io.Discard); !errors.Is(err, apperr.ErrForbidden) {
		t.Errorf("link err = %v", err)
	}

	res, err := member.AnonymiseFiles(ctx, scrub.Request{}, []client.IngestFile{file("a.md", "Ottoline\n"), file("b.json", `{"name": "Wendy Moss"}`), file("c.xls", "xls")})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Files) != 3 || res.Files[2].Error == "" || strings.Contains(string(res.Files[0].Data), "Ottoline") || strings.Contains(string(res.Files[1].Data), "Wendy") {
		t.Errorf("files = %+v", res)
	}
	if _, err := member.AnonymiseFiles(ctx, scrub.Request{}, []client.IngestFile{{Name: "x.md", Open: func() (io.ReadCloser, error) { return nil, errors.New("gone") }}}); err == nil {
		t.Error("an unreadable file was sent")
	}

	// 429 comes back as ErrBusy.
	busy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"error": "busy: this key already has an anonymise run in progress"}`))
	}))
	defer busy.Close()
	if _, err := newClient(t, busy.URL, "exm_x").AnonymiseFile(ctx, scrub.Request{}, "a.md", strings.NewReader("x"), io.Discard); !errors.Is(err, apperr.ErrBusy) {
		t.Errorf("429 err = %v", err)
	}
	// A server sending a zip that is not one fails clearly.
	junk := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte("not a zip")) }))
	defer junk.Close()
	if _, err := newClient(t, junk.URL, "exm_x").AnonymiseFiles(ctx, scrub.Request{}, []client.IngestFile{file("a.md", "x")}); err == nil || !strings.Contains(err.Error(), "reading the anonymised files") {
		t.Errorf("junk err = %v", err)
	}
	_ = url
}
