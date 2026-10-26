package dataset_test

import (
	"context"
	"errors"
	"testing"

	"github.com/martinsuchenak/exemplar/internal/apperr"
	"github.com/martinsuchenak/exemplar/internal/dataset"
	"github.com/martinsuchenak/exemplar/internal/principal"
	"github.com/martinsuchenak/exemplar/internal/testutil"
)

var ctx = context.Background()

func TestScopesAndVisibility(t *testing.T) {
	a := testutil.NewApp(t)
	admin := testutil.Admin(t, a)
	alice := testutil.Member(t, a, admin, "alice")
	bob := testutil.Member(t, a, admin, "bob")

	if _, err := a.Datasets.Create(ctx, alice, dataset.CreateInput{Name: "shared", Scope: dataset.ScopeGlobal}); !errors.Is(err, apperr.ErrForbidden) {
		t.Errorf("member creating global err = %v, want ErrForbidden", err)
	}
	global, err := a.Datasets.Create(ctx, admin, dataset.CreateInput{Name: "shared", Scope: dataset.ScopeGlobal, Tags: []string{"Email", " email ", ""}})
	if err != nil {
		t.Fatalf("create global: %v", err)
	}
	if len(global.Tags) != 1 || global.Tags[0] != "email" {
		t.Errorf("tags = %v, want [email]", global.Tags)
	}
	ws, err := a.Datasets.Create(ctx, alice, dataset.CreateInput{Name: "mine"})
	if err != nil {
		t.Fatalf("create workspace: %v", err)
	}
	if ws.Scope != dataset.ScopeWorkspace || ws.OwnerAccountID != alice.AccountID {
		t.Errorf("workspace = %+v", ws)
	}
	quarantine, err := a.Datasets.Create(ctx, admin, dataset.CreateInput{Name: "inbox", Scope: dataset.ScopeQuarantine})
	if err != nil {
		t.Fatalf("create quarantine: %v", err)
	}

	// Bob sees the global dataset only; Alice's workspace and quarantine are hidden.
	list, err := a.Datasets.List(ctx, bob, dataset.ListFilter{})
	if err != nil || len(list) != 1 || list[0].ID != global.ID {
		t.Errorf("bob's list = %+v, %v; want only the global dataset", list, err)
	}
	for _, id := range []string{ws.ID, quarantine.ID} {
		if _, err := a.Datasets.Get(ctx, bob, id); !errors.Is(err, apperr.ErrNotFound) {
			t.Errorf("bob get %s err = %v, want ErrNotFound", id, err)
		}
	}
	if list, _ := a.Datasets.List(ctx, admin, dataset.ListFilter{}); len(list) != 3 {
		t.Errorf("admin sees %d datasets, want 3", len(list))
	}

	// Same workspace name is fine for another owner, not for the same one.
	if _, err := a.Datasets.Create(ctx, bob, dataset.CreateInput{Name: "mine"}); err != nil {
		t.Errorf("bob's own 'mine': %v", err)
	}
	if _, err := a.Datasets.Create(ctx, alice, dataset.CreateInput{Name: "mine"}); !errors.Is(err, apperr.ErrConflict) {
		t.Errorf("duplicate name err = %v, want ErrConflict", err)
	}
}

