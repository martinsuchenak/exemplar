package client_test

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/martinsuchenak/exemplar/cmd/routes"
	"github.com/martinsuchenak/exemplar/internal/account"
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
	promoted, err := admin.Promote(ctx, fork.Dataset.ID, dataset.PromoteInput{Name: "emails"})
	if err != nil || promoted.Dataset.Version != 2 || promoted.Dataset.FrozenAt == nil {
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
		c.AnonDetector = anonymisetest.Detector{Names: []string{"Priya"}}
	})
	d, err := admin.CreateDataset(ctx, dataset.CreateInput{Name: "q", Scope: dataset.ScopeQuarantine})
	if err != nil {
		t.Fatal(err)
	}
	mail := anonymisetest.Email("Priya Shah", "Tom Reid", "acme.com.au", "Hi", "Hello Tom, Priya")
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
