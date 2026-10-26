package cmd

import (
	"strings"
	"testing"

	"github.com/martinsuchenak/exemplar/internal/person"
)

func TestPersonsCmdExists(t *testing.T) {
	cmd := personsCmd()
	if cmd == nil {
		t.Fatal("personsCmd() should not return nil")
	}
	if cmd.Name != "persons" {
		t.Errorf("expected command name %q, got %q", "persons", cmd.Name)
	}
}

func TestPersonsCommands(t *testing.T) {
	s := startServer(t)
	member := s.member(t, "alice")

	out, err := s.run(t, member, "persons", "generate", "--count", "3", "--seed", "4")
	if err != nil || !strings.Contains(out, "Seed 4.") || !strings.Contains(out, ".example") {
		t.Fatalf("generate = %q, %v", out, err)
	}
	var gen person.GenerateResult
	s.runJSON(t, member, &gen, "persons", "generate", "--count", "2")
	if len(gen.People) != 2 || gen.Seed == 0 {
		t.Errorf("json generate = %+v", gen)
	}

	var all []person.Person
	s.runJSON(t, member, &all, "persons", "list")
	if len(all) != 5 {
		t.Fatalf("list = %d", len(all))
	}
	var page []person.Person
	s.runJSON(t, member, &page, "persons", "list", "--limit", "2", "--offset", "1", "--company", all[0].Company)
	if len(page) > 2 {
		t.Errorf("page = %d", len(page))
	}
	table, err := s.run(t, member, "persons", "list")
	if err != nil || !strings.HasPrefix(table, "ID") || !strings.Contains(table, all[0].Email) {
		t.Errorf("table = %q, %v", table, err)
	}
	if _, err := s.run(t, member, "persons", "generate", "--count", "0"); err == nil {
		t.Error("count 0 accepted")
	}
	if _, err := s.run(t, member, "persons", "generate", "--seed", "x"); err == nil {
		t.Error("bad seed accepted")
	}
}
