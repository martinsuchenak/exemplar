package cmd

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/paularlott/cli"
	cli_toml "github.com/paularlott/cli/toml"

	"github.com/martinsuchenak/exemplar/internal/dataset"
	"github.com/martinsuchenak/exemplar/internal/ingest"
	"github.com/martinsuchenak/exemplar/internal/smtppush"
	"github.com/martinsuchenak/exemplar/internal/smtppush/smtptest"
)

func TestPushSMTPCmdExists(t *testing.T) {
	cmd := pushSMTPCmd()
	if cmd == nil {
		t.Fatal("pushSMTPCmd() should not return nil")
	}
	if cmd.Name != "push-smtp" {
		t.Errorf("expected command name %q, got %q", "push-smtp", cmd.Name)
	}
}

func TestPushSMTPCommand(t *testing.T) {
	s := startServer(t)
	member := s.member(t, "alice")
	smtpSrv := smtptest.Start(t)
	var d dataset.Dataset
	s.runJSON(t, member, &d, "datasets", "create", "--name", "mail")
	if _, err := s.run(t, member, "push-smtp", d.ID, "--smtp", smtpSrv.Addr); err == nil || !strings.Contains(err.Error(), "no matching .eml") {
		t.Errorf("empty dataset err = %v", err)
	}
	if _, err := s.run(t, member, "generate", d.ID, "--kind", "emails", "--count", "3", "--shapes", "plain,bounce,reply", "--seed", "5"); err != nil {
		t.Fatal(err)
	}
	// A non-email fixture is never pushed.
	if _, err := s.run(t, member, "generate", d.ID, "--kind", "files", "--count", "1", "--formats", "txt", "--seed", "6"); err != nil {
		t.Fatal(err)
	}

	// The old name still works.
	out, err := s.run(t, member, "push-mailpit", d.ID, "--smtp", smtpSrv.Addr, "--run", "ci-7")
	if err != nil || !strings.Contains(out, "Pushed 3 of 3 emails") || !strings.Contains(out, "header X-Exemplar-Run") || !strings.Contains(out, "exemplar-run-ci-7") {
		t.Fatalf("push = %q, %v", out, err)
	}
	got := smtpSrv.Received()
	if len(got) != 3 {
		t.Fatalf("server received %d", len(got))
	}
	for _, m := range got {
		if !strings.HasPrefix(m.Data, "X-Tags: exemplar-run-ci-7\r\nX-Exemplar-Run: ci-7\r\nX-Exemplar-Fixture: ") {
			t.Errorf("message not tagged:\n%.120s", m.Data)
		}
	}

	var res struct {
		Run     string            `json:"run"`
		Tag     string            `json:"tag"`
		DryRun  bool              `json:"dry_run"`
		Results []smtppush.Result `json:"results"`
	}
	s.runJSON(t, member, &res, "push-smtp", d.ID, "--smtp", smtpSrv.Addr, "--scenario", "bounce", "--to", "qa@exemplar.test,qa2@exemplar.test", "--raw")
	if len(res.Results) != 1 || strings.Join(res.Results[0].To, ",") != "qa@exemplar.test,qa2@exemplar.test" || res.Run == "" || res.DryRun {
		t.Errorf("redirect = %+v", res)
	}
	last := smtpSrv.Received()[3]
	if strings.HasPrefix(last.Data, "X-") || len(last.To) != 2 {
		t.Errorf("raw message changed or wrong envelope: %+v", last.To)
	}

	before := len(smtpSrv.Received())
	out, err = s.run(t, member, "push-smtp", d.ID, "--smtp", smtpSrv.Addr, "--dry-run", "--limit", "2")
	if err != nil || !strings.Contains(out, "Would push 2 of 2") || strings.Count(out, "would send") != 2 || len(smtpSrv.Received()) != before {
		t.Errorf("dry run = %q, %v", out, err)
	}

	// Failures are reported per message and make the command fail.
	rejecting := smtptest.Start(t, func(srv *smtptest.Server) { srv.RejectRcpt = "nobody@exemplar.test" })
	out, err = s.run(t, member, "push-smtp", d.ID, "--smtp", rejecting.Addr, "--to", "nobody@exemplar.test")
	if err == nil || !strings.Contains(err.Error(), "3 of 3 emails failed") || !strings.Contains(out, "failed: RCPT TO") {
		t.Errorf("rejected push = %q, %v", out, err)
	}

	bob := s.member(t, "bob")
	if _, err := s.run(t, bob, "push-smtp", d.ID, "--smtp", smtpSrv.Addr); err == nil {
		t.Error("bob pushed alice's workspace")
	}
	if _, err := s.run(t, member, "push-smtp", d.ID, "--smtp", "no-port"); err == nil {
		t.Error("bad SMTP address accepted")
	}
}

