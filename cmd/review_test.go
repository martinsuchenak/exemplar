package cmd

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/martinsuchenak/exemplar/internal/anonymise/anonymisetest"
	"github.com/martinsuchenak/exemplar/internal/fixture"
)

// reviewServer is an anonymising server with three unreviewed emails in its
// "inbox" quarantine dataset.
func reviewServer(t *testing.T) (*liveServer, string) {
	t.Helper()
	s, dsID, _ := anonymisingServer(t)
	dir := t.TempDir()
	for _, name := range []string{"a.eml", "b.eml", "c.eml"} {
		if err := os.WriteFile(filepath.Join(dir, name), anonymisetest.Email("Priya Shah", "Tom Reid", "acme.com.au", "Quote "+name, "Hi Tom, Priya"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.run(t, s.adminKey, "ingest", "inbox", dir, "--batch", "review"); err != nil {
		t.Fatalf("ingest: %v", err)
	}
	return s, dsID
}

// opened records the files review opens and what each held when opened.
func stubOpen(t *testing.T, fail bool) *[]string {
	t.Helper()
	var paths []string
	old := openFile
	openFile = func(path string) error {
		info, err := os.Stat(path)
		if err != nil || info.Mode().Perm() != 0o600 || info.Size() == 0 {
			t.Errorf("opened %s: %v, %v", path, info, err)
		}
		paths = append(paths, path)
		if fail {
			return errors.New("no opener")
		}
		return nil
	}
	t.Cleanup(func() { openFile = old })
	return &paths
}

func (s *liveServer) reviewStates(t *testing.T, dsID string) map[fixture.ReviewState]int {
	t.Helper()
	var list []fixture.Fixture
	s.runJSON(t, s.adminKey, &list, "fixtures", "list", dsID)
	out := map[fixture.ReviewState]int{}
	for _, f := range list {
		out[f.ReviewState]++
	}
	return out
}

func TestReviewWalk(t *testing.T) {
	s, dsID := reviewServer(t)
	opened := stubOpen(t, false)

	// Ingest names them after the batch: review-001.eml and on.
	// First fixture: an unknown answer is asked again, "o" opens it again,
	// then approve; the second is rejected, the third skipped.
	out, _, err := runCLIWith(t, "x\no\na\nreject\ns\n", "review", "inbox", "--server", s.url, "--api-key", s.adminKey)
	if err != nil {
		t.Fatalf("review: %v\n%s", err, out)
	}
	for _, want := range []string{"[1/3] review-001.eml", "[3/3] review-003.eml", "approved", "rejected", "Approved 1, rejected 1, skipped 1, left 0."} {
		if !strings.Contains(out, want) {
			t.Errorf("output misses %q:\n%s", want, out)
		}
	}
	if len(*opened) != 4 {
		t.Errorf("opened %d times, want 4 (the first twice)", len(*opened))
	}
	// Local copies are gone, and so is their directory.
	for _, p := range *opened {
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Errorf("%s left behind", p)
		}
	}
	if _, err := os.Stat(filepath.Dir((*opened)[0])); !os.IsNotExist(err) {
		t.Errorf("review directory left behind")
	}
	if got := s.reviewStates(t, dsID); got[fixture.ReviewApproved] != 1 || got[fixture.ReviewRejected] != 1 || got[fixture.ReviewUnreviewed] != 1 {
		t.Errorf("states = %v", got)
	}

	// The skipped one is still to do; quit leaves it, and so does running out
	// of input.
	for _, input := range []string{"q\n", ""} {
		out, _, err = runCLIWith(t, input, "review", dsID, "--server", s.url, "--api-key", s.adminKey)
		if err != nil || !strings.Contains(out, "[1/1] review-003.eml") || !strings.Contains(out, "left 1.") {
			t.Errorf("input %q: %v\n%s", input, err, out)
		}
	}
	// A last answer without a newline still counts.
	out, _, err = runCLIWith(t, "a", "review", dsID, "--server", s.url, "--api-key", s.adminKey)
	if err != nil || !strings.Contains(out, "Approved 1,") {
		t.Errorf("answer without newline: %v\n%s", err, out)
	}
	out, _, err = runCLIWith(t, "", "review", dsID, "--server", s.url, "--api-key", s.adminKey)
	if err != nil || !strings.Contains(out, "Nothing to review.") {
		t.Errorf("empty review = %q, %v", out, err)
	}
}

func TestReviewWithoutOpening(t *testing.T) {
	s, _ := reviewServer(t)
	opened := stubOpen(t, true)

	// --no-open prints each path; it never calls the opener.
	out, _, err := runCLIWith(t, "s\nq\n", "review", "inbox", "--no-open", "--query", "001", "--kind", "email", "--server", s.url, "--api-key", s.adminKey)
	if err != nil || len(*opened) != 0 || !strings.Contains(out, string(filepath.Separator)+"review-001.eml\n") || !strings.Contains(out, "[1/1]") {
		t.Errorf("--no-open: %v, opened %d\n%s", err, len(*opened), out)
	}
	// An opener that fails falls back to printing the path.
	out, errOut, err := runCLIWith(t, "q\n", "review", "inbox", "--server", s.url, "--api-key", s.adminKey)
	if err != nil || !strings.Contains(errOut, "could not open it") || !strings.Contains(out, "review-001.eml\n") {
		t.Errorf("failing opener: %v\n%s\n%s", err, out, errOut)
	}
}

func TestReviewNeedsAnAdminKey(t *testing.T) {
	s, dsID := reviewServer(t)
	stubOpen(t, false)
	member := s.member(t, "alice")
	if _, _, err := runCLIWith(t, "a\n", "review", dsID, "--server", s.url, "--api-key", member); err == nil {
		t.Error("a member reviewed quarantine")
	}
	if _, _, err := runCLIWith(t, "", "review", "nothing", "--server", s.url, "--api-key", s.adminKey); err == nil {
		t.Error("reviewing an unknown dataset succeeded")
	}
}

func TestDownloadAllFlagsUnreviewedData(t *testing.T) {
	s, _ := reviewServer(t)
	out, err := s.run(t, s.adminKey, "fixtures", "download-all", "inbox", "--dir", filepath.Join(t.TempDir(), "q"))
	if err != nil || !strings.Contains(out, "Downloaded 3 fixtures") || !strings.Contains(out, "3 are unreviewed real-derived data") {
		t.Errorf("download-all quarantine = %q, %v", out, err)
	}
}
