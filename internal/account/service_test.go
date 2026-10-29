package account_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/martinsuchenak/exemplar/internal/account"
	"github.com/martinsuchenak/exemplar/internal/apperr"
	"github.com/martinsuchenak/exemplar/internal/audit"
	"github.com/martinsuchenak/exemplar/internal/principal"
	"github.com/martinsuchenak/exemplar/internal/testutil"
)

var ctx = context.Background()

func TestBootstrapOnlyOnce(t *testing.T) {
	a := testutil.NewApp(t)
	acc, issued, err := a.Accounts.Bootstrap(ctx, account.CreateAccountInput{})
	if err != nil {
		t.Fatalf("bootstrap: %v", err)
	}
	if acc.Name != "admin" || acc.Role != principal.RoleAdmin {
		t.Errorf("account = %+v, want admin/admin", acc)
	}
	if !strings.HasPrefix(issued.Secret, account.KeyPrefix) || !strings.HasPrefix(issued.Secret, issued.Key.Prefix) {
		t.Errorf("secret %q does not match prefix %q", issued.Secret, issued.Key.Prefix)
	}

	p := testutil.Authenticate(t, a, issued.Secret)
	if !p.IsAdmin() {
		t.Errorf("bootstrap key is not admin: %+v", p)
	}

	if _, _, err := a.Accounts.Bootstrap(ctx, account.CreateAccountInput{Name: "second"}); !errors.Is(err, apperr.ErrConflict) {
		t.Errorf("second bootstrap err = %v, want ErrConflict", err)
	}
}

func TestAuthenticateRejectsBadKeys(t *testing.T) {
	a := testutil.NewApp(t)
	admin := testutil.Admin(t, a)

	for _, secret := range []string{"", "nope", account.KeyPrefix, account.KeyPrefix + "aaaaaaaaaaaaaaaaaaaaaaaaaaaa"} {
		if _, err := a.Accounts.Authenticate(ctx, secret); !errors.Is(err, apperr.ErrUnauthenticated) {
			t.Errorf("Authenticate(%q) err = %v, want ErrUnauthenticated", secret, err)
		}
	}

	issued, err := a.Accounts.CreateKey(ctx, admin, account.CreateKeyInput{Name: "temp"})
	if err != nil {
		t.Fatalf("create key: %v", err)
	}
	if err := a.Accounts.RevokeKey(ctx, admin, issued.Key.ID); err != nil {
		t.Fatalf("revoke: %v", err)
	}
	if _, err := a.Accounts.Authenticate(ctx, issued.Secret); !errors.Is(err, apperr.ErrUnauthenticated) {
		t.Errorf("revoked key err = %v, want ErrUnauthenticated", err)
	}
}

func TestMemberCannotEscalate(t *testing.T) {
	a := testutil.NewApp(t)
	admin := testutil.Admin(t, a)
	member := testutil.Member(t, a, admin, "dev1")
	other := testutil.Member(t, a, admin, "dev2")

	if _, err := a.Accounts.CreateAccount(ctx, member, account.CreateAccountInput{Name: "x", Kind: account.KindApp}); !errors.Is(err, apperr.ErrForbidden) {
		t.Errorf("member create account err = %v, want ErrForbidden", err)
	}
	if _, err := a.Accounts.CreateKey(ctx, member, account.CreateKeyInput{Name: "k", Scopes: []principal.Scope{principal.ScopeAdmin}}); !errors.Is(err, apperr.ErrForbidden) {
		t.Errorf("member admin-scope key err = %v, want ErrForbidden", err)
	}
	if _, err := a.Accounts.CreateKey(ctx, member, account.CreateKeyInput{AccountID: other.AccountID, Name: "k"}); !errors.Is(err, apperr.ErrForbidden) {
		t.Errorf("member key for other account err = %v, want ErrForbidden", err)
	}
	if _, err := a.Accounts.ListKeys(ctx, member, other.AccountID); !errors.Is(err, apperr.ErrNotFound) {
		t.Errorf("member list other keys err = %v, want ErrNotFound", err)
	}
	accounts, err := a.Accounts.ListAccounts(ctx, member)
	if err != nil || len(accounts) != 1 || accounts[0].ID != member.AccountID {
		t.Errorf("member list accounts = %+v, %v; want only itself", accounts, err)
	}

	// A read-only key cannot mint a write key for itself.
	ro, err := a.Accounts.CreateKey(ctx, member, account.CreateKeyInput{Name: "ro", Scopes: []principal.Scope{principal.ScopeRead}})
	if err != nil {
		t.Fatalf("create read-only key: %v", err)
	}
	roP := testutil.Authenticate(t, a, ro.Secret)
	if _, err := a.Accounts.CreateKey(ctx, roP, account.CreateKeyInput{Name: "rw"}); !errors.Is(err, apperr.ErrForbidden) {
		t.Errorf("read-only key creating key err = %v, want ErrForbidden", err)
	}
}

