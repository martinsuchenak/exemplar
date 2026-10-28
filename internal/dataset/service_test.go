package dataset_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/martinsuchenak/exemplar/internal/apperr"
	"github.com/martinsuchenak/exemplar/internal/dataset"
	"github.com/martinsuchenak/exemplar/internal/fixture"
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

	// Through an entry point that does not serve quarantine (MCP), even an
	// admin sees everything but quarantine, which reads as not found.
	viaMCP := admin
	viaMCP.NoQuarantine = true
	if list, _ := a.Datasets.List(ctx, viaMCP, dataset.ListFilter{}); len(list) != 2 || list[0].ID == quarantine.ID || list[1].ID == quarantine.ID {
		t.Errorf("admin without quarantine sees %+v, want the global dataset and the workspace", list)
	}
	if list, _ := a.Datasets.List(ctx, viaMCP, dataset.ListFilter{Scope: dataset.ScopeQuarantine}); len(list) != 0 {
		t.Errorf("quarantine filter without quarantine = %+v", list)
	}
	for _, access := range []dataset.Access{dataset.AccessRead, dataset.AccessWrite} {
		if _, err := a.Datasets.Authorize(ctx, nil, viaMCP, quarantine.ID, access); !errors.Is(err, apperr.ErrNotFound) {
			t.Errorf("authorize quarantine without quarantine err = %v, want ErrNotFound", err)
		}
	}
	for _, id := range []string{global.ID, ws.ID} {
		if _, err := a.Datasets.Get(ctx, viaMCP, id); err != nil {
			t.Errorf("admin without quarantine get %s: %v", id, err)
		}
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

func TestForkAndPromote(t *testing.T) {
	a := testutil.NewApp(t)
	admin := testutil.Admin(t, a)
	alice := testutil.Member(t, a, admin, "alice")

	global, err := a.Datasets.Create(ctx, admin, dataset.CreateInput{Name: "emails", Scope: dataset.ScopeGlobal, Description: "base", Tags: []string{"mail"}})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	for _, name := range []string{"a.eml", "b.eml"} {
		if _, err := a.Fixtures.Create(ctx, admin, fixture.CreateInput{DatasetID: global.ID, Name: name, Content: strings.NewReader(name)}); err != nil {
			t.Fatalf("add %s: %v", name, err)
		}
	}

	fork, err := a.Datasets.Fork(ctx, alice, global.ID, dataset.ForkInput{})
	if err != nil {
		t.Fatalf("fork: %v", err)
	}
	d := fork.Dataset
	if d.Scope != dataset.ScopeWorkspace || d.OwnerAccountID != alice.AccountID || d.ParentID != global.ID || d.Name != "emails" || d.Description != "base" || d.Frozen() {
		t.Errorf("fork = %+v", d)
	}
	if fork.Copied != 2 || fork.Skipped != 0 {
		t.Errorf("fork copied %d, skipped %d; want 2, 0", fork.Copied, fork.Skipped)
	}
	copies, err := a.Fixtures.List(ctx, alice, d.ID, fixture.ListFilter{})
	if err != nil || len(copies) != 2 || copies[0].CreatedBy != alice.AccountID {
		t.Errorf("fork contents = %+v, %v", copies, err)
	}
	// The fork is independent: alice can add to it while the global one is untouched.
	if _, err := a.Fixtures.Create(ctx, alice, fixture.CreateInput{DatasetID: d.ID, Name: "c.eml", Content: strings.NewReader("c")}); err != nil {
		t.Fatalf("add to fork: %v", err)
	}
	if _, err := a.Datasets.Fork(ctx, alice, global.ID, dataset.ForkInput{}); !errors.Is(err, apperr.ErrConflict) {
		t.Errorf("second fork with the same name err = %v, want ErrConflict", err)
	}

	if _, err := a.Datasets.Promote(ctx, alice, d.ID, dataset.PromoteInput{}); !errors.Is(err, apperr.ErrForbidden) {
		t.Errorf("member promote err = %v, want ErrForbidden", err)
	}
	v2, err := a.Datasets.Promote(ctx, admin, d.ID, dataset.PromoteInput{})
	if err != nil {
		t.Fatalf("promote: %v", err)
	}
	if v2.Dataset.Scope != dataset.ScopeGlobal || v2.Dataset.Version != 2 || !v2.Dataset.Frozen() || v2.Copied != 3 || v2.Dataset.Description != "base" {
		t.Errorf("promoted = %+v copied %d; want global v2, frozen, 3 copied, the source's description", v2.Dataset, v2.Copied)
	}
	v3, err := a.Datasets.Promote(ctx, admin, d.ID, dataset.PromoteInput{})
	if err != nil || v3.Dataset.Version != 3 {
		t.Errorf("second promote = %+v, %v; want version 3", v3, err)
	}
	renamed, err := a.Datasets.Promote(ctx, admin, d.ID, dataset.PromoteInput{Name: "emails-v2", Description: "reviewed emails"})
	if err != nil || renamed.Dataset.Version != 1 || renamed.Dataset.Name != "emails-v2" || renamed.Dataset.Description != "reviewed emails" {
		t.Errorf("renamed promote = %+v, %v; want emails-v2 v1, described", renamed, err)
	}
	if src, _ := a.Datasets.Get(ctx, alice, d.ID); src.Description != "base" {
		t.Errorf("promoting changed the source's description to %q", src.Description)
	}
}

func TestForkRules(t *testing.T) {
	a := testutil.NewApp(t)
	admin := testutil.Admin(t, a)
	alice := testutil.Member(t, a, admin, "alice")
	bob := testutil.Member(t, a, admin, "bob")
	ws, err := a.Datasets.Create(ctx, alice, dataset.CreateInput{Name: "mine"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	if _, err := a.Datasets.Fork(ctx, bob, ws.ID, dataset.ForkInput{}); !errors.Is(err, apperr.ErrNotFound) {
		t.Errorf("bob forking alice's workspace err = %v, want ErrNotFound", err)
	}
	ro := alice
	ro.Scopes = []principal.Scope{principal.ScopeRead}
	if _, err := a.Datasets.Fork(ctx, ro, ws.ID, dataset.ForkInput{Name: "x"}); !errors.Is(err, apperr.ErrForbidden) {
		t.Errorf("read-only fork err = %v, want ErrForbidden", err)
	}
	if _, err := a.Datasets.Fork(ctx, alice, ws.ID, dataset.ForkInput{Name: "../bad"}); !errors.Is(err, apperr.ErrInvalid) {
		t.Errorf("bad fork name err = %v, want ErrInvalid", err)
	}
	named, err := a.Datasets.Fork(ctx, alice, ws.ID, dataset.ForkInput{Name: "copy", Description: "mine too"})
	if err != nil || named.Dataset.Name != "copy" || named.Dataset.Description != "mine too" || named.Copied != 0 {
		t.Errorf("named fork = %+v, %v", named, err)
	}
}

func TestQuarantineForkSkipsUnapproved(t *testing.T) {
	a := testutil.NewApp(t)
	admin := testutil.Admin(t, a)
	q, err := a.Datasets.Create(ctx, admin, dataset.CreateInput{Name: "inbox", Scope: dataset.ScopeQuarantine})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	var approvedID string
	for _, name := range []string{"ok.eml", "pending.eml", "bad.eml"} {
		f, err := a.Fixtures.Create(ctx, admin, fixture.CreateInput{DatasetID: q.ID, Name: name, Source: fixture.SourceAnonymised, IngestBatch: "b1", Content: strings.NewReader(name)})
		if err != nil {
			t.Fatalf("add %s: %v", name, err)
		}
		switch name {
		case "ok.eml":
			approvedID = f.ID
			if _, err := a.Fixtures.Review(ctx, admin, f.ID, true); err != nil {
				t.Fatalf("approve: %v", err)
			}
		case "bad.eml":
			if _, err := a.Fixtures.Review(ctx, admin, f.ID, false); err != nil {
				t.Fatalf("reject: %v", err)
			}
		}
	}

	res, err := a.Datasets.Promote(ctx, admin, q.ID, dataset.PromoteInput{Name: "real-emails"})
	if err != nil {
		t.Fatalf("promote: %v", err)
	}
	if res.Copied != 1 || res.Skipped != 2 {
		t.Errorf("copied %d, skipped %d; want 1, 2", res.Copied, res.Skipped)
	}
	alice := testutil.Member(t, a, admin, "alice")
	got, err := a.Fixtures.List(ctx, alice, res.Dataset.ID, fixture.ListFilter{})
	if err != nil || len(got) != 1 || got[0].Name != "ok.eml" {
		t.Fatalf("promoted contents = %+v, %v", got, err)
	}
	if got[0].ReviewState != fixture.ReviewApproved || got[0].ReviewedBy != admin.AccountID || got[0].IngestBatch != "b1" || got[0].ID == approvedID {
		t.Errorf("copy lost its provenance or kept the source ID: %+v", got[0])
	}
	// Members can read approved real-derived data once it is in the library.
	if _, _, err := a.Fixtures.Open(ctx, alice, got[0].ID); err != nil {
		t.Errorf("member reading approved copy: %v", err)
	}
}

func TestListQuery(t *testing.T) {
	a := testutil.NewApp(t)
	admin := testutil.Admin(t, a)
	alice := testutil.Member(t, a, admin, "alice")
	bob := testutil.Member(t, a, admin, "bob")
	if _, err := a.Datasets.Create(ctx, admin, dataset.CreateInput{Name: "crm-contacts", Scope: dataset.ScopeGlobal, Description: "Contacts for the CRM", Tags: []string{"Sales"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Datasets.Create(ctx, admin, dataset.CreateInput{Name: "coverage", Scope: dataset.ScopeGlobal, Description: "Covers 100% of the edge cases"}); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Datasets.Create(ctx, alice, dataset.CreateInput{Name: "Inbox", Description: "Email replies"}); err != nil {
		t.Fatal(err)
	}

	names := func(p principal.Principal, query string) string {
		t.Helper()
		list, err := a.Datasets.List(ctx, p, dataset.ListFilter{Query: query})
		if err != nil {
			t.Fatalf("query %q: %v", query, err)
		}
		var out []string
		for _, d := range list {
			out = append(out, d.Name)
		}
		return strings.Join(out, ",")
	}
	for query, want := range map[string]string{
		"":             "coverage,crm-contacts,Inbox",
		"crm contacts": "crm-contacts",          // words, in any order
		"contacts_crm": "crm-contacts",          // separators split words
		"CRM":          "crm-contacts",          // ASCII letters ignore case
		"SALES":        "crm-contacts",          // a tag
		"replies":      "Inbox",                 // the description
		"inbox email":  "Inbox",                 // name and description together
		"%":            "coverage",              // a literal %, not a wildcard
		"co":           "coverage,crm-contacts", // part of a word
		"crm replies":  "",                      // every word must match
	} {
		if got := names(alice, query); got != want {
			t.Errorf("query %q = %q, want %q", query, got, want)
		}
	}
	// The query narrows what the key may read; it never widens it.
	if got := names(bob, "inbox"); got != "" {
		t.Errorf("bob's query for alice's workspace = %q", got)
	}
	for _, query := range []string{strings.Repeat("x", 201), "a b c d e f g h i"} {
		if _, err := a.Datasets.List(ctx, alice, dataset.ListFilter{Query: query}); !errors.Is(err, apperr.ErrInvalid) {
			t.Errorf("query of %d chars err = %v, want ErrInvalid", len(query), err)
		}
	}
}

func TestResolve(t *testing.T) {
	a := testutil.NewApp(t)
	admin := testutil.Admin(t, a)
	alice := testutil.Member(t, a, admin, "alice")
	bob := testutil.Member(t, a, admin, "bob")

	src, err := a.Datasets.Create(ctx, admin, dataset.CreateInput{Name: "draft"})
	if err != nil {
		t.Fatal(err)
	}
	var newest *dataset.Dataset
	for range 2 {
		res, err := a.Datasets.Promote(ctx, admin, src.ID, dataset.PromoteInput{Name: "library"})
		if err != nil {
			t.Fatal(err)
		}
		newest = res.Dataset
	}
	mine, err := a.Datasets.Create(ctx, alice, dataset.CreateInput{Name: "mine"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.Datasets.Create(ctx, bob, dataset.CreateInput{Name: "mine"}); err != nil {
		t.Fatal(err)
	}
	// A fork keeps its source's name, so alice now has "library" twice.
	if _, err := a.Datasets.Fork(ctx, alice, newest.ID, dataset.ForkInput{}); err != nil {
		t.Fatal(err)
	}
	quarantine, err := a.Datasets.Create(ctx, admin, dataset.CreateInput{Name: "inbox", Scope: dataset.ScopeQuarantine})
	if err != nil {
		t.Fatal(err)
	}

	for _, tt := range []struct {
		who  principal.Principal
		ref  string
		want string
	}{
		{alice, mine.ID, mine.ID},   // an id
		{alice, " mine ", mine.ID},  // her workspace, by name
		{bob, "library", newest.ID}, // the newest global version
		{bob, newest.ID, newest.ID}, // a global dataset, by id
		{admin, "inbox", quarantine.ID},
	} {
		d, err := a.Datasets.Resolve(ctx, tt.who, tt.ref)
		if err != nil || d.ID != tt.want {
			t.Errorf("Resolve(%q) = %+v, %v; want %s", tt.ref, d, err, tt.want)
		}
	}

	// A name that means several datasets is refused, listing them: alice's
	// "library" fork and the global one; alice's and bob's "mine" for admin.
	for _, tt := range []struct {
		who  principal.Principal
		name string
	}{{alice, "library"}, {admin, "mine"}} {
		_, err := a.Datasets.Resolve(ctx, tt.who, tt.name)
		if !errors.Is(err, apperr.ErrInvalid) || !strings.Contains(err.Error(), "2 datasets") {
			t.Errorf("resolving %q err = %v, want ErrInvalid naming 2 datasets", tt.name, err)
		}
	}

	viaMCP := admin
	viaMCP.NoQuarantine = true
	none := alice
	none.Scopes = nil
	for _, tt := range []struct {
		who  principal.Principal
		ref  string
		want error
	}{
		{alice, "", apperr.ErrInvalid},
		{alice, "nothing", apperr.ErrNotFound},
		{bob, mine.ID, apperr.ErrNotFound},    // another's workspace, by id
		{bob, "draft", apperr.ErrNotFound},    // another's workspace, by name
		{viaMCP, "inbox", apperr.ErrNotFound}, // quarantine, by name
		{viaMCP, quarantine.ID, apperr.ErrNotFound},
		{none, "library", apperr.ErrForbidden},
	} {
		if _, err := a.Datasets.Resolve(ctx, tt.who, tt.ref); !errors.Is(err, tt.want) {
			t.Errorf("Resolve(%q) err = %v, want %v", tt.ref, err, tt.want)
		}
	}
}
