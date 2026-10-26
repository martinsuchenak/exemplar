package cmd

import (
	"bytes"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/martinsuchenak/exemplar/internal/account"
	"github.com/martinsuchenak/exemplar/internal/principal"
)

var secretRe = regexp.MustCompile(`Secret:\s+(exm_[a-z0-9]+)`)

func TestBootstrapCmdExists(t *testing.T) {
	cmd := bootstrapCmd()
	if cmd == nil {
		t.Fatal("bootstrapCmd() should not return nil")
	}
	if cmd.Name != "bootstrap" {
		t.Errorf("expected command name %q, got %q", "bootstrap", cmd.Name)
	}
}

func storageArgs(dir string) []string {
	return []string{"--db-path", filepath.Join(dir, "x.db"), "--blob-dir", filepath.Join(dir, "blobs")}
}

func TestBootstrapCreatesAdminOnce(t *testing.T) {
	dir := t.TempDir()
	args := append([]string{"bootstrap", "--name", "root", "--kind", "team"}, storageArgs(dir)...)

	out, err := runCLI(t, args...)
	if err != nil {
		t.Fatalf("bootstrap: %v", err)
	}
	if !strings.Contains(out, "Account: root (") || !strings.Contains(out, ", admin)") {
		t.Errorf("output does not name the admin account:\n%s", out)
	}
	if !secretRe.MatchString(out) {
		t.Errorf("output has no secret:\n%s", out)
	}

	if _, err := runCLI(t, args...); err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Errorf("second bootstrap err = %v, want 'already exists'", err)
	}
}

func TestBootstrapRecover(t *testing.T) {
	dir := t.TempDir()
	first, err := runCLI(t, append([]string{"bootstrap"}, storageArgs(dir)...)...)
	if err != nil {
		t.Fatalf("bootstrap: %v", err)
	}
	second, err := runCLI(t, append([]string{"bootstrap", "--recover", "--name", "admin"}, storageArgs(dir)...)...)
	if err != nil {
		t.Fatalf("recover: %v", err)
	}
	a, b := secretRe.FindStringSubmatch(first), secretRe.FindStringSubmatch(second)
	if a == nil || b == nil || a[1] == b[1] {
		t.Errorf("recover did not print a new secret:\n%s\n---\n%s", first, second)
	}

	if _, err := runCLI(t, append([]string{"bootstrap", "--recover", "--name", "ghost"}, storageArgs(dir)...)...); err == nil {
		t.Error("recovering an unknown account succeeded")
	}
}

func TestBootstrapRejectsBadInput(t *testing.T) {
	dir := t.TempDir()
	if _, err := runCLI(t, append([]string{"bootstrap", "--kind", "robot"}, storageArgs(dir)...)...); err == nil {
		t.Error("bootstrap with an unknown kind succeeded")
	}
	if _, err := runCLI(t, "bootstrap", "--db-path", "", "--blob-dir", ""); err == nil {
		t.Error("bootstrap without storage paths succeeded")
	}
}

func TestPrintIssuedKey(t *testing.T) {
	var buf bytes.Buffer
	printIssuedKey(&buf,
		&account.Account{ID: "a1", Name: "root", Role: principal.RoleAdmin},
		&account.IssuedKey{Key: account.APIKey{ID: "k1", Name: "bootstrap", CreatedAt: time.Now()}, Secret: "exm_secret"},
	)
	for _, want := range []string{"root (a1, admin)", "bootstrap (k1)", "exm_secret", "shown only once"} {
		if !strings.Contains(buf.String(), want) {
			t.Errorf("output missing %q:\n%s", want, buf.String())
		}
	}
}
