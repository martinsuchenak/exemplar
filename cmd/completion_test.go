package cmd

import (
	"strings"
	"testing"
)

func TestCompletionCmdExists(t *testing.T) {
	cmd := completionCmd()
	if cmd == nil {
		t.Fatal("completionCmd should not return nil")
	}
	if cmd.Name != "completion" {
		t.Errorf("expected command name 'completion', got %q", cmd.Name)
	}
}

func TestCompletionShells(t *testing.T) {
	for _, shell := range []string{"bash", "zsh", "fish"} {
		out, err := runCLI(t, "completion", "--shell", shell)
		if err != nil {
			t.Errorf("%s: %v", shell, err)
		}
		if !strings.Contains(strings.ToLower(out), shell) {
			t.Errorf("%s output does not mention the shell: %q", shell, out)
		}
	}
	if _, err := runCLI(t, "completion", "--shell", "tcsh"); err == nil {
		t.Error("unsupported shell succeeded")
	}
}
