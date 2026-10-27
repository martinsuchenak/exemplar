package anonymise

import (
	"context"
	"encoding/json"
	"html"
	"strings"
	"testing"
)

// docSecrets are in the documents below; none may survive. Wendy Moss, in
// the CSV and JSON ones, is found only through a column or key named for
// names (no detector knows her), the rest by the test detector and the
// patterns.
var docSecrets = []string{
	"Sarah Lane", "Sarah", "Priya", "Wendy Moss", "Wendy", "sarah.lane@acme-plumbing.com.au", "acme-plumbing",
	"0412 345 678", "invoices?id=991", "Acme Plumbing Pty Ltd",
}

var documents = map[string]string{
	"notes.txt": "Call from Sarah Lane (0412 345 678) about https://acme-plumbing.com.au/invoices?id=991.\n" +
		"Priya says Acme Plumbing Pty Ltd paid. sarah.lane@acme-plumbing.com.au\n",
	"README.md": "---\nauthor: Priya\n---\n# Notes for [Sarah Lane](mailto:sarah.lane@acme-plumbing.com.au)\n\n" +
		"![photo](https://acme-plumbing.com.au/invoices?id=991)\n\n```\ncall 0412 345 678 # Acme Plumbing Pty Ltd\n```\n",
	"people.csv": "Name,Email,Phone,Notes\r\n" +
		"Wendy Moss,sarah.lane@acme-plumbing.com.au,0412 345 678,\"Spoke to Priya, re https://acme-plumbing.com.au/invoices?id=991\"\r\n" +
		"\"Sarah Lane\",other@example.com,,Acme Plumbing Pty Ltd\r\n",
	"people.tsv": "Full name\tE-mail\tNotes\nWendy Moss\tsarah.lane@acme-plumbing.com.au\tPriya: 0412 345 678\n" +
		"Sarah Lane\t\thttps://acme-plumbing.com.au/invoices?id=991 Acme Plumbing Pty Ltd\n",
	"contact.json": `{"customer": {"name": "Wendy Moss", "email": "sarah.lane@acme-plumbing.com.au",` +
		` "notes": "Sarah Lane, 0412 345 678, Acme Plumbing Pty Ltd", "count": 3},` +
		` "tags": ["Priya", "https://acme-plumbing.com.au/invoices?id=991"]}`,
	"events.jsonl": `{"who": "Sarah Lane", "phone": "0412 345 678"}` + "\n" +
		`{"name": "Wendy Moss", "with": ["Priya", "sarah.lane@acme-plumbing.com.au"], "co": "Acme Plumbing Pty Ltd"}` + "\n\n" +
		`{"link": "https://acme-plumbing.com.au/invoices?id=991"}` + "\n",
	"page.html": "<html><head><title>Sarah Lane</title><meta name=\"author\" content=\"Priya\"></head>\n" +
		"<body><!-- written for Priya --><p>Call <a href=\"mailto:sarah.lane@acme-plumbing.com.au\">us</a> on 0412 345 678.</p>\n" +
		"<img alt='Acme Plumbing Pty Ltd' src=\"https://acme-plumbing.com.au/invoices?id=991\"></body></html>\n",
	"mail.html.twig": "{# reviewed by Sarah Lane #}<p>Dear {{ customer.name|title }}, from {{ 'Priya'|upper }}</p>\n" +
		"{% set signer = \"Sarah Lane\" %}{% if phone %}<p>0412 345 678</p>{% endif %}\n" +
		"<a href=\"mailto:sarah.lane@acme-plumbing.com.au\">{{ \"Acme Plumbing Pty Ltd, ref #{invoice.id}\" }}</a>\n" +
		"{% verbatim %}{{ https://acme-plumbing.com.au/invoices?id=991 }}{% endverbatim %}\n",
	"letter.txt.j2": "{#- Priya asked for this -#}\nDear {{ name }},\n{{ _('Regards, Priya') }}\n" +
		"{% raw %}{{ Sarah Lane }}{% endraw %} 0412 345 678 sarah.lane@acme-plumbing.com.au\n" +
		"https://acme-plumbing.com.au/invoices?id=991 Acme Plumbing Pty Ltd\n",
}