func TestAdminScopeOnlyForAdminAccounts(t *testing.T) {
	a := testutil.NewApp(t)
	admin := testutil.Admin(t, a)
	member := testutil.Member(t, a, admin, "dev1")

	_, err := a.Accounts.CreateKey(ctx, admin, account.CreateKeyInput{AccountID: member.AccountID, Name: "k", Scopes: []principal.Scope{principal.ScopeAdmin}})
	if !errors.Is(err, apperr.ErrInvalid) {
		t.Errorf("admin scope for member err = %v, want ErrInvalid", err)
	}

	// An admin's everyday read/write key is not an admin principal.
	rw, err := a.Accounts.CreateKey(ctx, admin, account.CreateKeyInput{Name: "everyday"})
	if err != nil {
		t.Fatalf("create key: %v", err)
	}
	if p := testutil.Authenticate(t, a, rw.Secret); p.IsAdmin() {
		t.Error("read/write key of an admin account acts as admin")
	}
}

func TestRecoverRevokesOldKeys(t *testing.T) {
	a := testutil.NewApp(t)
	_, first, err := a.Accounts.Bootstrap(ctx, account.CreateAccountInput{Name: "root"})
	if err != nil {
		t.Fatalf("bootstrap: %v", err)
	}
	_, recovered, err := a.Accounts.Recover(ctx, "root")
	if err != nil {
		t.Fatalf("recover: %v", err)
	}
	if _, err := a.Accounts.Authenticate(ctx, first.Secret); !errors.Is(err, apperr.ErrUnauthenticated) {
		t.Errorf("old key still works after recover: %v", err)
	}
	if p := testutil.Authenticate(t, a, recovered.Secret); !p.IsAdmin() {
		t.Error("recovered key is not admin")
	}
	if _, _, err := a.Accounts.Recover(ctx, "nobody"); !errors.Is(err, apperr.ErrNotFound) {
		t.Errorf("recover unknown err = %v, want ErrNotFound", err)
	}
}

func TestDisableAccount(t *testing.T) {
	a := testutil.NewApp(t)
	admin := testutil.Admin(t, a)
	member := testutil.Member(t, a, admin, "dev1")

	if err := a.Accounts.DisableAccount(ctx, admin, admin.AccountID); !errors.Is(err, apperr.ErrConflict) {
		t.Errorf("disable last admin err = %v, want ErrConflict", err)
	}
	keys, err := a.Accounts.ListKeys(ctx, admin, member.AccountID)
	if err != nil || len(keys) != 1 {
		t.Fatalf("list keys = %v, %v", keys, err)
	}
	if err := a.Accounts.DisableAccount(ctx, admin, member.AccountID); err != nil {
		t.Fatalf("disable: %v", err)
	}
	keys, _ = a.Accounts.ListKeys(ctx, admin, member.AccountID)
	if keys[0].RevokedAt == nil {
		t.Error("disabling the account did not revoke its key")
	}
}

func TestAccountActionsAreAudited(t *testing.T) {
	a := testutil.NewApp(t)
	admin := testutil.Admin(t, a)
	member := testutil.Member(t, a, admin, "dev1")

	events, err := a.Audit.List(ctx, admin, audit.Filter{})
	if err != nil {
		t.Fatalf("list audit: %v", err)
	}
	var actions []string
	for _, e := range events {
		actions = append(actions, e.Action)
	}
	want := []string{audit.ActionBootstrap, audit.ActionAccountCreate, audit.ActionKeyCreate}
	if strings.Join(actions, ",") != strings.Join(want, ",") {
		t.Errorf("audit actions = %v, want %v", actions, want)
	}
	if _, err := a.Audit.List(ctx, member, audit.Filter{}); !errors.Is(err, apperr.ErrForbidden) {
		t.Errorf("member reading audit err = %v, want ErrForbidden", err)
	}
}

