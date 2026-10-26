package cmd

import (
	"os"
	"strings"
	"testing"
)

func TestInitCmdExists(t *testing.T) {
	cmd := initCmd()
	if cmd == nil {
		t.Fatal("initCmd should not return nil")
	}
	if cmd.Name != "init" {
		t.Errorf("expected command name 'init', got %q", cmd.Name)
	}
}

func TestInitWritesConfigOnce(t *testing.T) {
	t.Chdir(t.TempDir())

	if _, err := runCLI(t, "init"); err != nil {
		t.Fatalf("init: %v", err)
	}
	b, err := os.ReadFile("exemplar-config.toml")
	if err != nil {
		t.Fatalf("reading config: %v", err)
	}
	for _, section := range []string{"[log]", "[server]", "[database]", "[storage]", "[client]"} {
		if !strings.Contains(string(b), section) {
			t.Errorf("config missing %s:\n%s", section, b)
		}
	}

	if _, err := runCLI(t, "init"); err == nil {
		t.Error("init overwrote an existing config file")
	}
}