func anonymiseDoc(t *testing.T, e *Engine, name, data string) *Output {
	t.Helper()
	f := File{Name: name, Data: []byte(data)}
	if errs := e.Prepare(ctx, []File{f}); errs[0] != nil {
		t.Fatalf("%s: preparing: %v", name, errs[0])
	}
	out, err := e.Anonymise(ctx, f)
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	return out
}

func TestDocumentsLoseEverySecret(t *testing.T) {
	for name, data := range documents {
		t.Run(name, func(t *testing.T) {
			out := anonymiseDoc(t, newEngine(t, newMemRegistry()), name, data)
			text := strings.ToLower(string(out.Data))
			for _, s := range docSecrets {
				if strings.Contains(text, strings.ToLower(s)) {
					t.Errorf("still contains %q:\n%s", s, out.Data)
				}
			}
			if want, _ := FormatOf(name); out.Format != want || !strings.HasPrefix(out.Name, "document-") || !strings.HasSuffix(out.Name, Extension(name)) {
				t.Errorf("format %q, name %q", out.Format, out.Name)
			}
			if len(out.Replacements) == 0 || out.Attachments != 0 || out.People != nil {
				t.Errorf("replacements %d, attachments %d, people %v", len(out.Replacements), out.Attachments, out.People)
			}
		})
	}
}

// What is not text stays exactly as sent.
func TestDocumentsKeepTheirStructure(t *testing.T) {
	e := newEngine(t, newMemRegistry())
	keeps := map[string][]string{
		"people.csv":   {"Name,Email,Phone,Notes\r\n", "@example.com,,", "\r\n\""},
		"contact.json": {`{"customer": {"name": "`, `", "count": 3},`, ` "tags": ["`},
		"events.jsonl": {`{"who": "`, `"}` + "\n" + `{"name": "`, "\n\n" + `{"link": "`},
		"page.html":    {"<html><head><title>", `<meta name="author" content="`, "<!-- written for ", "<img alt='"},
		"mail.html.twig": {
			"{# reviewed by ", "<p>Dear {{ customer.name|title }}, from {{ '", "'|upper }}</p>",
			`{% set signer = "`, "{% if phone %}<p>", "ref #{invoice.id}\" }}</a>", "{% verbatim %}{{ ", "}}{% endverbatim %}",
		},
		"letter.txt.j2": {"{#- ", " -#}\nDear {{ name }},\n{{ _('Regards, ", "') }}\n{% raw %}{{ ", " }}{% endraw %} "},
	}
	for name, parts := range keeps {
		out := string(anonymiseDoc(t, e, name, documents[name]).Data)
		for _, p := range parts {
			if !strings.Contains(out, p) {
				t.Errorf("%s lost %q:\n%s", name, p, out)
			}
		}
	}

	// A JSON value that does not change keeps its escaping; one that does
	// is written as JSON again.
	out := string(anonymiseDoc(t, e, "a.json", `{ "n" : 1.50, "cafe":"caf\u00e9", "who":"Sarah Lane \u2013 hi" }`).Data)
	var v map[string]any
	if err := json.Unmarshal([]byte(out), &v); err != nil || !strings.HasPrefix(out, `{ "n" : 1.50, "cafe":"caf\u00e9", "who":"`) || !strings.HasSuffix(out, ` – hi" }`) {
		t.Errorf("json = %s (%v)", out, err)
	}

	// A CSV cell that changes keeps its quotes, or gains them when it must.
	d := &csvDoc{comma: ','}
	if d.quote("a, b", false) != `"a, b"` || d.quote(`say "hi"`, false) != `"say ""hi"""` || d.quote("ab", true) != `"ab"` || d.quote("ab", false) != "ab" {
		t.Error("csv quoting")
	}
	// A fake brought into a template string cannot end it early.
	if got := escapeQuote(`O'Brien \'x\' "y"`, '\''); got != `O\'Brien \'x\' "y"` {
		t.Errorf("escapeQuote = %s", got)
	}
}