func TestAccountValidationAndConflicts(t *testing.T) {
	a := testutil.NewApp(t)
	admin := testutil.Admin(t, a)

	for _, in := range []account.CreateAccountInput{
		{Name: "", Kind: account.KindApp},
		{Name: "has space", Kind: account.KindApp},
		{Name: "ok", Kind: "robot"},
		{Name: "ok", Kind: account.KindApp, Role: "owner"},
	} {
		if _, err := a.Accounts.CreateAccount(ctx, admin, in); !errors.Is(err, apperr.ErrInvalid) {
			t.Errorf("CreateAccount(%+v) err = %v, want ErrInvalid", in, err)
		}
	}
	acc, err := a.Accounts.CreateAccount(ctx, admin, account.CreateAccountInput{Name: "ci", Kind: account.KindApp})
	if err != nil || acc.Role != principal.RoleMember {
		t.Fatalf("create = %+v, %v; want a member", acc, err)
	}
	if _, err := a.Accounts.CreateAccount(ctx, admin, account.CreateAccountInput{Name: "ci", Kind: account.KindTeam}); !errors.Is(err, apperr.ErrConflict) {
		t.Errorf("duplicate account err = %v, want ErrConflict", err)
	}
}

func TestGetAndListAccounts(t *testing.T) {
	a := testutil.NewApp(t)
	admin := testutil.Admin(t, a)
	alice := testutil.Member(t, a, admin, "alice")
	bob := testutil.Member(t, a, admin, "bob")

	if _, err := a.Accounts.GetAccount(ctx, alice, bob.AccountID); !errors.Is(err, apperr.ErrNotFound) {
		t.Errorf("member get other account err = %v, want ErrNotFound", err)
	}
	if got, err := a.Accounts.GetAccount(ctx, admin, bob.AccountID); err != nil || got.Name != "bob" {
		t.Errorf("admin get = %+v, %v", got, err)
	}
	if _, err := a.Accounts.GetAccount(ctx, admin, "missing"); !errors.Is(err, apperr.ErrNotFound) {
		t.Errorf("get missing err = %v, want ErrNotFound", err)
	}
	list, err := a.Accounts.ListAccounts(ctx, admin)
	if err != nil || len(list) != 3 || list[0].Name != "admin" || list[1].Name != "alice" {
		t.Errorf("admin list = %+v, %v; want admin, alice, bob", list, err)
	}
}

func TestDisableAccountRules(t *testing.T) {
	a := testutil.NewApp(t)
	admin := testutil.Admin(t, a)
	alice := testutil.Member(t, a, admin, "alice")

	if err := a.Accounts.DisableAccount(ctx, alice, admin.AccountID); !errors.Is(err, apperr.ErrForbidden) {
		t.Errorf("member disable err = %v, want ErrForbidden", err)
	}
	if err := a.Accounts.DisableAccount(ctx, admin, "missing"); !errors.Is(err, apperr.ErrNotFound) {
		t.Errorf("disable missing err = %v, want ErrNotFound", err)
	}
	if err := a.Accounts.DisableAccount(ctx, admin, alice.AccountID); err != nil {
		t.Fatalf("disable: %v", err)
	}
	if err := a.Accounts.DisableAccount(ctx, admin, alice.AccountID); err != nil {
		t.Errorf("disabling twice err = %v, want nil", err)
	}
	if _, err := a.Accounts.CreateKey(ctx, admin, account.CreateKeyInput{AccountID: alice.AccountID, Name: "k"}); !errors.Is(err, apperr.ErrInvalid) {
		t.Errorf("key for disabled account err = %v, want ErrInvalid", err)
	}

	// A second admin can be disabled while another remains.
	second, err := a.Accounts.CreateAccount(ctx, admin, account.CreateAccountInput{Name: "admin2", Kind: account.KindDeveloper, Role: principal.RoleAdmin})
	if err != nil {
		t.Fatalf("create admin2: %v", err)
	}
	if err := a.Accounts.DisableAccount(ctx, admin, second.ID); err != nil {
		t.Errorf("disable second admin: %v", err)
	}
	if _, _, err := a.Accounts.Recover(ctx, "admin2"); !errors.Is(err, apperr.ErrInvalid) {
		t.Errorf("recover disabled admin err = %v, want ErrInvalid", err)
	}
	if _, _, err := a.Accounts.Recover(ctx, "alice"); !errors.Is(err, apperr.ErrInvalid) {
		t.Errorf("recover member err = %v, want ErrInvalid", err)
	}
}

