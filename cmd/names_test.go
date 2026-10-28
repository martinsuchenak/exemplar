package cmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/martinsuchenak/exemplar/internal/dataset"
	"github.com/martinsuchenak/exemplar/internal/fixture"
	"github.com/martinsuchenak/exemplar/internal/generate"
)

// Commands that take a dataset take its name as well as its id, and the
// list commands take --query.
func TestDatasetNamesAndQueries(t *testing.T) {
	s := startServer(t)
	member := s.member(t, "alice")
	var d dataset.Dataset
	s.runJSON(t, member, &d, "datasets", "create", "--name", "crm-contacts", "--description", "Contacts for the CRM")
	s.runJSON(t, member, &dataset.Dataset{}, "datasets", "create", "--name", "inbox")

	var gen generate.Result
	s.runJSON(t, member, &gen, "generate", "crm-contacts", "--kind", "files", "--count", "2", "--formats", "pdf,png", "--seed", "1")
	if len(gen.Fixtures) != 2 || gen.Fixtures[0].DatasetID != d.ID {
		t.Fatalf("generate by name = %+v", gen)
	}
	var list []fixture.Fixture
	s.runJSON(t, member, &list, "fixtures", "list", "crm-contacts")
	if len(list) != 2 {
		t.Errorf("fixtures list by name = %d fixtures", len(list))
	}
	pdf := gen.Fixtures[0].Name
	if gen.Fixtures[0].Format != "pdf" {
		pdf = gen.Fixtures[1].Name
	}
	s.runJSON(t, member, &list, "fixtures", "list", d.ID, "--query", strings.ToUpper(pdf))
	if len(list) != 1 || list[0].Name != pdf {
		t.Errorf("fixtures --query %q = %+v", pdf, list)
	}

	var found []dataset.Dataset
	s.runJSON(t, member, &found, "datasets", "list", "--query", "crm contacts")
	if len(found) != 1 || found[0].ID != d.ID {
		t.Errorf("datasets --query = %+v", found)
	}
	s.runJSON(t, member, &found, "datasets", "list", "--query", "the crm")
	if len(found) != 1 {
		t.Errorf("datasets --query over the description = %+v", found)
	}
	if _, err := s.run(t, member, "datasets", "list", "--query", strings.Repeat("x", 201)); err == nil {
		t.Error("a 201-character query was accepted")
	}

	var frozen dataset.Dataset
	s.runJSON(t, member, &frozen, "datasets", "freeze", "crm-contacts")
	if frozen.ID != d.ID || !frozen.Frozen() {
		t.Errorf("freeze by name = %+v", frozen)
	}
	path := filepath.Join(t.TempDir(), "out.tar.gz")
	if _, err := s.run(t, member, "export", "crm-contacts", "inbox", "--output", path); err != nil {
		t.Errorf("export by names: %v", err)
	}

	// Names follow the key's access rules: bob has no "crm-contacts".
	bob := s.member(t, "bob")
	if _, err := s.run(t, bob, "fixtures", "list", "crm-contacts"); err == nil || !strings.Contains(err.Error(), "not found") {
		t.Errorf("bob listing alice's dataset by name err = %v", err)
	}
	// Two "inbox" datasets an admin can read: the name is refused, listing ids.
	s.runJSON(t, bob, &dataset.Dataset{}, "datasets", "create", "--name", "inbox")
	if _, err := s.run(t, s.adminKey, "fixtures", "list", "inbox"); err == nil || !strings.Contains(err.Error(), "2 datasets") {
		t.Errorf("ambiguous name err = %v", err)
	}
	if _, err := s.run(t, member, "generate", "nothing", "--kind", "files"); err == nil {
		t.Error("generate into an unknown name succeeded")
	}
}

func TestFixturesDownloadAll(t *testing.T) {
	s := startServer(t)
	member := s.member(t, "alice")
	s.runJSON(t, member, &dataset.Dataset{}, "datasets", "create", "--name", "files")
	var gen generate.Result
	s.runJSON(t, member, &gen, "generate", "files", "--kind", "files", "--count", "3", "--formats", "pdf,png,csv", "--seed", "2")

	dir := filepath.Join(t.TempDir(), "out")
	out, err := s.run(t, member, "fixtures", "download-all", "files", "--dir", dir)
	if err != nil || !strings.Contains(out, "Downloaded 3 fixtures") {
		t.Fatalf("download-all = %q, %v", out, err)
	}
	if info, err := os.Stat(dir); err != nil || info.Mode().Perm() != 0o700 {
		t.Errorf("dir mode = %v, %v", info, err)
	}
	for _, f := range gen.Fixtures {
		b, err := os.ReadFile(filepath.Join(dir, f.Name))
		if err != nil || int64(len(b)) != f.Size {
			t.Errorf("%s: %d bytes, %v; want %d", f.Name, len(b), err, f.Size)
		}
		if info, _ := os.Stat(filepath.Join(dir, f.Name)); info.Mode().Perm() != 0o600 {
			t.Errorf("%s mode = %v", f.Name, info.Mode())
		}
	}
	// A rerun into the same directory writes nothing.
	if _, err := s.run(t, member, "fixtures", "download-all", "files", "--dir", dir); err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Errorf("rerun err = %v", err)
	}

	// Filters, and --json lists what was written.
	var done []fixture.Fixture
	s.runJSON(t, member, &done, "fixtures", "download-all", "files", "--dir", filepath.Join(t.TempDir(), "pdf"), "--format", "pdf")
	if len(done) != 1 || done[0].Format != "pdf" {
		t.Errorf("filtered download-all = %+v", done)
	}
	out, err = s.run(t, member, "fixtures", "download-all", "files", "--dir", filepath.Join(t.TempDir(), "none"), "--query", "nothing-matches")
	if err != nil || !strings.Contains(out, "Downloaded 0 fixtures") {
		t.Errorf("empty download-all = %q, %v", out, err)
	}
	if _, err := s.run(t, member, "fixtures", "download-all", "files", "--dir", t.TempDir(), "--kind", "video"); err == nil {
		t.Error("unknown kind accepted")
	}
}

func TestLocalName(t *testing.T) {
	for name, want := range map[string]string{
		"a.eml":    "a.eml",
		"../x.eml": "x.eml",
		"..":       "id-1",
		".":        "id-1",
		"/":        "id-1",
	} {
		if got := localName(fixture.Fixture{ID: "id-1", Name: name}); got != want {
			t.Errorf("localName(%q) = %q, want %q", name, got, want)
		}
	}
}