func TestDocumentErrors(t *testing.T) {
	for name, c := range map[string]struct{ data, want string }{
		"a.json":       {`{"a": 1,}`, "not valid JSON"},
		"b.jsonl":      {"{\"a\": 1}\n{oops}\n", "line 2 is not valid JSON"},
		"c.csv":        {"a,b\n\"never closed,x\n", "row 2: field 1: a quoted field is never closed"},
		"d.csv":        {"a,b\n\"quoted\"after,x\n", "row 2: field 1: text after a closing quote"},
		"e.twig":       {"line one\n{{ never closed", "line 2: a tag is never closed"},
		"f.twig":       {"{# never closed", "line 1: a comment is never closed"},
		"g.twig":       {"{{ 'never closed }}", "line 1: a string in a tag is never closed"},
		"h.j2":         {"{% raw %} no end", "{% raw %} is never ended"},
		"i.txt":        {"caf\xe9", "not UTF-8 text"},
		"j.md":         {"nul\x00byte", "NUL bytes"},
		"k.txt":        {"\xff\xfeodd", "odd number of bytes"},
		"l.html":       {"\xfe\xff\xd8\x00", "unpaired surrogate"},
		"m.unknown.gz": {"x", "cannot anonymise .gz"},
	} {
		// Prepare and Anonymise fail alike, so a bad file is reported once
		// and never half-read.
		f := File{Name: name, Data: []byte(c.data)}
		e := newEngine(t, newMemRegistry())
		perr := e.Prepare(ctx, []File{f})[0]
		_, err := e.Anonymise(ctx, f)
		if err == nil || perr == nil || err.Error() != perr.Error() || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: prepare %v, anonymise %v, want %q", name, perr, err, c.want)
		}
	}
}

func TestTextEncodingsSurvive(t *testing.T) {
	text := "Notes: Sarah Lane – café\n"
	for name, enc := range map[string]func(string) []byte{
		"utf-8 with BOM": func(s string) []byte { return append([]byte{0xef, 0xbb, 0xbf}, s...) },
		"utf-16le":       func(s string) []byte { return encodeUTF16(s, false) },
		"utf-16be":       func(s string) []byte { return encodeUTF16(s, true) },
	} {
		out := anonymiseDoc(t, newEngine(t, newMemRegistry()), "a.txt", string(enc(text)))
		got, back, err := decodeText(out.Data)
		if err != nil || strings.Contains(got, "Sarah") || !strings.HasPrefix(got, "Notes: ") || !strings.HasSuffix(got, " – café\n") {
			t.Errorf("%s: %q (%v)", name, got, err)
		}
		if string(back(got)) != string(out.Data) || string(out.Data[:2]) != string(enc(text)[:2]) {
			t.Errorf("%s: encoding not kept", name)
		}
	}
	if _, err := decodeUTF16([]byte{0x00, 0xd8}, false); err == nil {
		t.Error("an unpaired surrogate was accepted")
	}
}

// contextDetector finds a name only after "Customer: ", the way NER needs
// context, plus the names the engine already knows.
type contextDetector struct{ calls int }

func (d *contextDetector) Detect(_ context.Context, text string, names []string, _ float64) ([]Span, error) {
	d.calls++
	var spans []Span
	if i := strings.Index(text, "Customer: "); i >= 0 {
		start := i + len("Customer: ")
		end := start + strings.IndexAny(text[start:]+"\n", "\n.")
		spans = append(spans, Span{Start: start, End: end, Entity: EntityPerson})
	}
	for _, n := range names {
		for i := 0; ; {
			j := strings.Index(text[i:], n)
			if j < 0 {
				break
			}
			spans = append(spans, Span{Start: i + j, End: i + j + len(n), Entity: EntityPerson})
			i += j + len(n)
		}
	}
	return spans, nil
}

