package cmd

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/martinsuchenak/exemplar/internal/anonymise"
	"github.com/martinsuchenak/exemplar/internal/anonymise/anonymisetest"
	"github.com/martinsuchenak/exemplar/internal/app"
)

const anonMD = "# Notes\n\nCall priya.shah@acmeplumbing.com.au on 0412 345 678.\n"

// hermetic keeps the developer's own anonymiser settings out of a test.
func hermetic(t *testing.T) {
	for _, v := range []string{"PRESIDIO_URL", "EXEMPLAR_ANON_KEY", "ANON_KEY_FILE", "ANON_PROFILES_DIR"} {
		t.Setenv(v, "")
	}
}

func TestAnonymiseLocally(t *testing.T) {
	hermetic(t)
	// stdin to stdout: the file alone on stdout, the summary and warning on stderr.
	out, errOut, err := runCLIWith(t, anonMD, "anonymise", "-", "--format", "md", "--local", "--patterns-only")
	if err != nil {
		t.Fatalf("%v\n%s", err, errOut)
	}
	if !strings.HasPrefix(out, "# Notes\n\nCall ") || strings.Contains(out, "acmeplumbing") || strings.Contains(out, "345 678") {
		t.Errorf("stdout = %q", out)
	}
	if !strings.Contains(errOut, "1 input.md -> document-") || !strings.HasSuffix(errOut, unreviewedWarning+"\n") {
		t.Errorf("stderr = %q", errOut)
	}

	dir := t.TempDir()
	in := filepath.Join(dir, "in")
	if err := os.Mkdir(in, 0o700); err != nil {
		t.Fatal(err)
	}
	for name, data := range map[string]string{
		"a.md": anonMD, "b.csv": "Name,Phone\nWendy Moss,0412 345 678\n", "c.xls": "xls",
	} {
		if err := os.WriteFile(filepath.Join(in, name), []byte(data), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	// One file to --output, never overwritten, owner-only.
	target := filepath.Join(dir, "anon.md")
	if _, errOut, err := runCLIWith(t, "", "anonymise", filepath.Join(in, "a.md"), "--output", target, "--local", "--patterns-only", "--quiet"); err != nil {
		t.Fatalf("%v\n%s", err, errOut)
	} else if errOut != unreviewedWarning+"\n" {
		t.Errorf("--quiet stderr = %q", errOut)
	}
	if info, err := os.Stat(target); err != nil || info.Mode().Perm() != 0o600 {
		t.Errorf("output file = %v, %v", info, err)
	}
	if _, _, err := runCLIWith(t, "", "anonymise", filepath.Join(in, "a.md"), "--output", target, "--local", "--patterns-only"); err == nil || !strings.Contains(err.Error(), "exists") {
		t.Errorf("overwrite err = %v", err)
	}

	// Several files need --out-dir; they go there under fake names, a failure
	// is listed, and the command fails.
	if _, _, err := runCLIWith(t, "", "anonymise", in, "--local", "--patterns-only"); err == nil || !strings.Contains(err.Error(), "3 files need --out-dir") {
		t.Errorf("no out-dir err = %v", err)
	}
	outDir := filepath.Join(dir, "out")
	_, errOut, err = runCLIWith(t, "", "anonymise", in, "--out-dir", outDir, "--local", "--patterns-only", "--report")
	if err == nil || !strings.Contains(err.Error(), "1 of 3 files failed") {
		t.Errorf("multi err = %v", err)
	}
	for _, want := range []string{"1 a.md -> document-", "2 b.csv -> document-", "3 c.xls failed: cannot anonymise .xls", "ORIGINAL personal data", `"0412 345 678" -> "`, unreviewedWarning} {
		if !strings.Contains(errOut, want) {
			t.Errorf("stderr misses %q:\n%s", want, errOut)
		}
	}
	written, _ := os.ReadDir(outDir)
	if len(written) != 2 {
		t.Fatalf("out-dir holds %d files", len(written))
	}
	for _, e := range written {
		data, _ := os.ReadFile(filepath.Join(outDir, e.Name()))
		if !strings.HasPrefix(e.Name(), "document-") || strings.Contains(string(data), "345 678") || strings.Contains(string(data), "Wendy") {
			t.Errorf("%s = %q", e.Name(), data)
		}
	}
	if _, _, err := runCLIWith(t, "", "anonymise", in, "--out-dir", outDir, "--local", "--patterns-only"); err == nil || !strings.Contains(err.Error(), "not empty") {
		t.Errorf("full out-dir err = %v", err)
	}
}

func TestAnonymiseArguments(t *testing.T) {
	hermetic(t)
	dir := t.TempDir()
	md := filepath.Join(dir, "a.md")
	if err := os.WriteFile(md, []byte(anonMD), 0o600); err != nil {
		t.Fatal(err)
	}
	for want, args := range map[string][]string{
		"needs --format or --name":       {"anonymise", "-", "--local", "--patterns-only"},
		`unknown format "xls"`:           {"anonymise", "-", "--format", "xls", "--local", "--patterns-only"},
		"are for stdin":                  {"anonymise", md, "--format", "md", "--local"},
		"not both":                       {"anonymise", md, "--output", "x", "--out-dir", "y", "--local"},
		"--patterns-only to run without": {"anonymise", md, "--local"},
		"Presidio URL or patterns only":  {"anonymise", md, "--local", "--patterns-only", "--presidio-url", "http://x"},
		"drop --local":                   {"anonymise", md, "--local", "--patterns-only", "--link-registry"},
		"--report needs --local":         {"anonymise", md, "--report"},
		"--patterns-only is for --local": {"anonymise", md, "--patterns-only", "--api-key", "exm_x"},
	} {
		if _, _, err := runCLIWith(t, anonMD, args...); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%v: err = %v, want %q", args, err, want)
		}
	}
}

func TestAnonymiseOnTheServer(t *testing.T) {
	hermetic(t)
	s := startServerWith(t, func(c *app.Config) {
		c.AnonDetector = anonymisetest.Detector{Names: []string{"Wendy"}}
	})
	member := s.member(t, "alice")

	// A DOCX's dropped parts are in the summary.
	_, errOut, err := runCLIWith(t, string(anonymisetest.DOCX("Wendy")), "anonymise", "-", "--format", "docx", "--output", filepath.Join(t.TempDir(), "a.docx"), "--server", s.url, "--api-key", member)
	if err != nil || !strings.Contains(errOut, "1 parts dropped (word/vbaProject.bin)") {
		t.Errorf("docx: %v\n%s", err, errOut)
	}

	// One file goes up raw and comes back as it went.
	out, errOut, err := runCLIWith(t, anonMD, "anonymise", "-", "--name", "notes.md", "--server", s.url, "--api-key", member)
	if err != nil {
		t.Fatalf("%v\n%s", err, errOut)
	}
	if !strings.HasPrefix(out, "# Notes\n\nCall ") || strings.Contains(out, "acmeplumbing") || !strings.Contains(errOut, "1 notes.md -> document-") {
		t.Errorf("stdout %q, stderr %q", out, errOut)
	}

	// Several go up as one batch and land in --out-dir.
	dir := t.TempDir()
	for name, data := range map[string]string{"a.md": "Wendy called\n", "b.json": `{"note": "Wendy again"}`} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(data), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	outDir := filepath.Join(t.TempDir(), "out")
	_, errOut, err = runCLIWith(t, "", "anonymise", dir, "--out-dir", outDir, "--server", s.url, "--api-key", member)
	if err != nil {
		t.Fatalf("%v\n%s", err, errOut)
	}
	written, _ := os.ReadDir(outDir)
	if len(written) != 2 {
		t.Fatalf("out-dir holds %d files:\n%s", len(written), errOut)
	}
	for _, e := range written {
		if data, _ := os.ReadFile(filepath.Join(outDir, e.Name())); strings.Contains(string(data), "Wendy") {
			t.Errorf("%s = %q", e.Name(), data)
		}
	}

	// A single file's error is the command's error.
	pdf := filepath.Join(dir, "x.xls")
	if err := os.WriteFile(pdf, []byte("%PDF"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := runCLIWith(t, "", "anonymise", pdf, "--server", s.url, "--api-key", member); err == nil || !strings.Contains(err.Error(), "cannot anonymise .xls") {
		t.Errorf("xls err = %v", err)
	}
	if _, _, err := runCLIWith(t, anonMD, "anonymise", "-", "--format", "md", "--link-registry", "--server", s.url, "--api-key", member); err == nil || !strings.Contains(err.Error(), "admin key") {
		t.Errorf("link as member err = %v", err)
	}
}

// --check lists on stderr what came through unchanged; --check-json writes
// it to a file or, beside --output or --out-dir, to stdout. Stdin can be
// checked too, and a file that failed says why.
func TestAnonymiseCheck(t *testing.T) {
	hermetic(t)
	dir := t.TempDir()
	note := "Wendy Moss met Tom Baker at Lumen Fabrication. Ref 7700012345.\n"
	in := filepath.Join(dir, "note.md")
	if err := os.WriteFile(in, []byte(note), 0o600); err != nil {
		t.Fatal(err)
	}
	_, errOut, err := runCLIWith(t, "", "anonymise", in, "--output", filepath.Join(dir, "out.md"), "--local", "--patterns-only",
		"--check", "--check-json", filepath.Join(dir, "check.json"))
	if err != nil {
		t.Fatalf("%v\n%s", err, errOut)
	}
	for _, want := range []string{"Unchanged: in the output exactly as in the original", "1 note.md -> document-", "number  7700012345", "name    Lumen Fabrication", unreviewedWarning} {
		if !strings.Contains(errOut, want) {
			t.Errorf("stderr misses %q:\n%s", want, errOut)
		}
	}
	data, err := os.ReadFile(filepath.Join(dir, "check.json"))
	if err != nil {
		t.Fatal(err)
	}
	var report struct {
		Files []struct {
			Input, Output string
			Unchanged     []anonymise.Kept
		}
	}
	if err := json.Unmarshal(data, &report); err != nil || len(report.Files) != 1 || report.Files[0].Input != "note.md" ||
		!strings.HasPrefix(report.Files[0].Output, "document-") || !slices.Contains(report.Files[0].Unchanged, anonymise.Kept{Kind: "name", Value: "Lumen Fabrication", Count: 1}) {
		t.Errorf("check.json = %s, %v", data, err)
	}

	// A bad --check-json path fails before anything is anonymised or written.
	for _, c := range []struct{ path, want string }{
		{filepath.Join(dir, "check.json"), "exists"},
		{filepath.Join(dir, "nowhere", "check.json"), "its directory does not exist"},
		{filepath.Join(dir, "early.md"), "name the same file"},
	} {
		_, _, err := runCLIWith(t, "", "anonymise", in, "--output", filepath.Join(dir, "early.md"), "--local", "--patterns-only", "--check-json", c.path)
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: err = %v", c.path, err)
		}
		if _, err := os.Stat(filepath.Join(dir, "early.md")); err == nil {
			t.Fatalf("%s: wrote the output before failing", c.path)
		}
	}

	// - means stdout, which must not also carry the file.
	if _, _, err := runCLIWith(t, "", "anonymise", in, "--local", "--patterns-only", "--check-json", "-"); err == nil || !strings.Contains(err.Error(), "needs --output or --out-dir") {
		t.Errorf("err = %v", err)
	}
	other := filepath.Join(dir, "bad.xls")
	if err := os.WriteFile(other, []byte("xls"), 0o600); err != nil {
		t.Fatal(err)
	}
	out, _, err := runCLIWith(t, "", "anonymise", in, other, "--out-dir", filepath.Join(dir, "outs"), "--local", "--patterns-only", "--quiet", "--check-json", "-")
	if err == nil {
		t.Error("a failed file did not fail the command")
	}
	if !strings.Contains(out, `"input": "bad.xls"`) || !strings.Contains(out, `"error": "cannot anonymise .xls`) || !strings.Contains(out, `"Lumen Fabrication"`) {
		t.Errorf("stdout = %s", out)
	}

	// Stdin is read once and kept for the check.
	out, errOut, err = runCLIWith(t, note, "anonymise", "-", "--format", "md", "--local", "--patterns-only", "--check")
	if err != nil || !strings.Contains(out, "Lumen Fabrication") || !strings.Contains(errOut, "name    Lumen Fabrication") {
		t.Errorf("stdin: out %q, stderr %q, %v", out, errOut, err)
	}
}
