package cmd

import (
	"strings"
	"testing"

	"github.com/martinsuchenak/exemplar/internal/dataset"
	"github.com/martinsuchenak/exemplar/internal/fixture"
	"github.com/martinsuchenak/exemplar/internal/generate"
)

func TestGenerateCmdExists(t *testing.T) {
	cmd := generateCmd()
	if cmd == nil {
		t.Fatal("generateCmd() should not return nil")
	}
	if cmd.Name != "generate" {
		t.Errorf("expected command name %q, got %q", "generate", cmd.Name)
	}
}

func TestGenerateCommand(t *testing.T) {
	s := startServer(t)
	member := s.member(t, "alice")
	var d dataset.Dataset
	s.runJSON(t, member, &d, "datasets", "create", "--name", "gen")

	out, err := s.run(t, member, "generate", d.ID, "--kind", "files", "--count", "2", "--formats", "pdf,docx", "--seed", "21", "--people", "2", "--profile", "manufacturing", "--scenario", "smoke", "--tags", "a,b")
	if err != nil || !strings.Contains(out, ".pdf") || !strings.Contains(out, ".docx") || !strings.Contains(out, "with seed 21") {
		t.Fatalf("files = %q, %v", out, err)
	}
	var res generate.Result
	s.runJSON(t, member, &res, "generate", d.ID, "--kind", "emails", "--count", "2", "--shapes", "reply,bounce", "--seed", "22")
	if res.Seed != 22 || len(res.Fixtures) != 2 {
		t.Errorf("emails = %+v", res)
	}
	s.runJSON(t, member, &res, "generate", d.ID, "--kind", "records", "--count", "2", "--schemas", "contact,project", "--seed", "23")
	if len(res.Fixtures) != 2 || res.Fixtures[0].RecordSchema != "contact" {
		t.Errorf("records = %+v", res.Fixtures)
	}

	var contacts []fixture.Fixture
	s.runJSON(t, member, &contacts, "fixtures", "list", d.ID, "--record-schema", "contact", "--person", res.PersonIDs[0])
	if len(contacts) != 1 {
		t.Errorf("fixtures for person = %+v", contacts)
	}

	for _, args := range [][]string{
		{"generate", d.ID, "--kind", "videos"},
		{"generate", d.ID, "--kind", "files", "--seed", "abc"},
		{"generate", d.ID, "--kind", "files", "--seed", "0"},
		{"generate", d.ID, "--kind", "files", "--count", "0"},
	} {
		if _, err := s.run(t, member, args...); err == nil {
			t.Errorf("%v succeeded", args)
		}
	}
}

func TestParseSeed(t *testing.T) {
	if v, err := parseSeed(""); err != nil || v != 0 {
		t.Errorf("empty = %d, %v", v, err)
	}
	if v, err := parseSeed("18446744073709551615"); err != nil || v != 1<<64-1 {
		t.Errorf("max = %d, %v", v, err)
	}
	for _, bad := range []string{"0", "-1", "x"} {
		if _, err := parseSeed(bad); err == nil {
			t.Errorf("parseSeed(%q) accepted", bad)
		}
	}
}

func TestGenerateFromTemplates(t *testing.T) {
	s := startServer(t)
	member := s.member(t, "alice")
	var d dataset.Dataset
	s.runJSON(t, member, &d, "datasets", "create", "--name", "mine")
	var base generate.Result
	s.runJSON(t, member, &base, "generate", d.ID, "--kind", "emails", "--count", "1", "--shapes", "reply", "--seed", "5")

	var res generate.Result
	s.runJSON(t, member, &res, "generate", d.ID, "--kind", "emails", "--count", "3", "--from", base.Fixtures[0].ID, "--seed", "9")
	if len(res.Fixtures) != 3 {
		t.Fatalf("generated %d", len(res.Fixtures))
	}
	for _, f := range res.Fixtures {
		if f.Generator != "template:"+base.Fixtures[0].ID+" seed=9" || f.Tags[len(f.Tags)-1] != "template" {
			t.Errorf("fixture = %+v", f)
		}
	}
	if _, err := s.run(t, member, "generate", d.ID, "--kind", "emails", "--from", base.Fixtures[0].ID, "--shapes", "plain"); err == nil || !strings.Contains(err.Error(), "not both") {
		t.Errorf("shapes and from err = %v", err)
	}
}
