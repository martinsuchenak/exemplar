package mailpit

import (
	"context"
	"net/smtp"
	"testing"
)

func TestPlainAuthWorksWithoutTLS(t *testing.T) {
	remote := &smtp.ServerInfo{Name: "mailpit", TLS: false, Auth: []string{"PLAIN"}}
	// The standard library refuses here, which is why plainAuth exists.
	if _, _, err := smtp.PlainAuth("", "ci", "pw", "mailpit").Start(remote); err == nil {
		t.Fatal("net/smtp now allows plaintext AUTH; plainAuth may be unnecessary")
	}
	mech, resp, err := plainAuth{username: "ci", password: "pw"}.Start(remote)
	if err != nil || mech != "PLAIN" || string(resp) != "\x00ci\x00pw" {
		t.Errorf("start = %s %q %v", mech, resp, err)
	}
	if _, err := (plainAuth{}).Next([]byte("challenge"), true); err == nil {
		t.Error("unexpected challenge accepted")
	}
	if out, err := (plainAuth{}).Next(nil, false); err != nil || out != nil {
		t.Errorf("next = %q %v", out, err)
	}
}

func TestRunIDs(t *testing.T) {
	for run, ok := range map[string]bool{"20261027-091502-3fa1": true, "ci.42_a": true, "a,b": false, "x\r\nBcc: e@x.test": false, "": false, "-x": false} {
		if ValidRun(run) != ok {
			t.Errorf("ValidRun(%q) != %v", run, ok)
		}
	}
	if _, _, _, err := Prepare(Message{Data: []byte("From: a@x.test\r\nTo: b@x.test\r\n\r\nhi\r\n")}, Options{Run: "a,b"}); err == nil {
		t.Error("Prepare accepted a bad run ID")
	}
	if _, err := Push(context.Background(), Options{Addr: "127.0.0.1:1", Run: "bad run"}, nil); err == nil {
		t.Error("Push accepted a bad run ID")
	}
}
