package fixture_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/martinsuchenak/exemplar/internal/app"
	"github.com/martinsuchenak/exemplar/internal/apperr"
	"github.com/martinsuchenak/exemplar/internal/dataset"
	"github.com/martinsuchenak/exemplar/internal/fixture"
	"github.com/martinsuchenak/exemplar/internal/testutil"
)

func TestCheckImport(t *testing.T) {
	a := testutil.NewApp(t)
	admin := testutil.Admin(t, a)
	alice := testutil.Member(t, a, admin, "alice")
	ws := &dataset.Dataset{ID: "w", Scope: dataset.ScopeWorkspace}
	q := &dataset.Dataset{ID: "q", Scope: dataset.ScopeQuarantine}
	good := fixture.Fixture{ID: "f", Name: "a.eml", Kind: fixture.KindEmail, Format: "eml", Source: fixture.SourceSynthetic, BlobHash: strings.Repeat("a", 64)}
	if err := fixture.CheckImport(alice, ws, &good); err != nil {
		t.Errorf("good fixture: %v", err)
	}
	mod := func(f func(*fixture.Fixture)) *fixture.Fixture {
		g := good
		f(&g)
		return &g
	}
	approved := mod(func(f *fixture.Fixture) { f.Source, f.ReviewState = fixture.SourceAnonymised, fixture.ReviewApproved })
	for name, tt := range map[string]struct {
		f    *fixture.Fixture
		d    *dataset.Dataset
		want error
	}{
		"no id":           {mod(func(f *fixture.Fixture) { f.ID = "" }), ws, apperr.ErrInvalid},
		"path in id":      {mod(func(f *fixture.Fixture) { f.ID = "../../etc/passwd" }), ws, apperr.ErrInvalid},
		"path in link":    {mod(func(f *fixture.Fixture) { f.People = []fixture.PersonLink{{PersonID: "../p", Role: "from"}} }), ws, apperr.ErrInvalid},
		"slash in name":   {mod(func(f *fixture.Fixture) { f.Name = "a/b" }), ws, apperr.ErrInvalid},
		"dot-dot name":    {mod(func(f *fixture.Fixture) { f.Name = " .. " }), ws, apperr.ErrInvalid},
		"dot name":        {mod(func(f *fixture.Fixture) { f.Name = "." }), ws, apperr.ErrInvalid},
		"bad kind":        {mod(func(f *fixture.Fixture) { f.Kind = "x" }), ws, apperr.ErrInvalid},
		"bad hash":        {mod(func(f *fixture.Fixture) { f.BlobHash = "../x" }), ws, apperr.ErrInvalid},
		"schema on email": {mod(func(f *fixture.Fixture) { f.RecordSchema = "contact" }), ws, apperr.ErrInvalid},
		"bad link":        {mod(func(f *fixture.Fixture) { f.People = []fixture.PersonLink{{PersonID: "p", Role: "Bad Role"}} }), ws, apperr.ErrInvalid},
		"synthetic in q":  {&good, q, apperr.ErrInvalid},
		"unreviewed real": {mod(func(f *fixture.Fixture) { f.Source = fixture.SourceAnonymised }), q, apperr.ErrInvalid},
		"member real":     {approved, q, apperr.ErrForbidden},
	} {
		if err := fixture.CheckImport(alice, tt.d, tt.f); !errors.Is(err, tt.want) {
			t.Errorf("%s: err = %v, want %v", name, err, tt.want)
		}
	}
	if err := fixture.CheckImport(admin, ws, approved); err != nil {
		t.Errorf("admin, approved real-derived into a workspace: %v", err)
	}
}

func TestStoreContent(t *testing.T) {
	a := testutil.NewAppWith(t, func(c *app.Config) { c.MaxFixtureBytes = 8 })
	admin := testutil.Admin(t, a)
	release := a.Fixtures.HoldContent()
	defer release()
	hash, size, err := a.Fixtures.StoreContent(ctx, admin, strings.NewReader("small"))
	if err != nil || size != 5 || len(hash) != 64 {
		t.Errorf("store = %s %d %v", hash, size, err)
	}
	if _, _, err := a.Fixtures.StoreContent(ctx, admin, strings.NewReader("far too large")); !errors.Is(err, apperr.ErrTooLarge) {
		t.Errorf("too large err = %v", err)
	}
	reader := admin
	reader.Scopes = reader.Scopes[:0]
	if _, _, err := a.Fixtures.StoreContent(ctx, reader, strings.NewReader("x")); !errors.Is(err, apperr.ErrForbidden) {
		t.Errorf("read-only err = %v", err)
	}
}

func TestEnsureContent(t *testing.T) {
	a := testutil.NewApp(t)
	admin := testutil.Admin(t, a)
	hash, size, err := a.Fixtures.StoreContent(ctx, admin, strings.NewReader("kept"))
	if err != nil {
		t.Fatal(err)
	}
	release := a.Fixtures.HoldContent()
	defer release()
	if err := a.Fixtures.EnsureContent(ctx, hash, size); err != nil {
		t.Errorf("stored content: %v", err)
	}
	if err := a.Fixtures.EnsureContent(ctx, strings.Repeat("0", 64), 1); !errors.Is(err, apperr.ErrConflict) {
		t.Errorf("missing content err = %v", err)
	}
}