func TestPushSMTPSkipsUnapprovedRealData(t *testing.T) {
	s, dsID, dir := anonymisingServer(t)
	var res ingest.Result
	s.runJSON(t, s.adminKey, &res, "ingest", dsID, filepath.Join(dir, "a.eml"), "--batch", "q")
	smtpSrv := smtptest.Start(t)

	// Even an admin key, which may read it for review, does not send it.
	if _, err := s.run(t, s.adminKey, "push-smtp", dsID, "--smtp", smtpSrv.Addr); err == nil || !strings.Contains(err.Error(), "may be sent") {
		t.Errorf("unapproved push err = %v", err)
	}
	if n := len(smtpSrv.Received()); n != 0 {
		t.Fatalf("%d unapproved emails reached the SMTP server", n)
	}
	if _, err := s.run(t, s.adminKey, "approve", res.Files[0].FixtureID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.run(t, s.adminKey, "push-smtp", dsID, "--smtp", smtpSrv.Addr, "--run", "ok-1"); err != nil {
		t.Fatalf("approved push: %v", err)
	}
	if n := len(smtpSrv.Received()); n != 1 {
		t.Errorf("approved emails sent = %d", n)
	}
	if _, err := s.run(t, s.adminKey, "push-smtp", dsID, "--smtp", smtpSrv.Addr, "--run", "a,b"); err == nil || !strings.Contains(err.Error(), "run ID") {
		t.Errorf("bad run err = %v", err)
	}
}

// The SMTP flags read the new names first and fall back to the ones
// push-mailpit used, so existing configs and CI jobs keep working.
func TestSMTPFlagSources(t *testing.T) {
	t.Chdir(t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	for _, v := range []string{"EXEMPLAR_SMTP", "MAILPIT_SMTP", "EXEMPLAR_SMTP_USER", "MAILPIT_SMTP_USER", "EXEMPLAR_SMTP_PASSWORD", "MAILPIT_SMTP_PASSWORD"} {
		t.Setenv(v, "")
		_ = os.Unsetenv(v)
	}
	resolve := func(config string) (addr, user string) {
		t.Helper()
		if config != "" {
			if err := os.WriteFile("exemplar-config.toml", []byte(config), 0o600); err != nil {
				t.Fatal(err)
			}
		} else {
			_ = os.Remove("exemplar-config.toml")
		}
		name := "exemplar-config.toml"
		root := &cli.Command{
			Name:       "exemplar",
			ConfigFile: cli_toml.NewConfigFile(&name, ConfigSearchPath(&name)),
			Flags:      smtpFlags(),
			Run: func(_ context.Context, cmd *cli.Command) error {
				addr, user = cmd.GetString("smtp"), cmd.GetString("smtp-user")+"/"+cmd.GetString("smtp-password")
				return nil
			},
		}
		old := os.Args
		os.Args = []string{"exemplar"}
		defer func() { os.Args = old }()
		if err := root.Execute(context.Background()); err != nil {
			t.Fatal(err)
		}
		return addr, user
	}

	if addr, _ := resolve(""); addr != "localhost:1025" {
		t.Errorf("default = %q", addr)
	}
	if addr, _ := resolve("[mailpit]\nsmtp = \"old:1025\"\n"); addr != "old:1025" {
		t.Errorf("[mailpit] smtp = %q", addr)
	}
	if addr, _ := resolve("[mailpit]\nsmtp = \"old:1025\"\n[smtp]\naddr = \"new:2525\"\n"); addr != "new:2525" {
		t.Errorf("[smtp] addr beside [mailpit] = %q", addr)
	}
	t.Setenv("MAILPIT_SMTP", "oldenv:1025")
	t.Setenv("MAILPIT_SMTP_USER", "olduser")
	t.Setenv("MAILPIT_SMTP_PASSWORD", "oldpw")
	if addr, user := resolve(""); addr != "oldenv:1025" || user != "olduser/oldpw" {
		t.Errorf("MAILPIT_SMTP = %q, %q", addr, user)
	}
	t.Setenv("EXEMPLAR_SMTP", "newenv:2525")
	t.Setenv("EXEMPLAR_SMTP_USER", "newuser")
	t.Setenv("EXEMPLAR_SMTP_PASSWORD", "newpw")
	if addr, user := resolve(""); addr != "newenv:2525" || user != "newuser/newpw" {
		t.Errorf("EXEMPLAR_SMTP beside MAILPIT_SMTP = %q, %q", addr, user)
	}
	// An env var beats the config file, old name or not.
	t.Setenv("EXEMPLAR_SMTP", "")
	_ = os.Unsetenv("EXEMPLAR_SMTP")
	if addr, _ := resolve("[smtp]\naddr = \"new:2525\"\n"); addr != "oldenv:1025" {
		t.Errorf("MAILPIT_SMTP beside [smtp] addr = %q", addr)
	}
}