func TestNamesCarryAcrossFiles(t *testing.T) {
	a := File{Name: "a.txt", Data: []byte("Customer: Wendy Moss.\n")}
	b := File{Name: "b.md", Data: []byte("# Wendy Moss called again\n")}
	run := func(prepare ...File) string {
		e, err := New(testKey(t), defaultProfile(t), &contextDetector{}, newMemRegistry())
		if err != nil {
			t.Fatal(err)
		}
		for _, err := range e.Prepare(ctx, prepare) {
			if err != nil {
				t.Fatal(err)
			}
		}
		out, err := e.Anonymise(ctx, b)
		if err != nil {
			t.Fatal(err)
		}
		return string(out.Data)
	}
	if out := run(b); !strings.Contains(out, "Wendy Moss") {
		t.Fatalf("the detector found Wendy without context: %s", out)
	}
	if out := run(a, b); strings.Contains(out, "Wendy") || strings.Contains(out, "Moss") {
		t.Errorf("a name found in one file was missed in the next: %s", out)
	}

	// A person from an email's headers gets the same fake in a document.
	mail := eml("From: Wendy Moss <wendy.moss@acme.com.au>\r\nTo: bo@other.com.au\r\nSubject: hi\r\n\r\nhello\r\n")
	e, err := New(testKey(t), defaultProfile(t), &contextDetector{}, newMemRegistry())
	if err != nil {
		t.Fatal(err)
	}
	e.Prepare(ctx, []File{mail, b})
	m, err := e.Anonymise(ctx, mail)
	if err != nil {
		t.Fatal(err)
	}
	doc, err := e.Anonymise(ctx, b)
	if err != nil {
		t.Fatal(err)
	}
	fake := m.Replacements["Wendy Moss"]
	if fake == "" || !strings.Contains(string(doc.Data), fake) || !strings.Contains(string(m.Data), fake) {
		t.Errorf("email fake %q; document %q", fake, doc.Data)
	}
}

func TestDetectionIsChunked(t *testing.T) {
	var b strings.Builder
	b.WriteString("Name,Notes\n")
	for i := 0; i < 3000; i++ {
		b.WriteString("Wendy Moss,Customer: Ann Lee\n")
	}
	d := &contextDetector{}
	e, err := New(testKey(t), defaultProfile(t), d, newMemRegistry())
	if err != nil {
		t.Fatal(err)
	}
	out, err := e.Anonymise(ctx, File{Name: "big.csv", Data: []byte(b.String())})
	if err != nil {
		t.Fatal(err)
	}
	if d.calls > 5 || strings.Contains(string(out.Data), "Wendy") {
		t.Errorf("%d detector calls for 3000 rows; Wendy kept: %v", d.calls, strings.Contains(string(out.Data), "Wendy"))
	}
}

func TestFieldHints(t *testing.T) {
	for label, want := range map[string]string{
		"E-mail": EntityEmail, "email_address": EntityEmail, "Full Name": EntityPerson, "first_name": EntityPerson,
		"Mobile": EntityPhone, "Organisation": EntityCompany, "notes": "", "": "",
	} {
		if got := fieldHint(label); got != want {
			t.Errorf("fieldHint(%q) = %q, want %q", label, got, want)
		}
	}
}

// brokenDoc rewrites into something with another structure.
type brokenDoc struct{ text string }

func (d brokenDoc) texts() []docText                  { return []docText{{value: d.text}} }
func (d brokenDoc) rewrite(*replacer) (string, error) { return d.text + "!", nil }
func (d brokenDoc) shape() string                     { return d.text }
func parseBroken(_, text string) (doc, error) {
	if strings.HasSuffix(text, "!!") {
		return nil, errBroken
	}
	return brokenDoc{text}, nil
}

var errBroken = &json.SyntaxError{}

func TestOutputMustKeepItsStructure(t *testing.T) {
	e := newEngine(t, newMemRegistry())
	h := docHandler{parse: parseBroken}
	if _, err := h.anonymise(ctx, e, File{Name: "x", Data: []byte("a")}); err == nil || !strings.Contains(err.Error(), "changed the file's structure") {
		t.Errorf("err = %v", err)
	}
	if _, err := h.anonymise(ctx, e, File{Name: "x", Data: []byte("a!")}); err == nil || !strings.Contains(err.Error(), "does not parse") {
		t.Errorf("err = %v", err)
	}
}

