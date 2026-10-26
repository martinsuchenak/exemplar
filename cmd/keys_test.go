package cmd

import (
	"strings"
	"testing"

	"github.com/martinsuchenak/exemplar/internal/account"
)

func TestKeysCmdExists(t *testing.T) {
	cmd := keysCmd()
	if cmd == nil {
		t.Fatal("keysCmd() should not return nil")
	}
	if cmd.Name != "keys" {
		t.Errorf("expected command name %q, got %q", "keys", cmd.Name)
	}
}

func TestKeysCommands(t *testing.T) {
	s := startServer(t)
	member := s.member(t, "alice")

	out, err := s.run(t, member, "keys", "create", "--name", "ci", "--scopes", "read")
	if err != nil || !strings.Contains(out, "Scopes:  read") || !secretRe.MatchString(out) || !strings.Contains(out, "shown only once") {
		t.Fatalf("create = %q, %v", out, err)
	}
	if _, err := s.run(t, member, "keys", "create", "--name", "root", "--scopes", "read,admin"); err == nil {
		t.Error("member created an admin-scope key")
	}

	var keys []account.APIKey
	s.runJSON(t, member, &keys, "keys", "list")
	if len(keys) != 2 {
		t.Fatalf("keys = %+v", keys)
	}
	table, err := s.run(t, member, "keys", "list")
	if err != nil || !strings.Contains(table, "PREFIX") || !strings.Contains(table, "exm_") {
		t.Errorf("table = %q, %v", table, err)
	}

	var adminView []account.APIKey
	s.runJSON(t, s.adminKey, &adminView, "keys", "list", "--account", keys[0].AccountID)
	if len(adminView) != 2 {
		t.Errorf("admin view of alice's keys = %d", len(adminView))
	}

	var ci account.APIKey
	for _, k := range keys {
		if k.Name == "ci" {
			ci = k
		}
	}
	if out, err := s.run(t, member, "keys", "revoke", ci.ID); err != nil || !strings.Contains(out, "Revoked key "+ci.ID) {
		t.Errorf("revoke = %q, %v", out, err)
	}
	if _, err := s.run(t, member, "keys", "revoke", "missing"); err == nil {
		t.Error("revoking a missing key succeeded")
	}
}

// mustRun lists datasets with key, a cheap authenticated call.
func mustRun(t *testing.T, s *liveServer, key string, extra ...string) string {
	t.Helper()
	out, err := s.run(t, key, append([]string{"datasets", "list"}, extra...)...)
	if err != nil {
		t.Fatalf("datasets list: %v", err)
	}
	return out
}
