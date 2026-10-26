package cmd

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/martinsuchenak/exemplar/internal/dataset"
	"github.com/martinsuchenak/exemplar/internal/fixture"
	"github.com/martinsuchenak/exemplar/internal/testutil"
)

func TestForkCmdExists(t *testing.T) {
	cmd := forkCmd()
	if cmd == nil {
		t.Fatal("forkCmd() should not return nil")
	}
	if cmd.Name != "fork" {
		t.Errorf("expected command name %q, got %q", "fork", cmd.Name)
	}
}

func TestForkAndPromoteCommands(t *testing.T) {
	s := startServer(t)
	member := s.member(t, "alice")

	var lib dataset.Dataset
	s.runJSON(t, s.adminKey, &lib, "datasets", "create", "--name", "lib", "--scope", "global")
	src := filepath.Join(t.TempDir(), "a.txt")
	if err := os.WriteFile(src, []byte("a"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := s.run(t, s.adminKey, "upload", lib.ID, src); err != nil {
		t.Fatalf("upload: %v", err)
	}

	out, err := s.run(t, member, "fork", lib.ID, "--name", "my-lib", "--description", "playground")
	if err != nil || !strings.Contains(out, "my-lib") || !strings.Contains(out, "Copied 1 fixtures.") {
		t.Fatalf("fork = %q, %v", out, err)
	}
	var forked dataset.CopyResult
	s.runJSON(t, member, &forked, "fork", lib.ID, "--name", "second")
	if forked.Dataset.Name != "second" || forked.Copied != 1 {
		t.Errorf("json fork = %+v", forked)
	}

	if _, err := s.run(t, member, "promote", forked.Dataset.ID); err == nil || !strings.Contains(err.Error(), "admin") {
		t.Errorf("member promote err = %v", err)
	}
	var promoted dataset.CopyResult
	s.runJSON(t, s.adminKey, &promoted, "promote", forked.Dataset.ID, "--name", "lib")
	if promoted.Dataset.Version != 2 || promoted.Dataset.Scope != dataset.ScopeGlobal || promoted.Dataset.FrozenAt == nil {
		t.Errorf("promoted = %+v", promoted.Dataset)
	}
	if _, err := s.run(t, member, "fork", "missing"); err == nil {
		t.Error("forking a missing dataset succeeded")
	}
}

func TestRenderCopyMentionsSkipped(t *testing.T) {
	s := startServer(t)
	admin := testutil.Authenticate(t, s.app, s.adminKey)
	q, err := s.app.Datasets.Create(context.Background(), admin, dataset.CreateInput{Name: "inbox", Scope: dataset.ScopeQuarantine})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.app.Fixtures.Create(context.Background(), admin, fixture.CreateInput{DatasetID: q.ID, Name: "r.eml", Source: fixture.SourceAnonymised, Content: strings.NewReader("r")}); err != nil {
		t.Fatal(err)
	}
	out, err := s.run(t, s.adminKey, "promote", q.ID, "--name", "real")
	if err != nil || !strings.Contains(out, "Copied 0 fixtures; skipped 1 real-derived fixtures that are not approved.") {
		t.Errorf("promote output = %q, %v", out, err)
	}
}