func TestKeyInputValidation(t *testing.T) {
	a := testutil.NewApp(t)
	admin := testutil.Admin(t, a)

	if _, err := a.Accounts.CreateKey(ctx, admin, account.CreateKeyInput{}); !errors.Is(err, apperr.ErrInvalid) {
		t.Errorf("key without name err = %v, want ErrInvalid", err)
	}
	for _, name := range []string{"has space", "-leading", "tab\there", "esc\x1b[31m", "über", strings.Repeat("k", 65)} {
		if _, err := a.Accounts.CreateKey(ctx, admin, account.CreateKeyInput{Name: name}); !errors.Is(err, apperr.ErrInvalid) {
			t.Errorf("key name %q err = %v, want ErrInvalid", name, err)
		}
	}
	for _, name := range []string{"mcp-read", "laptop.2026", "ci_bot", strings.Repeat("k", 64)} {
		if _, err := a.Accounts.CreateKey(ctx, admin, account.CreateKeyInput{Name: name}); err != nil {
			t.Errorf("key name %q: %v", name, err)
		}
	}
	if _, err := a.Accounts.CreateKey(ctx, admin, account.CreateKeyInput{Name: "k", Scopes: []principal.Scope{"delete"}}); !errors.Is(err, apperr.ErrInvalid) {
		t.Errorf("unknown scope err = %v, want ErrInvalid", err)
	}
	if _, err := a.Accounts.CreateKey(ctx, admin, account.CreateKeyInput{AccountID: "missing", Name: "k"}); !errors.Is(err, apperr.ErrNotFound) {
		t.Errorf("key for missing account err = %v, want ErrNotFound", err)
	}
	issued, err := a.Accounts.CreateKey(ctx, admin, account.CreateKeyInput{Name: "default"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if got := issued.Key.Scopes; len(got) != 2 || got[0] != principal.ScopeRead || got[1] != principal.ScopeWrite {
		t.Errorf("default scopes = %v, want [read write]", got)
	}
}

func TestRevokeKeyRules(t *testing.T) {
	a := testutil.NewApp(t)
	admin := testutil.Admin(t, a)
	alice := testutil.Member(t, a, admin, "alice")
	bob := testutil.Member(t, a, admin, "bob")

	own, err := a.Accounts.CreateKey(ctx, alice, account.CreateKeyInput{Name: "spare"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := a.Accounts.RevokeKey(ctx, bob, own.Key.ID); !errors.Is(err, apperr.ErrNotFound) {
		t.Errorf("bob revoking alice's key err = %v, want ErrNotFound", err)
	}
	if err := a.Accounts.RevokeKey(ctx, alice, "missing"); !errors.Is(err, apperr.ErrNotFound) {
		t.Errorf("revoke missing err = %v, want ErrNotFound", err)
	}

	ro, err := a.Accounts.CreateKey(ctx, alice, account.CreateKeyInput{Name: "ro", Scopes: []principal.Scope{principal.ScopeRead}})
	if err != nil {
		t.Fatalf("create ro: %v", err)
	}
	roP := testutil.Authenticate(t, a, ro.Secret)
	if err := a.Accounts.RevokeKey(ctx, roP, own.Key.ID); !errors.Is(err, apperr.ErrForbidden) {
		t.Errorf("read-only key revoking err = %v, want ErrForbidden", err)
	}

	if err := a.Accounts.RevokeKey(ctx, alice, own.Key.ID); err != nil {
		t.Fatalf("revoke own: %v", err)
	}
	if err := a.Accounts.RevokeKey(ctx, alice, own.Key.ID); err != nil {
		t.Errorf("revoking twice err = %v, want nil", err)
	}
	keys, err := a.Accounts.ListKeys(ctx, alice, "")
	if err != nil || len(keys) != 3 {
		t.Fatalf("list own keys = %v, %v", keys, err)
	}
}

func TestAuthenticateRecordsLastUse(t *testing.T) {
	a := testutil.NewApp(t)
	admin := testutil.Admin(t, a)
	keys, err := a.Accounts.ListKeys(ctx, admin, "")
	if err != nil || len(keys) != 1 || keys[0].LastUsedAt == nil {
		t.Errorf("bootstrap key last_used_at not set after authenticate: %+v, %v", keys, err)
	}
}
