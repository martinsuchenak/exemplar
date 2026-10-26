package cmd

import (
	"strings"
	"testing"

	"github.com/martinsuchenak/exemplar/internal/account"
)

func TestAccountsCmdExists(t *testing.T) {
	cmd := accountsCmd()
	if cmd == nil {
		t.Fatal("accountsCmd() should not return nil")
	}
	if cmd.Name != "accounts" {
		t.Errorf("expected command name %q, got %q", "accounts", cmd.Name)
	}
}

func TestAccountsCommands(t *testing.T) {
	s := startServer(t)

	out, err := s.run(t, s.adminKey, "accounts", "create", "--name", "ci-bot", "--kind", "app")
	if err != nil || !strings.Contains(out, "ci-bot") || !strings.Contains(out, "app") || !strings.Contains(out, "member") {
		t.Fatalf("create = %q, %v", out, err)
	}
	var accounts []account.Account
	s.runJSON(t, s.adminKey, &accounts, "accounts", "list")
	if len(accounts) != 2 {
		t.Fatalf("accounts = %+v", accounts)
	}
	table, err := s.run(t, s.adminKey, "accounts", "list")
	if err != nil || !strings.HasPrefix(table, "ID") || !strings.Contains(table, "root") {
		t.Errorf("table = %q, %v", table, err)
	}

	var bot account.Account
	for _, a := range accounts {
		if a.Name == "ci-bot" {
			bot = a
		}
	}
	if out, err := s.run(t, s.adminKey, "accounts", "disable", bot.ID); err != nil || !strings.Contains(out, "Disabled account "+bot.ID) {
		t.Errorf("disable = %q, %v", out, err)
	}

	member := s.member(t, "alice")
	if _, err := s.run(t, member, "accounts", "create", "--name", "x"); err == nil || !strings.Contains(err.Error(), "admin") {
		t.Errorf("member create err = %v", err)
	}
	if _, err := s.run(t, s.adminKey, "accounts", "create", "--name", "x", "--kind", "robot"); err == nil {
		t.Error("unknown kind accepted")
	}
}
