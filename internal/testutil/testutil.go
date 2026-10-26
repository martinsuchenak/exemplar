// Package testutil builds a real app (SQLite file and blob directory in a
// temp dir) and principals for service tests.
package testutil

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/martinsuchenak/exemplar/internal/account"
	"github.com/martinsuchenak/exemplar/internal/app"
	"github.com/martinsuchenak/exemplar/internal/principal"
)

// NewApp opens an app on a SQLite file and blob directory in t.TempDir().
func NewApp(t *testing.T) *app.App {
	t.Helper()
	dir := t.TempDir()
	a, err := app.Open(context.Background(), nil, app.Config{
		DBPath:  filepath.Join(dir, "exemplar.db"),
		BlobDir: filepath.Join(dir, "blobs"),
	})
	if err != nil {
		t.Fatalf("opening app: %v", err)
	}
	t.Cleanup(func() { _ = a.Close() })
	return a
}

// Admin bootstraps the first admin and returns its principal (admin scope).
func Admin(t *testing.T, a *app.App) principal.Principal {
	t.Helper()
	_, issued, err := a.Accounts.Bootstrap(context.Background(), account.CreateAccountInput{Name: "admin"})
	if err != nil {
		t.Fatalf("bootstrap: %v", err)
	}
	return Authenticate(t, a, issued.Secret)
}

// Member creates a member account with a read/write key.
func Member(t *testing.T, a *app.App, admin principal.Principal, name string) principal.Principal {
	t.Helper()
	ctx := context.Background()
	acc, err := a.Accounts.CreateAccount(ctx, admin, account.CreateAccountInput{Name: name, Kind: account.KindDeveloper})
	if err != nil {
		t.Fatalf("creating account %s: %v", name, err)
	}
	issued, err := a.Accounts.CreateKey(ctx, admin, account.CreateKeyInput{AccountID: acc.ID, Name: "test"})
	if err != nil {
		t.Fatalf("creating key for %s: %v", name, err)
	}
	return Authenticate(t, a, issued.Secret)
}

// Authenticate resolves secret to its principal, failing the test on error.
func Authenticate(t *testing.T, a *app.App, secret string) principal.Principal {
	t.Helper()
	p, err := a.Accounts.Authenticate(context.Background(), secret)
	if err != nil {
		t.Fatalf("authenticate: %v", err)
	}
	return p
}