func TestFreezeAndDelete(t *testing.T) {
	a := testutil.NewApp(t)
	admin := testutil.Admin(t, a)
	alice := testutil.Member(t, a, admin, "alice")
	bob := testutil.Member(t, a, admin, "bob")

	ws, err := a.Datasets.Create(ctx, alice, dataset.CreateInput{Name: "mine"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	frozen, err := a.Datasets.Freeze(ctx, alice, ws.ID)
	if err != nil || !frozen.Frozen() {
		t.Fatalf("freeze = %+v, %v", frozen, err)
	}
	if _, err := a.Datasets.Authorize(ctx, nil, alice, ws.ID, dataset.AccessWrite); !errors.Is(err, apperr.ErrConflict) {
		t.Errorf("write to frozen err = %v, want ErrConflict", err)
	}
	if err := a.Datasets.Delete(ctx, bob, ws.ID); !errors.Is(err, apperr.ErrNotFound) {
		t.Errorf("bob deleting alice's workspace err = %v, want ErrNotFound", err)
	}
	if err := a.Datasets.Delete(ctx, alice, ws.ID); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := a.Datasets.Get(ctx, alice, ws.ID); !errors.Is(err, apperr.ErrNotFound) {
		t.Errorf("get after delete err = %v, want ErrNotFound", err)
	}
}

func TestInvalidInput(t *testing.T) {
	a := testutil.NewApp(t)
	admin := testutil.Admin(t, a)
	for _, in := range []dataset.CreateInput{
		{Name: ""},
		{Name: "../x"},
		{Name: "ok", Scope: "public"},
	} {
		if _, err := a.Datasets.Create(ctx, admin, in); !errors.Is(err, apperr.ErrInvalid) {
			t.Errorf("Create(%+v) err = %v, want ErrInvalid", in, err)
		}
	}
}

func TestListFilters(t *testing.T) {
	a := testutil.NewApp(t)
	admin := testutil.Admin(t, a)
	for _, in := range []dataset.CreateInput{
		{Name: "emails", Scope: dataset.ScopeGlobal},
		{Name: "files", Scope: dataset.ScopeGlobal},
		{Name: "emails"},
	} {
		if _, err := a.Datasets.Create(ctx, admin, in); err != nil {
			t.Fatalf("create %+v: %v", in, err)
		}
	}
	if got, _ := a.Datasets.List(ctx, admin, dataset.ListFilter{Scope: dataset.ScopeGlobal}); len(got) != 2 {
		t.Errorf("global = %d, want 2", len(got))
	}
	if got, _ := a.Datasets.List(ctx, admin, dataset.ListFilter{Name: "emails"}); len(got) != 2 {
		t.Errorf("named emails = %d, want 2", len(got))
	}
	if _, err := a.Datasets.List(ctx, admin, dataset.ListFilter{Scope: "public"}); !errors.Is(err, apperr.ErrInvalid) {
		t.Errorf("unknown scope err = %v, want ErrInvalid", err)
	}
}

func TestReadOnlyKeys(t *testing.T) {
	a := testutil.NewApp(t)
	admin := testutil.Admin(t, a)
	alice := testutil.Member(t, a, admin, "alice")
	ws, err := a.Datasets.Create(ctx, alice, dataset.CreateInput{Name: "mine"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	ro := alice
	ro.Scopes = []principal.Scope{principal.ScopeRead}
	if _, err := a.Datasets.Create(ctx, ro, dataset.CreateInput{Name: "x"}); !errors.Is(err, apperr.ErrForbidden) {
		t.Errorf("read-only create err = %v, want ErrForbidden", err)
	}
	if _, err := a.Datasets.Get(ctx, ro, ws.ID); err != nil {
		t.Errorf("read-only get: %v", err)
	}
	if _, err := a.Datasets.Freeze(ctx, ro, ws.ID); !errors.Is(err, apperr.ErrForbidden) {
		t.Errorf("read-only freeze err = %v, want ErrForbidden", err)
	}

	none := alice
	none.Scopes = nil
	if _, err := a.Datasets.List(ctx, none, dataset.ListFilter{}); !errors.Is(err, apperr.ErrForbidden) {
		t.Errorf("no-scope list err = %v, want ErrForbidden", err)
	}
}

func TestFreezeRules(t *testing.T) {
	a := testutil.NewApp(t)
	admin := testutil.Admin(t, a)
	alice := testutil.Member(t, a, admin, "alice")
	global, err := a.Datasets.Create(ctx, admin, dataset.CreateInput{Name: "shared", Scope: dataset.ScopeGlobal})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := a.Datasets.Freeze(ctx, alice, global.ID); !errors.Is(err, apperr.ErrForbidden) {
		t.Errorf("member freezing global err = %v, want ErrForbidden", err)
	}
	first, err := a.Datasets.Freeze(ctx, admin, global.ID)
	if err != nil {
		t.Fatalf("freeze: %v", err)
	}
	second, err := a.Datasets.Freeze(ctx, admin, global.ID)
	if err != nil || !second.FrozenAt.Equal(*first.FrozenAt) {
		t.Errorf("second freeze = %+v, %v; want unchanged", second, err)
	}
	// Frozen datasets can still be deleted by someone allowed to change them.
	if err := a.Datasets.Delete(ctx, admin, global.ID); err != nil {
		t.Errorf("delete frozen: %v", err)
	}
	if err := a.Datasets.Delete(ctx, admin, global.ID); !errors.Is(err, apperr.ErrNotFound) {
		t.Errorf("delete twice err = %v, want ErrNotFound", err)
	}
}
