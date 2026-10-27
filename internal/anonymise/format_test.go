package anonymise

import (
	"reflect"
	"regexp"
	"strings"
	"testing"

	"github.com/martinsuchenak/exemplar/internal/mimetree"
)

// supported is the error's list of extensions, in format order.
const supported = "the supported files are .eml, .txt, .text, .log, .md, .markdown, .csv, .tsv, .json, .jsonl, .ndjson, .html, .htm, .twig, .j2, .jinja, .jinja2"

func TestFormatOf(t *testing.T) {
	for name, want := range map[string]string{
		"a.eml":                 "eml",
		"Jane Doe invoice.EML":  "eml",
		"dir/thread.2025.eml":   "eml",
		"notes.TXT":             "txt",
		"app.log":               "txt",
		"README.md":             "md",
		"guide.markdown":        "md",
		"people.csv":            "csv",
		"people.tsv":            "tsv",
		"contact.json":          "json",
		"events.jsonl":          "jsonl",
		"events.ndjson":         "jsonl",
		"page.HTM":              "html",
		"mail.html.twig":        "twig",
		"letter.j2":             "jinja",
		"letter.jinja2":         "jinja",
		"outlook.msg":           "MSG files are not supported yet; convert to .eml first",
		"report.pdf":            "cannot anonymise .pdf: " + supported,
		"README":                "cannot anonymise a file without an extension: " + supported,
		"":                      "cannot anonymise a file without an extension: " + supported,
		"archive.eml.zip":       "cannot anonymise .zip: " + supported,
		"trailing-dot.":         "cannot anonymise a file without an extension: " + supported,
		"looks like eml but no": "cannot anonymise a file without an extension: " + supported,
	} {
		f, err := FormatOf(name)
		got := string(f)
		if err != nil {
			got = err.Error()
		}
		if got != want {
			t.Errorf("FormatOf(%q) = %q, want %q", name, got, want)
		}
	}
	want := []Format{FormatEmail, FormatText, FormatMarkdown, FormatCSV, FormatTSV, FormatJSON, FormatJSONL, FormatHTML, FormatTwig, FormatJinja}
	if got := Formats(); !reflect.DeepEqual(got, want) {
		t.Errorf("Formats() = %v", got)
	}
	// No extension selects two formats.
	seen := map[string]Format{}
	for _, f := range formats {
		for _, x := range f.exts {
			if other, ok := seen[x]; ok {
				t.Errorf("%s selects both %s and %s", x, other, f.format)
			}
			seen[x] = f.format
		}
	}
}

func TestExtension(t *testing.T) {
	for name, want := range map[string]string{
		"a.EML": ".eml", "page.html.twig": ".html.twig", "Mail.HTM.Twig": ".htm.twig", "letter.txt.j2": ".txt.j2",
		"plain.twig": ".twig", "odd.name.twig": ".twig", "notes.md": ".md", "data.json.gz": ".gz", "README": "",
	} {
		if got := Extension(name); got != want {
			t.Errorf("Extension(%q) = %q, want %q", name, got, want)
		}
	}
}

var fakeEmailName = regexp.MustCompile(`^email-[0-9a-f]{6}\.eml$`)

func TestAnonymiseNamesTheFile(t *testing.T) {
	e := newEngine(t, newMemRegistry())
	files := []File{
		{Name: "Sarah Lane invoice.EML", Data: []byte(original)},
		{Name: "reply from Tom.eml", Data: []byte(reply)},
	}
	if errs := e.Prepare(ctx, files); errs[0] != nil || errs[1] != nil {
		t.Fatal(errs)
	}
	var names []string
	for _, f := range files {
		out, err := e.Anonymise(ctx, f)
		if err != nil {
			t.Fatal(err)
		}
		if out.Format != FormatEmail || !fakeEmailName.MatchString(out.Name) {
			t.Errorf("%s: format %q, name %q", f.Name, out.Format, out.Name)
		}
		for _, word := range []string{"Sarah", "Lane", "invoice", "Tom", "reply"} {
			if strings.Contains(out.Name, word) {
				t.Errorf("%s: fake name %q keeps %q", f.Name, out.Name, word)
			}
		}
		names = append(names, out.Name)
	}
	if names[0] == names[1] {
		t.Errorf("two files share the fake name %s", names[0])
	}
	// The same original name always gets the same fake, from any engine
	// with the key, and the content does not change it.
	again, err := newEngine(t, newMemRegistry()).Anonymise(ctx, File{Name: files[0].Name, Data: []byte(reply)})
	if err != nil {
		t.Fatal(err)
	}
	if again.Name != names[0] {
		t.Errorf("same name gave %s, then %s", names[0], again.Name)
	}
}

// A file named like an attachment gets the same hash as that attachment, so
// a reference from one file to another still matches after anonymising.
func TestFileNamesMatchAttachmentNames(t *testing.T) {
	e := newEngine(t, newMemRegistry())
	out := run(t, e, forwarded)[0]
	root, err := mimetree.Parse(out.Data)
	if err != nil {
		t.Fatal(err)
	}
	var attached string
	root.Walk(func(p *mimetree.Part) {
		if n := filename(p); strings.HasPrefix(n, "attachment-") {
			attached = n
		}
	})
	want := "attachment-" + strings.TrimPrefix(e.fakeFileName("email", "notes for Ann.txt"), "email-")
	if attached == "" || attached != want {
		t.Errorf("attachment named %q, a file of the same name %q", attached, want)
	}
}

func TestUnsupportedFilesFailAlone(t *testing.T) {
	e := newEngine(t, newMemRegistry())
	files := []File{
		{Name: "notes.pdf", Data: []byte("Sarah Lane called")},
		eml(original),
		{Name: "outlook.msg", Data: []byte("binary")},
	}
	errs := e.Prepare(ctx, files)
	if len(errs) != 3 || errs[1] != nil ||
		errs[0] == nil || !strings.Contains(errs[0].Error(), "cannot anonymise .pdf") ||
		errs[2] == nil || !strings.Contains(errs[2].Error(), "MSG") {
		t.Fatalf("errs = %v", errs)
	}
	if out, err := e.Anonymise(ctx, files[0]); err == nil || out != nil {
		t.Errorf("unsupported file anonymised: %v", err)
	}
	// The email was still prepared: the reply's body names Sarah, known
	// only from the first email's From.
	out, err := e.Anonymise(ctx, eml(reply))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(decodedText(t, out.Data), "Sarah") {
		t.Error("the email was not prepared past the unsupported files")
	}
}