// The same document with the same key gives the same bytes every time:
// fakes are made in a fixed order, not the order of a map.
func TestDocumentsAreDeterministic(t *testing.T) {
	for name, data := range documents {
		first := string(anonymiseDoc(t, newEngine(t, newMemRegistry()), name, data).Data)
		for i := 0; i < 20; i++ {
			if again := string(anonymiseDoc(t, newEngine(t, newMemRegistry()), name, data).Data); again != first {
				t.Fatalf("%s: run %d differs:\n%s\n---\n%s", name, i+2, first, again)
			}
		}
	}
}

// An address in one file and the name in another get matching fakes,
// whichever file is rewritten first, because Prepare maps the names first.
func TestNamesAndAddressesMatchAcrossFiles(t *testing.T) {
	addr := File{Name: "a.txt", Data: []byte("Write to sarah.lane@acme-plumbing.com.au\n")}
	name := File{Name: "b.csv", Data: []byte("Notes\nSarah Lane called\n")}
	e := newEngine(t, newMemRegistry())
	for _, err := range e.Prepare(ctx, []File{addr, name}) {
		if err != nil {
			t.Fatal(err)
		}
	}
	a, err := e.Anonymise(ctx, addr)
	if err != nil {
		t.Fatal(err)
	}
	b, err := e.Anonymise(ctx, name)
	if err != nil {
		t.Fatal(err)
	}
	fake := strings.Fields(b.Replacements["Sarah Lane"])
	if len(fake) != 2 {
		t.Fatalf("Sarah Lane became %q", b.Replacements["Sarah Lane"])
	}
	if local := strings.ToLower(fake[0] + "." + fake[1] + "@"); !strings.Contains(string(a.Data), local) {
		t.Errorf("address became %q, not %s… after the name %v", a.Data, local, fake)
	}
}

// A name written with character references is replaced like any other, in
// a page, an HTML template and an email's HTML body; other references stay
// as written.
func TestHTMLCharacterReferences(t *testing.T) {
	newE := func() *Engine {
		e, err := New(testKey(t), defaultProfile(t), wordDetector{names: []string{"José García", "Sarah Lane"}}, newMemRegistry())
		if err != nil {
			t.Fatal(err)
		}
		return e
	}
	page := `<p>Jos&eacute; Garc&iacute;a and Sarah&#32;Lane, <a href="mailto:sarah.lane&#64;acme-plumbing.com.au">mail</a> &amp; more</p>` +
		`<img alt="Jos&#233; Garc&#xED;a">`
	for name, data := range map[string]string{"page.html": page, "page.html.twig": "{{ x }}" + page} {
		out := string(anonymiseDoc(t, newE(), name, data).Data)
		decoded := html.UnescapeString(out)
		for _, s := range []string{"José", "García", "Sarah", "Lane", "acme-plumbing"} {
			if strings.Contains(decoded, s) {
				t.Errorf("%s still holds %q:\n%s", name, s, out)
			}
		}
		if !strings.Contains(out, "</a> &amp; more</p><img alt=\"") {
			t.Errorf("%s lost what was around the names:\n%s", name, out)
		}
	}

	mail := "From: Tom Baker <tom@harbour-freight.com.au>\r\nTo: Ann Lee <ann@harbour-freight.com.au>\r\nSubject: Hi\r\n" +
		"Message-ID: <refs-1@harbour-freight.com.au>\r\nMIME-Version: 1.0\r\nContent-Type: text/html; charset=utf-8\r\n\r\n" +
		"<p>Jos&eacute; Garc&iacute;a called &mdash; ring back</p>\r\n"
	out := run(t, newE(), mail)[0]
	body := decodedText(t, out.Data)
	if strings.Contains(html.UnescapeString(body), "José") || !strings.Contains(body, "called &mdash; ring back") {
		t.Errorf("html body = %s", body)
	}

	// Positions in the decoded text map back to whole references.
	dec, starts, ends := decodeRefs("a&eacute;b&bogus;c")
	if dec != "aéb&bogus;c" || starts[1] != 1 || starts[2] != 1 || ends[3] != 9 || starts[3] != 9 || starts[len(dec)] != 18 {
		t.Errorf("decodeRefs = %q %v %v", dec, starts, ends)
	}
}
