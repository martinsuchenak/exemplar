package cmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/martinsuchenak/exemplar/internal/dataset"
	"github.com/martinsuchenak/exemplar/internal/fixture"
)

func TestFixturesCmdExists(t *testing.T) {
	cmd := fixturesCmd()
	if cmd == nil {
		t.Fatal("fixturesCmd() should not return nil")
	}
	if cmd.Name != "fixtures" {
		t.Errorf("expected command name %q, got %q", "fixtures", cmd.Name)
	}
}

func TestFixturesListAndDownload(t *testing.T) {
	s := startServer(t)
	member := s.member(t, "alice")
	var d dataset.Dataset
	s.runJSON(t, member, &d, "datasets", "create", "--name", "mine")
	dir := t.TempDir()
	src := filepath.Join(dir, "hello.eml")
	if err := os.WriteFile(src, []byte("Subject: hi"), 0o600); err != nil {
		t.Fatal(err)
	}
	var uploaded []fixture.Fixture
	s.runJSON(t, member, &uploaded, "upload", d.ID, src, "--scenario", "greeting", "--tags", "t1")

	var list []fixture.Fixture
	s.runJSON(t, member, &list, "fixtures", "list", d.ID, "--kind", "email", "--format", "eml", "--scenario", "greeting", "--tag", "t1", "--review-state", "not-required", "--limit", "10", "--offset", "0")
	if len(list) != 1 {
		t.Fatalf("filtered list = %+v", list)
	}
	table, err := s.run(t, member, "fixtures", "list", d.ID)
	if err != nil || !strings.Contains(table, "hello.eml") || !strings.Contains(table, "greeting") {
		t.Errorf("table = %q, %v", table, err)
	}
	if _, err := s.run(t, member, "fixtures", "list", d.ID, "--kind", "video"); err == nil {
		t.Error("unknown kind accepted")
	}

	out, err := s.run(t, member, "fixtures", "download", uploaded[0].ID)
	if err != nil || out != "Subject: hi" {
		t.Errorf("download to stdout = %q, %v", out, err)
	}
	dst := filepath.Join(dir, "copy.eml")
	if _, err := s.run(t, member, "fixtures", "download", uploaded[0].ID, "--output", dst); err != nil {
		t.Fatalf("download to file: %v", err)
	}
	if b, _ := os.ReadFile(dst); string(b) != "Subject: hi" {
		t.Errorf("file = %q", b)
	}
	if _, err := s.run(t, member, "fixtures", "download", uploaded[0].ID, "--output", dst); err == nil {
		t.Error("download overwrote an existing file")
	}
	if _, err := s.run(t, member, "fixtures", "download", "missing"); err == nil {
		t.Error("downloading a missing fixture succeeded")
	}
}
