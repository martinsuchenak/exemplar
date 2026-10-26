package anonymise

import (
	"bytes"
	"context"
	"encoding/base64"
	"net/mail"
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/martinsuchenak/exemplar/internal/mimetree"
	"github.com/martinsuchenak/exemplar/internal/synth"
)

var ctx = context.Background()

// memRegistry is an in-memory Registry.
type memRegistry struct {
	tokens map[string]string
	people map[string]synth.Person
	n      int
}

func newMemRegistry() *memRegistry {
	return &memRegistry{tokens: map[string]string{}, people: map[string]synth.Person{}}
}

func (m *memRegistry) Token(_ context.Context, hash, fake string) (string, error) {
	if v, ok := m.tokens[hash]; ok {
		return v, nil
	}
	m.tokens[hash] = fake
	return fake, nil
}

func (m *memRegistry) LookupToken(_ context.Context, hash string) (string, bool, error) {
	v, ok := m.tokens[hash]
	return v, ok, nil
}

func (m *memRegistry) Person(_ context.Context, hashes []string, p synth.Person) (synth.Person, error) {
	for _, h := range hashes {
		if found, ok := m.people[h]; ok {
			for _, h2 := range hashes {
				m.people[h2] = found
			}
			return found, nil
		}
	}
	m.n++
	p.ID = "person-" + string(rune('a'+m.n))
	for _, h := range hashes {
		m.people[h] = p
	}
	return p, nil
}

// wordDetector finds the given names as PERSON, like NER would.
type wordDetector struct{ names []string }

