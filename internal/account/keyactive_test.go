package account_test

import (
	"context"
	"testing"

	"github.com/martinsuchenak/exemplar/internal/account"
	"github.com/martinsuchenak/exemplar/internal/testutil"
)

func TestKeyActive(t *testing.T) {
	ctx := context.Background()
	a := testutil.NewApp(t)
	admin := testutil.Admin(t, a)
	acc, err := a.Accounts.CreateAccount(ctx, admin, account.CreateAccountInput{Name: "member", Kind: account.KindDeveloper})
	if err != nil {
		t.Fatal(err)
	}
	issued, err := a.Accounts.CreateKey(ctx, admin, account.CreateKeyInput{AccountID: acc.ID, Name: "k"})
	if err != nil {
		t.Fatal(err)
	}
	active := func() bool {
		ok, err := a.Accounts.KeyActive(ctx, issued.Key.ID)
		if err != nil {
			t.Fatalf("KeyActive: %v", err)
		}
		return ok
	}
	if !active() {
		t.Error("fresh key reported inactive")
	}
	if err := a.Accounts.RevokeKey(ctx, admin, issued.Key.ID); err != nil {
		t.Fatal(err)
	}
	if active() {
		t.Error("revoked key reported active")
	}

	// A disabled account's keys are inactive too.
	issued2, err := a.Accounts.CreateKey(ctx, admin, account.CreateKeyInput{AccountID: acc.ID, Name: "k2"})
	if err != nil {
		t.Fatal(err)
	}
	if err := a.Accounts.DisableAccount(ctx, admin, acc.ID); err != nil {
		t.Fatal(err)
	}
	if ok, _ := a.Accounts.KeyActive(ctx, issued2.Key.ID); ok {
		t.Error("key of a disabled account reported active")
	}
	if ok, _ := a.Accounts.KeyActive(ctx, ""); ok {
		t.Error("empty key id reported active")
	}
}
