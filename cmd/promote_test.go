package cmd

import "testing"

func TestPromoteCmdExists(t *testing.T) {
	cmd := promoteCmd()
	if cmd == nil {
		t.Fatal("promoteCmd() should not return nil")
	}
	if cmd.Name != "promote" {
		t.Errorf("expected command name %q, got %q", "promote", cmd.Name)
	}
}
