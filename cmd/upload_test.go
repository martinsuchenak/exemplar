package cmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/martinsuchenak/exemplar/internal/dataset"
	"github.com/martinsuchenak/exemplar/internal/fixture"
)

func TestUploadCmdExists(t *testing.T) {
	cmd := uploadCmd()
	if cmd == nil {
		t.Fatal("uploadCmd() should not return nil")
	}
	if cmd.Name != "upload" {
		t.Errorf("expected command name %q, got %q", "upload", cmd.Name)
	}
}

func TestUploadFilesAndDirectories(t *testing.T) {
	s := startServer(t)
	member := s.member(t, "alice")
	var d dataset.Dataset
	s.runJSON(t, member, &d, "datasets", "create", "--name", "mine")

	dir := t.TempDir()
	write := func(name, body string) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		return p
	}
	sub := filepath.Join(dir, "emails")
	if err := os.Mkdir(sub, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, n := range []string{"b.eml", "a.eml"} {
		if err := os.WriteFile(filepath.Join(sub, n), []byte("Subject: "+n), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	// Nested directories are not uploaded.
	if err := os.Mkdir(filepath.Join(sub, "nested"), 0o700); err != nil {
		t.Fatal(err)
	}
	single := write("report.pdf", "%PDF-1.4")

	var uploaded []fixture.Fixture
	s.runJSON(t, member, &uploaded, "upload", d.ID, sub, single, "--scenario", "smoke", "--tags", "x,y", "--source", "synthetic", "--generator", "test")
	if len(uploaded) != 3 || uploaded[0].Name != "a.eml" || uploaded[1].Name != "b.eml" || uploaded[2].Name != "report.pdf" {
		t.Fatalf("uploaded = %+v", uploaded)
	}
	if f := uploaded[0]; f.Kind != fixture.KindEmail || f.Scenario != "smoke" || len(f.Tags) != 2 || f.Source != fixture.SourceSynthetic || f.Generator != "test" {
		t.Errorf("metadata = %+v", f)
	}

	// A failure on one file is reported, the others still upload.
	out, err := s.run(t, member, "upload", d.ID, single, write("new.txt", "n"))
	if err == nil || !strings.Contains(err.Error(), "1 of 2 files failed") || !strings.Contains(out, "new.txt") {
		t.Errorf("partial upload = %q, %v", out, err)
	}
}

func TestExpandPaths(t *testing.T) {
	dir := t.TempDir()
	if _, err := expandPaths(nil); err == nil {
		t.Error("no paths accepted")
	}
	if _, err := expandPaths([]string{filepath.Join(dir, "missing")}); err == nil {
		t.Error("missing path accepted")
	}
	if _, err := expandPaths([]string{dir}); err == nil || !strings.Contains(err.Error(), "no files") {
		t.Errorf("empty directory err = %v", err)
	}
}
