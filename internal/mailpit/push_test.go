package mailpit_test

import (
	"context"
	"net"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/martinsuchenak/exemplar/internal/mailpit"
	"github.com/martinsuchenak/exemplar/internal/mailpit/mailpittest"
)

var ctx = context.Background()

const sample = "From: \"Ann Lee\" <ann@a.example>\r\nTo: bob@b.example, \"Cy\" <cy@c.example>\r\nCc: dee@d.example\r\nSubject: hi\r\n\r\nbody\r\n.leading dot\r\n"

func TestPushTagsAndDelivers(t *testing.T) {
	srv := mailpittest.Start(t)
	results, err := mailpit.Push(ctx, mailpit.Options{Addr: srv.Addr, Run: "r1"}, []mailpit.Message{
		{FixtureID: "f1", Name: "one.eml", Data: []byte(sample)},
		{FixtureID: "f2", Name: "two.eml", Data: []byte(sample)},
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range results {
		if r.Error != "" || r.From != "ann@a.example" || strings.Join(r.To, ",") != "bob@b.example,cy@c.example,dee@d.example" {
			t.Errorf("result = %+v", r)
		}
	}
	got := srv.Received()
	if len(got) != 2 {
		t.Fatalf("received %d", len(got))
	}
	m := got[0]
	if m.From != "ann@a.example" || len(m.To) != 3 {
		t.Errorf("envelope = %s -> %v", m.From, m.To)
	}
	for _, h := range []string{"X-Tags: exemplar-run-r1\r\n", "X-Exemplar-Run: r1\r\n", "X-Exemplar-Fixture: f1\r\n"} {
		if !strings.HasPrefix(m.Data, "X-") || !strings.Contains(m.Data, h) {
			t.Errorf("data missing %q:\n%s", h, m.Data)
		}
	}
	if !strings.HasSuffix(m.Data, sample) {
		t.Errorf("message body changed (dot-stuffing must round-trip):\n%q", m.Data)
	}
}

func TestPushRawAndRedirect(t *testing.T) {
	srv := mailpittest.Start(t)
	_, err := mailpit.Push(ctx, mailpit.Options{Addr: srv.Addr, Run: "r2", Raw: true, To: []string{"run@exemplar.test"}},
		[]mailpit.Message{{FixtureID: "f1", Data: []byte(sample)}})
	if err != nil {
		t.Fatal(err)
	}
	got := srv.Received()
	if len(got) != 1 || got[0].Data != sample || strings.Join(got[0].To, ",") != "run@exemplar.test" {
		t.Errorf("received = %+v", got)
	}
}

func TestPushWithAuth(t *testing.T) {
	srv := mailpittest.Start(t, func(s *mailpittest.Server) { s.RequireAuth = true })
	results, err := mailpit.Push(ctx, mailpit.Options{Addr: srv.Addr, Username: "qa", Password: "pw"}, []mailpit.Message{{Data: []byte(sample)}})
	if err != nil || results[0].Error != "" {
		t.Fatalf("push = %+v, %v", results, err)
	}
	if got := srv.Received(); len(got) != 1 || got[0].Auth != "qa:pw" {
		t.Errorf("auth = %+v", got)
	}
	results, _ = mailpit.Push(ctx, mailpit.Options{Addr: srv.Addr}, []mailpit.Message{{Data: []byte(sample)}})
	if !strings.Contains(results[0].Error, "MAIL FROM") {
		t.Errorf("unauthenticated push error = %q", results[0].Error)
	}
}

func TestPerMessageFailuresDoNotStopOthers(t *testing.T) {
	srv := mailpittest.Start(t, func(s *mailpittest.Server) { s.RejectRcpt = "cy@c.example" })
	noRcpt := "From: a@a.example\r\nSubject: x\r\n\r\nbody\r\n"
	results, err := mailpit.Push(ctx, mailpit.Options{Addr: srv.Addr}, []mailpit.Message{
		{Name: "rejected", Data: []byte(sample)},
		{Name: "garbage", Data: []byte("not an email")},
		{Name: "no recipients", Data: []byte(noRcpt)},
		{Name: "ok", Data: []byte("From: a@a.example\r\nTo: z@z.example\r\n\r\nx\r\n")},
	})
	if err != nil {
		t.Fatal(err)
	}
	wants := []string{"RCPT TO cy@c.example", "not a valid email", "no recipients", ""}
	for i, want := range wants {
		if want == "" && results[i].Error != "" || want != "" && !strings.Contains(results[i].Error, want) {
			t.Errorf("%s: error %q, want %q", results[i].Name, results[i].Error, want)
		}
	}
	if len(srv.Received()) != 1 {
		t.Errorf("received %d, want only the good message", len(srv.Received()))
	}
}

func TestPrepareDefaults(t *testing.T) {
	from, to, data, err := mailpit.Prepare(mailpit.Message{FixtureID: "a\r\nInjected: x", Data: []byte("To: z@z.example\r\n\r\nx")}, mailpit.Options{})
	if err != nil || from != mailpit.DefaultFrom || len(to) != 1 {
		t.Fatalf("prepare = %s %v %v", from, to, err)
	}
	if strings.Contains(string(data), "X-Tags") || strings.Contains(string(data), "\r\nInjected:") {
		t.Errorf("headers = %q", data)
	}
}

func TestOptionsAndConnectionErrors(t *testing.T) {
	msgs := []mailpit.Message{{Data: []byte(sample)}}
	for _, addr := range []string{"", "no-port"} {
		if _, err := mailpit.Push(ctx, mailpit.Options{Addr: addr}, msgs); err == nil {
			t.Errorf("addr %q accepted", addr)
		}
	}
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	closed := ln.Addr().String()
	_ = ln.Close()
	results, err := mailpit.Push(ctx, mailpit.Options{Addr: closed, Timeout: time.Second}, msgs)
	if err != nil || !strings.Contains(results[0].Error, "connecting") {
		t.Errorf("closed port = %+v, %v", results, err)
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := mailpit.Push(cancelled, mailpit.Options{Addr: closed}, msgs); err == nil {
		t.Error("cancelled context did not stop the push")
	}
}

func TestRunIDsAndTags(t *testing.T) {
	id := mailpit.NewRunID(time.Date(2026, 10, 27, 9, 15, 2, 0, time.UTC))
	if !regexp.MustCompile(`^20261027-091502-[0-9a-f]{4}$`).MatchString(id) {
		t.Errorf("run id = %s", id)
	}
	if mailpit.Tag("x") != "exemplar-run-x" {
		t.Error("tag")
	}
}
