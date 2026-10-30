// Package testutil builds a real app (SQLite file and blob directory in a
// temp dir) and principals for service tests.
package testutil

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/paularlott/logger"

	"github.com/martinsuchenak/exemplar/internal/account"
	"github.com/martinsuchenak/exemplar/internal/app"
	"github.com/martinsuchenak/exemplar/internal/principal"
)

// NewApp opens an app on a SQLite file and blob directory in t.TempDir().
func NewApp(t *testing.T) *app.App {
	return NewAppWith(t, nil)
}

// NewAppWith is NewApp with a chance to change the config (paths are set).
func NewAppWith(t *testing.T, configure func(*app.Config)) *app.App {
	t.Helper()
	return NewAppLogged(t, nil, configure)
}

// NewAppLogged is NewAppWith with a logger.
func NewAppLogged(t *testing.T, log logger.Logger, configure func(*app.Config)) *app.App {
	t.Helper()
	dir := t.TempDir()
	cfg := app.Config{
		DBPath:  filepath.Join(dir, "exemplar.db"),
		BlobDir: filepath.Join(dir, "blobs"),
	}
	if configure != nil {
		configure(&cfg)
	}
	a, err := app.Open(context.Background(), log, cfg)
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

// Logs is a logger that keeps what it is given, one line per call: the
// level, the message and the keys and values.
type Logs struct {
	mu    sync.Mutex
	lines *[]string
	with  []any
}

// NewLogs returns an empty Logs.
func NewLogs() *Logs { return &Logs{lines: &[]string{}} }

// Lines returns what was logged so far.
func (l *Logs) Lines() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]string(nil), *l.lines...)
}

func (l *Logs) add(level, msg string, kv []any) {
	l.mu.Lock()
	defer l.mu.Unlock()
	parts := []string{level, msg}
	for _, v := range append(append([]any{}, l.with...), kv...) {
		parts = append(parts, fmt.Sprint(v))
	}
	*l.lines = append(*l.lines, strings.Join(parts, " "))
}

func (l *Logs) Trace(msg string, kv ...any) { l.add("TRACE", msg, kv) }
func (l *Logs) Debug(msg string, kv ...any) { l.add("DEBUG", msg, kv) }
func (l *Logs) Info(msg string, kv ...any)  { l.add("INFO", msg, kv) }
func (l *Logs) Warn(msg string, kv ...any)  { l.add("WARN", msg, kv) }
func (l *Logs) Error(msg string, kv ...any) { l.add("ERROR", msg, kv) }
func (l *Logs) Fatal(msg string, kv ...any) { l.add("FATAL", msg, kv) }

func (l *Logs) With(key string, value any) logger.Logger {
	return &Logs{lines: l.lines, with: append(append([]any{}, l.with...), key, value)}
}
func (l *Logs) WithError(err error) logger.Logger    { return l.With("error", err) }
func (l *Logs) WithGroup(group string) logger.Logger { return l.With("group", group) }
