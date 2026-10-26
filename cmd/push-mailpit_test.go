package cmd

import (
	"strings"
	"testing"

	"github.com/martinsuchenak/exemplar/internal/dataset"
	"github.com/martinsuchenak/exemplar/internal/mailpit"
	"github.com/martinsuchenak/exemplar/internal/mailpit/mailpittest"
)

func TestPushMailpitCmdExists(t *testing.T) {
	cmd := pushMailpitCmd()
	if cmd == nil {
		t.Fatal("pushMailpitCmd() should not return nil")
	}
	if cmd.Name != "push-mailpit" {
		t.Errorf("expected command name %q, got %q", "push-mailpit", cmd.Name)
	}
}

func TestPushMailpitCommand(t *testing.T) {
	s := startServer(t)
	member := s.member(t, "alice")
	smtpSrv := mailpittest.Start(t)
	var d dataset.Dataset
	s.runJSON(t, member, &d, "datasets", "create", "--name", "mail")
	if _, err := s.run(t, member, "push-mailpit", d.ID, "--smtp", smtpSrv.Addr); err == nil || !strings.Contains(err.Error(), "no matching .eml") {
		t.Errorf("empty dataset err = %v", err)
	}
	if _, err := s.run(t, member, "generate", d.ID, "--kind", "emails", "--count", "3", "--shapes", "plain,bounce,reply", "--seed", "5"); err != nil {
		t.Fatal(err)
	}
	// A non-email fixture is never pushed.
	if _, err := s.run(t, member, "generate", d.ID, "--kind", "files", "--count", "1", "--formats", "txt", "--seed", "6"); err != nil {
		t.Fatal(err)
	}

	out, err := s.run(t, member, "push-mailpit", d.ID, "--smtp", smtpSrv.Addr, "--run", "ci-7")
	if err != nil || !strings.Contains(out, "Pushed 3 of 3 emails") || !strings.Contains(out, "exemplar-run-ci-7") {
		t.Fatalf("push = %q, %v", out, err)
	}
	got := smtpSrv.Received()
	if len(got) != 3 {
		t.Fatalf("server received %d", len(got))
	}
	for _, m := range got {
		if !strings.HasPrefix(m.Data, "X-Tags: exemplar-run-ci-7\r\n") {
			t.Errorf("message not tagged:\n%.80s", m.Data)
		}
	}

	var res struct {
		Run     string           `json:"run"`
		Tag     string           `json:"tag"`
		DryRun  bool             `json:"dry_run"`
		Results []mailpit.Result `json:"results"`
	}
	s.runJSON(t, member, &res, "push-mailpit", d.ID, "--smtp", smtpSrv.Addr, "--scenario", "bounce", "--to", "qa@exemplar.test,qa2@exemplar.test", "--raw")
	if len(res.Results) != 1 || strings.Join(res.Results[0].To, ",") != "qa@exemplar.test,qa2@exemplar.test" || res.Run == "" || res.DryRun {
		t.Errorf("redirect = %+v", res)
	}
	last := smtpSrv.Received()[3]
	if strings.HasPrefix(last.Data, "X-") || len(last.To) != 2 {
		t.Errorf("raw message changed or wrong envelope: %+v", last.To)
	}

	before := len(smtpSrv.Received())
	out, err = s.run(t, member, "push-mailpit", d.ID, "--smtp", smtpSrv.Addr, "--dry-run", "--limit", "2")
	if err != nil || !strings.Contains(out, "Would push 2 of 2") || strings.Count(out, "would send") != 2 || len(smtpSrv.Received()) != before {
		t.Errorf("dry run = %q, %v", out, err)
	}

	// Failures are reported per message and make the command fail.
	rejecting := mailpittest.Start(t, func(srv *mailpittest.Server) { srv.RejectRcpt = "nobody@exemplar.test" })
	out, err = s.run(t, member, "push-mailpit", d.ID, "--smtp", rejecting.Addr, "--to", "nobody@exemplar.test")
	if err == nil || !strings.Contains(err.Error(), "3 of 3 emails failed") || !strings.Contains(out, "failed: RCPT TO") {
		t.Errorf("rejected push = %q, %v", out, err)
	}

	bob := s.member(t, "bob")
	if _, err := s.run(t, bob, "push-mailpit", d.ID, "--smtp", smtpSrv.Addr); err == nil {
		t.Error("bob pushed alice's workspace")
	}
	if _, err := s.run(t, member, "push-mailpit", d.ID, "--smtp", "no-port"); err == nil {
		t.Error("bad SMTP address accepted")
	}
}
