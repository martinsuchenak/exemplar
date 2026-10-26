package cmd

import "testing"

func TestRejectCmdExists(t *testing.T) {
	cmd := rejectCmd()
	if cmd == nil {
		t.Fatal("rejectCmd() should not return nil")
	}
	if cmd.Name != "reject" {
		t.Errorf("expected command name %q, got %q", "reject", cmd.Name)
	}
}
