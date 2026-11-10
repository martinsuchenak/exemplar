package smtppush_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/martinsuchenak/exemplar/internal/smtppush"
	"github.com/martinsuchenak/exemplar/internal/synth"
)

// shapeMessages is one generated email of every shape, from fresh people.
func shapeMessages(t *testing.T) []smtppush.Message {
	t.Helper()
	g := synth.New(uint64(time.Now().UnixNano()))
	company := g.CompanyName()
	people := []synth.Person{g.Person(company), g.Person(company), g.Person(g.CompanyName())}
	var msgs []smtppush.Message
	for _, shape := range synth.Shapes {
		emails, err := g.Emails(synth.EmailInput{Shape: shape, People: people})
		if err != nil {
			t.Fatal(err)
		}
		for _, e := range emails {
			msgs = append(msgs, smtppush.Message{FixtureID: string(shape), Name: e.Subject, Data: e.Data})
		}
	}
	return msgs
}

// pushAll pushes msgs as one new run and fails the test if any message fails.
func pushAll(t *testing.T, smtpAddr string, msgs []smtppush.Message) string {
	t.Helper()
	run := smtppush.NewRunID(time.Now())
	results, err := smtppush.Push(ctx, smtppush.Options{Addr: smtpAddr, Run: run}, msgs)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range results {
		if r.Error != "" {
			t.Errorf("%s (%s): %s", r.Name, r.FixtureID, r.Error)
		}
	}
	return run
}

// getJSON decodes a GET of url into v.
func getJSON(t *testing.T, url string, v any) {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET %s: %s", url, resp.Status)
	}
	if err := json.NewDecoder(resp.Body).Decode(v); err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
}

// TestRealMailpit pushes every generated email shape into a running Mailpit
// and checks Mailpit tagged and stored them all. It runs only when
// EXEMPLAR_TEST_MAILPIT_SMTP (host:port) and EXEMPLAR_TEST_MAILPIT_API
// (e.g. http://127.0.0.1:8025) are set; see docs/development.md.
func TestRealMailpit(t *testing.T) {
	smtpAddr, api := os.Getenv("EXEMPLAR_TEST_MAILPIT_SMTP"), os.Getenv("EXEMPLAR_TEST_MAILPIT_API")
	if smtpAddr == "" || api == "" {
		t.Skip("set EXEMPLAR_TEST_MAILPIT_SMTP and EXEMPLAR_TEST_MAILPIT_API to run against a real Mailpit")
	}
	msgs := shapeMessages(t)
	run := pushAll(t, smtpAddr, msgs)

	var found struct {
		MessagesCount int `json:"messages_count"`
	}
	for i := 0; i < 20; i++ {
		getJSON(t, api+"/api/v1/search?query="+url.QueryEscape("tag:"+smtppush.Tag(run)), &found)
		if found.MessagesCount == len(msgs) {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Errorf("Mailpit has %d messages tagged %s, want %d", found.MessagesCount, smtppush.Tag(run), len(msgs))
}

// TestRealSMTP4Dev pushes every generated email shape into a running
// smtp4dev and finds them by their X-Exemplar-Run header, the way a test
// using smtp4dev would: smtp4dev has no tags and its search covers only
// subject, sender and recipients, so it lists the newest messages and reads
// each one's headers. It deletes the run's messages afterwards, so it can run
// against a shared smtp4dev. It runs only when EXEMPLAR_TEST_SMTP4DEV_SMTP
// (host:port) and EXEMPLAR_TEST_SMTP4DEV_API (e.g. http://127.0.0.1:5080)
// are set; see docs/development.md.
func TestRealSMTP4Dev(t *testing.T) {
	smtpAddr, api := os.Getenv("EXEMPLAR_TEST_SMTP4DEV_SMTP"), strings.TrimSuffix(os.Getenv("EXEMPLAR_TEST_SMTP4DEV_API"), "/")
	if smtpAddr == "" || api == "" {
		t.Skip("set EXEMPLAR_TEST_SMTP4DEV_SMTP and EXEMPLAR_TEST_SMTP4DEV_API to run against a real smtp4dev")
	}
	msgs := shapeMessages(t)
	run := pushAll(t, smtpAddr, msgs)

	// Headers never change, so each message's are read once: runOf and
	// fixtureOf hold every message seen so far, the run's and others'.
	runOf, fixtureOf := map[string]string{}, map[string]string{}
	scan := func() (ids []string) {
		var page struct {
			Results []struct{ ID string }
		}
		// Newest first; the run's messages are among the newest.
		getJSON(t, fmt.Sprintf("%s/api/messages?pageSize=%d&sortColumn=receivedDate&sortIsDescending=true", api, len(msgs)+50), &page)
		for _, m := range page.Results {
			if _, ok := runOf[m.ID]; !ok {
				var detail struct {
					Headers []struct{ Name, Value string }
				}
				getJSON(t, api+"/api/messages/"+url.PathEscape(m.ID), &detail)
				runOf[m.ID] = ""
				for _, h := range detail.Headers {
					switch {
					case strings.EqualFold(h.Name, smtppush.RunHeader):
						runOf[m.ID] = h.Value
					case strings.EqualFold(h.Name, smtppush.FixtureHeader):
						fixtureOf[m.ID] = h.Value
					}
				}
			}
			if runOf[m.ID] == run {
				ids = append(ids, m.ID)
			}
		}
		return ids
	}
	// Delete the run's messages whether the test passes or not, including
	// any that arrived after the last look.
	t.Cleanup(func() {
		for _, id := range scan() {
			req, _ := http.NewRequest(http.MethodDelete, api+"/api/messages/"+url.PathEscape(id), nil)
			if resp, err := http.DefaultClient.Do(req); err == nil {
				_ = resp.Body.Close()
			}
		}
	})

	var ids []string
	for i := 0; i < 50; i++ {
		if ids = scan(); len(ids) >= len(msgs) {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if len(ids) != len(msgs) {
		t.Fatalf("smtp4dev has %d messages with %s: %s, want %d", len(ids), smtppush.RunHeader, run, len(msgs))
	}
	fixtures := map[string]bool{}
	for _, id := range ids {
		fixtures[fixtureOf[id]] = true
	}
	for _, shape := range synth.Shapes {
		if !fixtures[string(shape)] {
			t.Errorf("no message with %s: %s", smtppush.FixtureHeader, shape)
		}
	}
}
