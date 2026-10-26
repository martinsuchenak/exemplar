package cmd

import (
	"strings"
	"testing"

	"github.com/martinsuchenak/exemplar/internal/dataset"
)

func TestDatasetsCmdExists(t *testing.T) {
	cmd := datasetsCmd()
	if cmd == nil {
		t.Fatal("datasetsCmd() should not return nil")
	}
	if cmd.Name != "datasets" {
		t.Errorf("expected command name %q, got %q", "datasets", cmd.Name)
	}
}

func TestDatasetsCommands(t *testing.T) {
	s := startServer(t)
	member := s.member(t, "alice")

	var d dataset.Dataset
	s.runJSON(t, member, &d, "datasets", "create", "--name", "replies", "--description", "reply shapes", "--tags", "Email, reply")
	if d.Scope != dataset.ScopeWorkspace || d.Description != "reply shapes" || len(d.Tags) != 2 {
		t.Fatalf("created = %+v", d)
	}
	if _, err := s.run(t, member, "datasets", "create", "--name", "lib", "--scope", "global"); err == nil {
		t.Error("member created a global dataset")
	}
	out, err := s.run(t, s.adminKey, "datasets", "create", "--name", "lib", "--scope", "global")
	if err != nil || !strings.Contains(out, "global") {
		t.Fatalf("admin global = %q, %v", out, err)
	}

	var all []dataset.Dataset
	s.runJSON(t, member, &all, "datasets", "list")
	if len(all) != 2 {
		t.Errorf("member sees %d datasets, want own workspace and the global one", len(all))
	}
	s.runJSON(t, member, &all, "datasets", "list", "--scope", "global", "--name", "lib")
	if len(all) != 1 || all[0].Name != "lib" {
		t.Errorf("filtered = %+v", all)
	}
	if table := mustRun(t, s, member); !strings.Contains(table, "replies") || !strings.HasPrefix(table, "ID") {
		t.Errorf("table = %q", table)
	}

	if out, err := s.run(t, member, "datasets", "freeze", d.ID); err != nil || !strings.Contains(out, d.ID) {
		t.Errorf("freeze = %q, %v", out, err)
	}
	if out, err := s.run(t, member, "datasets", "delete", d.ID); err != nil || !strings.Contains(out, "Deleted dataset") {
		t.Errorf("delete = %q, %v", out, err)
	}
	if _, err := s.run(t, member, "datasets", "delete", d.ID); err == nil {
		t.Error("deleting twice succeeded")
	}
}
