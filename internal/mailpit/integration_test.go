package mailpit_test

import (
	"encoding/json"
	"net/http"
	"net/url"
	"os"
	"testing"
	"time"

	"github.com/martinsuchenak/exemplar/internal/mailpit"
	"github.com/martinsuchenak/exemplar/internal/synth"
)

// TestRealMailpit pushes every generated email shape into a running Mailpit
// and checks Mailpit tagged and stored them all. It runs only when
// EXEMPLAR_TEST_MAILPIT_SMTP (host:port) and EXEMPLAR_TEST_MAILPIT_API
// (e.g. http://127.0.0.1:8025) are set; see docs/development.md.
func TestRealMailpit(t *testing.T) {
	smtpAddr, api := os.Getenv("EXEMPLAR_TEST_MAILPIT_SMTP"), os.Getenv("EXEMPLAR_TEST_MAILPIT_API")
	if smtpAddr == "" || api == "" {
		t.Skip("set EXEMPLAR_TEST_MAILPIT_SMTP and EXEMPLAR_TEST_MAILPIT_API to run against a real Mailpit")
	}
	g := synth.New(uint64(time.Now().UnixNano()))
	company := g.CompanyName()
	people := []synth.Person{g.Person(company), g.Person(company), g.Person(g.CompanyName())}
	var msgs []mailpit.Message
	for _, shape := range synth.Shapes {
		emails, err := g.Emails(synth.EmailInput{Shape: shape, People: people})
		if err != nil {
			t.Fatal(err)
		}
		for _, e := range emails {
			msgs = append(msgs, mailpit.Message{FixtureID: string(shape), Name: e.Subject, Data: e.Data})
		}
	}
	run := mailpit.NewRunID(time.Now())
	results, err := mailpit.Push(ctx, mailpit.Options{Addr: smtpAddr, Run: run}, msgs)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range results {
		if r.Error != "" {
			t.Errorf("%s (%s): %s", r.Name, r.FixtureID, r.Error)
		}
	}

	var found struct {
		MessagesCount int `json:"messages_count"`
	}
	for i := 0; i < 20; i++ {
		resp, err := http.Get(api + "/api/v1/search?query=" + url.QueryEscape("tag:"+mailpit.Tag(run)))
		if err != nil {
			t.Fatal(err)
		}
		_ = json.NewDecoder(resp.Body).Decode(&found)
		_ = resp.Body.Close()
		if found.MessagesCount == len(msgs) {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Errorf("Mailpit has %d messages tagged %s, want %d", found.MessagesCount, mailpit.Tag(run), len(msgs))
}