func (d wordDetector) Detect(_ context.Context, text string, extra []string, _ float64) ([]Span, error) {
	var spans []Span
	for _, n := range append(append([]string{}, d.names...), extra...) {
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

func testKey(t *testing.T) Key {
	t.Helper()
	k, err := NewKey([]byte("0123456789abcdef-test-key"))
	if err != nil {
		t.Fatal(err)
	}
	return k
}

func defaultProfile(t *testing.T) *Profile {
	t.Helper()
	ps, err := LoadProfiles("")
	if err != nil {
		t.Fatal(err)
	}
	return ps["default"]
}

var pdfB64 = base64.StdEncoding.EncodeToString([]byte("%PDF-1.4 real invoice for Acme Plumbing"))
var pngB64 = base64.StdEncoding.EncodeToString([]byte("\x89PNG\r\n\x1a\nreal logo bytes"))

// original is an Outlook-style message with the shapes real mail has.
var original = strings.ReplaceAll(`Received: from mail.acme-plumbing.com.au (203.0.113.7) by mx.example.net
DKIM-Signature: v=1; a=rsa-sha256; d=acme-plumbing.com.au; s=sel; b=abc
X-Originating-IP: 203.0.113.7
From: "Lane, Sarah" <sarah.lane@acme-plumbing.com.au>
To: Tom Baker <tom.baker@harbour-freight.com.au>
Cc: accounts@acme-plumbing.com.au, Priya Shah <priya.shah@gmail.com>
Subject: Invoice for Acme Plumbing - call 0412 345 678
Date: Mon, 3 Feb 2025 09:15:00 +1100
Message-ID: <CAF123abc@mail.acme-plumbing.com.au>
MIME-Version: 1.0
Content-Type: multipart/mixed; boundary="MIX"

--MIX
Content-Type: multipart/related; boundary="REL"

--REL
Content-Type: multipart/alternative; boundary="ALT"

--ALT
Content-Type: text/plain; charset=utf-8
Content-Transfer-Encoding: 7bit

Hi Tom,

Please find the invoice attached. Priya will follow up.
Pay $1,234.56 to ABN 51 824 753 556 by Friday.

Kind regards,
Sarah Lane
Acme Plumbing Pty Ltd | 12 Smith Street Fitzroy | 0412 345 678
https://www.acme-plumbing.com.au/invoices?id=991
--ALT
Content-Type: text/html; charset=utf-8

<html><body><p>Hi Tom,</p><p>Please find the invoice attached. Priya will follow up.</p>
<p>Kind regards,<br>Sarah Lane<br><a href="mailto:sarah.lane@acme-plumbing.com.au">sarah.lane@acme-plumbing.com.au</a></p>
<img src="cid:logo123@acme-plumbing.com.au" alt="Acme Plumbing"></body></html>
--ALT--
--REL
Content-Type: image/png; name="acme-logo.png"
Content-Disposition: inline; filename="acme-logo.png"
Content-ID: <logo123@acme-plumbing.com.au>
Content-Transfer-Encoding: base64

`+pngB64+`
--REL--
--MIX
Content-Type: application/pdf; name="Invoice-Acme-991.pdf"
Content-Disposition: attachment; filename="Invoice-Acme-991.pdf"
Content-Transfer-Encoding: base64

`+pdfB64+`
--MIX--
`, "\n", "\r\n")

// reply mentions Sarah only in its body and quotes the original's headers.
var reply = strings.ReplaceAll(`From: Tom Baker <tom.baker@harbour-freight.com.au>
To: noreply@acme-plumbing.com.au
Subject: RE: Invoice for Acme Plumbing
Date: Mon, 3 Feb 2025 10:00:00 +1100
Message-ID: <reply-777@harbour-freight.com.au>
In-Reply-To: <CAF123abc@mail.acme-plumbing.com.au>
References: <CAF123abc@mail.acme-plumbing.com.au>
MIME-Version: 1.0
Content-Type: text/plain; charset=windows-1252
Content-Transfer-Encoding: 8bit

Thanks Sarah, paid. Caf`+"\xe9"+` receipt attached `+"\x96"+` Tom
`, "\n", "\r\n")

var secrets = []string{
	"Sarah", "Lane", "Tom", "Baker", "Priya", "Shah", "acme-plumbing", "harbour-freight", "Acme Plumbing",
	"0412 345 678", "51 824 753 556", "1,234.56", "12 Smith Street", "203.0.113.7", "CAF123abc",
	"logo123", "Invoice-Acme-991", "acme-logo", "real invoice", "real logo", "invoices?id=991",
}

func run(t *testing.T, e *Engine, emails ...string) []*Output {
	t.Helper()
	raw := make([][]byte, len(emails))
	for i, m := range emails {
		raw[i] = []byte(m)
	}
	if err := e.Prepare(ctx, raw); err != nil {
		t.Fatal(err)
	}
	outs := make([]*Output, len(emails))
	for i, m := range raw {
		out, err := e.Anonymise(ctx, m)
		if err != nil {
			t.Fatalf("email %d: %v", i, err)
		}
		outs[i] = out
	}
	return outs
}

func newEngine(t *testing.T, reg Registry) *Engine {
	t.Helper()
	e, err := New(testKey(t), defaultProfile(t), wordDetector{names: []string{"Priya", "Sarah Lane"}}, reg)
	if err != nil {
		t.Fatal(err)
	}
	return e
}

// decodedText returns every decoded leaf, so secrets hidden by an encoding are found.
func decodedText(t *testing.T, data []byte) string {
	t.Helper()
	root, err := mimetree.Parse(data)
	if err != nil {
		t.Fatal(err)
	}
	var b strings.Builder
	for _, f := range root.Header.Fields {
		b.WriteString(f.Name + ": " + mimetree.DecodeHeader(f.Value) + "\n")
	}
	root.Walk(func(p *mimetree.Part) {
		for _, f := range p.Header.Fields {
			b.WriteString(f.Value + "\n")
		}
		if !p.IsMultipart() {
			if s, err := p.Text(); err == nil {
				b.WriteString(s)
			}
		}
	})
	return b.String()
}

func TestNoSecretSurvives(t *testing.T) {
	outs := run(t, newEngine(t, newMemRegistry()), original, reply)
	for i, out := range outs {
		text := decodedText(t, out.Data) + string(out.Data)
		for _, s := range secrets {
			if strings.Contains(strings.ToLower(text), strings.ToLower(s)) {
				t.Errorf("email %d still contains %q", i, s)
			}
		}
	}
}

func TestStructureAndHeaders(t *testing.T) {
	outs := run(t, newEngine(t, newMemRegistry()), original, reply)
	a, b := outs[0], outs[1]

	before, _ := mimetree.Parse([]byte(original))
	after, _ := mimetree.Parse(a.Data)
	types := func(p *mimetree.Part) string {
		var ts []string
		p.Walk(func(q *mimetree.Part) { mt, _ := q.MediaType(); ts = append(ts, mt+"/"+q.Encoding()) })
		return strings.Join(ts, ",")
	}
	if types(before) != types(after) {
		t.Errorf("structure changed:\n%s\n%s", types(before), types(after))
	}
	for _, h := range []string{"Received", "DKIM-Signature", "X-Originating-IP"} {
		if after.Header.Get(h) != "" {
			t.Errorf("%s not dropped", h)
		}
	}
	if len(a.DroppedHeaders) != 3 || a.Attachments != 2 {
		t.Errorf("dropped %v, attachments %d", a.DroppedHeaders, a.Attachments)
	}

	msgA, _ := mail.ReadMessage(bytes.NewReader(a.Data))
	msgB, _ := mail.ReadMessage(bytes.NewReader(b.Data))
	if msgB.Header.Get("In-Reply-To") != msgA.Header.Get("Message-ID") || msgB.Header.Get("References") != msgA.Header.Get("Message-ID") {
		t.Errorf("thread broken: %q vs %q", msgB.Header.Get("In-Reply-To"), msgA.Header.Get("Message-ID"))
	}
	from, _ := mail.ParseAddress(msgA.Header.Get("From"))
	if !strings.Contains(from.Name, ", ") || !strings.HasSuffix(from.Address, ".example") {
		t.Errorf("From = %q (want 'Last, First' kept and a .example address)", msgA.Header.Get("From"))
	}
	cc, _ := mail.ParseAddressList(msgA.Header.Get("Cc"))
	if !strings.HasPrefix(cc[0].Address, "accounts@") || !strings.HasSuffix(cc[1].Address, "@gmail.com") {
		t.Errorf("Cc = %v (system local part and kept domain should stay)", cc)
	}
	to, _ := mail.ParseAddress(msgB.Header.Get("To"))
	if !strings.HasPrefix(to.Address, "noreply@") || strings.HasSuffix(to.Address, "acme-plumbing.com.au") {
		t.Errorf("noreply To = %q", to.Address)
	}
	if len(a.People) != 3 {
		t.Errorf("people links = %+v (want from, to and the gmail cc)", a.People)
	}
}

func TestConsistentAcrossEmails(t *testing.T) {
	outs := run(t, newEngine(t, newMemRegistry()), original, reply)
	msgA, _ := mail.ReadMessage(bytes.NewReader(outs[0].Data))
	from, _ := mail.ParseAddress(msgA.Header.Get("From"))
	parts := strings.Split(from.Name, ", ")
	fakeFirst := parts[1]
	// The reply's body names Sarah, known only from the first email's headers,
	// and its Windows-1252 text is now UTF-8.
	if !strings.Contains(decodedText(t, outs[1].Data), "Thanks "+fakeFirst+", paid. Café receipt attached – ") {
		t.Errorf("reply body does not use %q:\n%s", fakeFirst, decodedText(t, outs[1].Data))
	}
	if !strings.Contains(decodedText(t, outs[0].Data), fakeFirst+" "+parts[0]) {
		t.Error("signature name does not match the From header")
	}
	// Tom is the same fake in both emails.
	toA, _ := mail.ParseAddress(msgA.Header.Get("To"))
	msgB, _ := mail.ReadMessage(bytes.NewReader(outs[1].Data))
	fromB, _ := mail.ParseAddress(msgB.Header.Get("From"))
	if toA.Address != fromB.Address || toA.Name != fromB.Name {
		t.Errorf("Tom differs: %v vs %v", toA, fromB)
	}
}

func TestLinksAndAttachments(t *testing.T) {
	out := run(t, newEngine(t, newMemRegistry()), original)[0]
	root, _ := mimetree.Parse(out.Data)
	var html, img, pdf *mimetree.Part
	root.Walk(func(p *mimetree.Part) {
		switch mt, _ := p.MediaType(); mt {
		case "text/html":
			html = p
		case "image/png":
			img = p
		case "application/pdf":
			pdf = p
		}
	})
	cid := strings.Trim(img.Header.Get("Content-ID"), "<>")
	h, _ := html.Text()
	if !strings.Contains(h, "cid:"+cid) {
		t.Errorf("HTML does not reference the new Content-ID %s", cid)
	}
	if !strings.HasPrefix(filename(img), "image-") || !strings.HasPrefix(filename(pdf), "attachment-") || !strings.HasSuffix(filename(pdf), ".pdf") {
		t.Errorf("filenames = %s, %s", filename(img), filename(pdf))
	}
	data, _ := pdf.Decoded()
	if !bytes.HasPrefix(data, []byte("%PDF-1.4")) || bytes.Contains(data, []byte("real invoice")) {
		t.Error("PDF attachment not replaced with a valid PDF")
	}
	png, _ := img.Decoded()
	if !bytes.HasPrefix(png, []byte("\x89PNG")) || len(png) >= signatureImageLimit {
		t.Errorf("image replacement = %d bytes", len(png))
	}
}

func TestDeterministicForTheSameKey(t *testing.T) {
	a := run(t, newEngine(t, newMemRegistry()), original, reply)
	b := run(t, newEngine(t, newMemRegistry()), original, reply)
	for i := range a {
		if !bytes.Equal(a[i].Data, b[i].Data) {
			t.Errorf("email %d differs between runs with the same key", i)
		}
	}
	other, _ := New(mustKey(t, "another-key-0123456789"), defaultProfile(t), wordDetector{}, newMemRegistry())
	c := run(t, other, original, reply)
	if bytes.Equal(a[0].Data, c[0].Data) {
		t.Error("a different key gave the same output")
	}
}

func TestDatesAreKept(t *testing.T) {
	out := run(t, newEngine(t, newMemRegistry()), original)[0]
	if _, err := mail.ReadMessage(bytes.NewReader(out.Data)); err != nil {
		t.Fatal(err)
	}
	if !regexp.MustCompile(`(?m)^Date: Mon, 3 Feb 2025 09:15:00 \+1100\r$`).Match(out.Data) {
		t.Error("Date header changed; dates are kept")
	}
}

func mustKey(t *testing.T, s string) Key {
	t.Helper()
	k, err := NewKey([]byte(s))
	if err != nil {
		t.Fatal(err)
	}
	return k
}

// TestRealPresidio runs the engine against a running Presidio Analyzer. It
// runs only when EXEMPLAR_TEST_PRESIDIO_URL is set (task test:presidio).
func TestRealPresidio(t *testing.T) {
	url := os.Getenv("EXEMPLAR_TEST_PRESIDIO_URL")
	if url == "" {
		t.Skip("set EXEMPLAR_TEST_PRESIDIO_URL to run against a real Presidio Analyzer")
	}
	e, err := New(testKey(t), defaultProfile(t), NewPresidio(url), newMemRegistry())
	if err != nil {
		t.Fatal(err)
	}
	outs := run(t, e, original, reply)
	for i, out := range outs {
		text := decodedText(t, out.Data) + string(out.Data)
		for _, s := range secrets {
			if strings.Contains(strings.ToLower(text), strings.ToLower(s)) {
				t.Errorf("email %d still contains %q", i, s)
			}
		}
	}
}
