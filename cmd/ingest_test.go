package cmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/martinsuchenak/exemplar/internal/anonymise/anonymisetest"
	"github.com/martinsuchenak/exemplar/internal/app"
	"github.com/martinsuchenak/exemplar/internal/dataset"
	"github.com/martinsuchenak/exemplar/internal/fixture"
	"github.com/martinsuchenak/exemplar/internal/ingest"
)

func TestIngestCmdExists(t *testing.T) {
	cmd := ingestCmd()
	if cmd == nil {
		t.Fatal("ingestCmd() should not return nil")
	}
	if cmd.Name != "ingest" {
		t.Errorf("expected command name %q, got %q", "ingest", cmd.Name)
	}
}

// anonymisingServer is a live server whose anonymiser finds "Priya".
func anonymisingServer(t *testing.T) (*liveServer, string, string) {
	t.Helper()
	s := startServerWith(t, func(c *app.Config) {
		c.AnonKey = anonymisetest.Key
		c.AnonDetector = anonymisetest.Detector{Names: []string{"Priya"}}
	})
	var d dataset.Dataset
	s.runJSON(t, s.adminKey, &d, "datasets", "create", "--name", "inbox", "--scope", "quarantine")

	dir := t.TempDir()
	for name, data := range map[string][]byte{
		"a.eml": anonymisetest.Email("Priya Shah", "Tom Reid", "acme.com.au", "Quote", "Hi Tom, Priya"),
		"b.msg": []byte("binary"),
	} {
		if err := os.WriteFile(filepath.Join(dir, name), data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return s, d.ID, dir
}

func TestIngestDryRunAndStore(t *testing.T) {
	s, dsID, dir := anonymisingServer(t)

	out, err := s.run(t, s.adminKey, "ingest", dsID, filepath.Join(dir, "a.eml"), "--dry-run", "--batch", "try")
	if err != nil {
		t.Fatalf("dry run: %v\n%s", err, out)
	}
	for _, want := range []string{"checked", `"Priya" -> "`, "Batch try, profile default, key k-"} {
		if !strings.Contains(out, want) {
			t.Errorf("dry run output misses %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "approve or reject") {
		t.Errorf("dry run asks for review:\n%s", out)
	}

	out, err = s.run(t, s.adminKey, "ingest", dsID, dir, "--batch", "real")
	if err == nil || !strings.Contains(err.Error(), "1 of 2 files failed") {
		t.Errorf("ingest err = %v", err)
	}
	for _, want := range []string{"real-001.eml", "failed: MSG files", "approve or reject"} {
		if !strings.Contains(out, want) {
			t.Errorf("ingest output misses %q:\n%s", want, out)
		}
	}

	var res ingest.Result
	s.runJSON(t, s.adminKey, &res, "ingest", dsID, filepath.Join(dir, "a.eml"), "--batch", "json")
	if res.Batch != "json" || len(res.Files) != 1 || res.Files[0].FixtureID == "" {
		t.Errorf("json result = %+v", res)
	}

	member := s.member(t, "alice")
	if _, err := s.run(t, member, "ingest", dsID, dir); err == nil || !strings.Contains(err.Error(), "admin") {
		t.Errorf("member ingest err = %v", err)
	}
	if _, err := s.run(t, s.adminKey, "ingest", dsID, filepath.Join(dir, "missing.eml")); err == nil {
		t.Error("missing file accepted")
	}
}

func TestApproveAndReject(t *testing.T) {
	s, dsID, dir := anonymisingServer(t)
	var a, b ingest.Result
	s.runJSON(t, s.adminKey, &a, "ingest", dsID, filepath.Join(dir, "a.eml"), "--batch", "one")
	s.runJSON(t, s.adminKey, &b, "ingest", dsID, filepath.Join(dir, "a.eml"), "--batch", "two")
	first, second := a.Files[0].FixtureID, b.Files[0].FixtureID

	var approved []fixture.Fixture
	s.runJSON(t, s.adminKey, &approved, "approve", first)
	if len(approved) != 1 || approved[0].ReviewState != fixture.ReviewApproved {
		t.Errorf("approved = %+v", approved)
	}
	out, err := s.run(t, s.adminKey, "reject", second, "missing")
	if err == nil || !strings.Contains(err.Error(), "1 of 2 fixtures failed") || !strings.Contains(out, "rejected") {
		t.Errorf("reject = %q, %v", out, err)
	}
	member := s.member(t, "alice")
	if _, err := s.run(t, member, "approve", first); err == nil {
		t.Error("member approved a fixture")
	}
}

func TestAnonymiserKey(t *testing.T) {
	dir := t.TempDir()
	if k, err := anonymiserKey("", "from-env"); err != nil || string(k) != "from-env" {
		t.Errorf("env key = %q, %v", k, err)
	}
	if k, err := anonymiserKey("", ""); err != nil || len(k) != 0 {
		t.Errorf("no key = %q, %v", k, err)
	}
	if _, err := anonymiserKey(filepath.Join(dir, "missing"), ""); err == nil {
		t.Error("missing key file accepted")
	}
	open := filepath.Join(dir, "open.key")
	if err := os.WriteFile(open, []byte("secret-secret-secret"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := anonymiserKey(open, ""); err == nil || !strings.Contains(err.Error(), "chmod 600") {
		t.Errorf("readable key file err = %v", err)
	}
	private := filepath.Join(dir, "private.key")
	if err := os.WriteFile(private, []byte("secret-secret-secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	if k, err := anonymiserKey(private, "ignored"); err != nil || string(k) != "secret-secret-secret" {
		t.Errorf("file key = %q, %v", k, err)
	}
}
