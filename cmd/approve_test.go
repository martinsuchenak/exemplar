package cmd

import "testing"

func TestApproveCmdExists(t *testing.T) {
	cmd := approveCmd()
	if cmd == nil {
		t.Fatal("approveCmd() should not return nil")
	}
	if cmd.Name != "approve" {
		t.Errorf("expected command name %q, got %q", "approve", cmd.Name)
	}
}
