package mimetree

import (
	"bytes"
	"errors"
	"fmt"
	"net/mail"
	"strings"
	"testing"
	"time"

	"github.com/martinsuchenak/exemplar/internal/synth"
)

// shape describes a tree for comparison: media types depth first and the
// decoded leaf bodies.
func shape(t *testing.T, p *Part) []string {
	t.Helper()
	var out []string
	p.Walk(func(q *Part) {
		mt, _ := q.MediaType()
		out = append(out, mt)
		if !q.IsMultipart() {
			d, err := q.Decoded()
			if err != nil {
				t.Fatalf("decode %s: %v", mt, err)
			}
			out = append(out, string(bytes.ReplaceAll(d, []byte("\r\n"), []byte("\n"))))
		}
	})
	return out
}

func TestRoundTripEveryShape(t *testing.T) {
	g := synth.New(9)
	c := g.CompanyName()
	people := []synth.Person{g.Person(c), g.Person(c), g.Person(g.CompanyName())}
	for _, s := range synth.Shapes {
		emails, err := g.Emails(synth.EmailInput{Shape: s, People: people})
		if err != nil {
			t.Fatal(err)
		}
		for _, e := range emails {
			p, err := Parse(e.Data)
			if err != nil {
				t.Fatalf("%s: %v", s, err)
			}
			out := p.Bytes()
			if !bytes.Equal(out, e.Data) {
				q, err := Parse(out)
				if err != nil {
					t.Fatalf("%s: reparse: %v", s, err)
				}
				if strings.Join(shape(t, p), "|") != strings.Join(shape(t, q), "|") {
					t.Errorf("%s: structure changed on round trip", s)
				}
			}
			if _, err := mail.ReadMessage(bytes.NewReader(out)); err != nil {
				t.Errorf("%s: output is not a valid message: %v", s, err)
			}
		}
	}
}

const nested = "From: a@x.example\nTo: b@y.example\nSubject: =?iso-8859-1?Q?Caf=E9?=\nContent-Type: multipart/mixed;\n boundary=\"OUTER\"\n\nThis is a preamble.\n--OUTER\nContent-Type: multipart/alternative; boundary=INNER\n\n--INNER\nContent-Type: text/plain; charset=windows-1252\nContent-Transfer-Encoding: 8bit\n\nprice \x80 5\n--INNER\nContent-Type: text/html; charset=utf-8\nContent-Transfer-Encoding: base64\n\nPGI+aGk8L2I+\n--INNER--\n--OUTER\nContent-Type: application/pdf; name=\"a.pdf\"\nContent-Disposition: attachment; filename=\"a.pdf\"\nContent-Transfer-Encoding: base64\n\nJVBERi0xLjQ=\n--OUTER--\nepilogue\n"

func TestNestedParseEditAndWrite(t *testing.T) {
	p, err := Parse([]byte(nested))
	if err != nil {
		t.Fatal(err)
	}
	if !p.IsMultipart() || len(p.Parts) != 2 || !p.Parts[0].IsMultipart() || len(p.Parts[0].Parts) != 2 {
		t.Fatalf("structure = %v", shape(t, p))
	}
	if string(p.Preamble) != "This is a preamble." || string(p.Epilogue) != "epilogue" {
		t.Errorf("preamble/epilogue = %q / %q", p.Preamble, p.Epilogue)
	}
	if DecodeHeader(p.Header.Get("subject")) != "Café" {
		t.Errorf("subject = %q", DecodeHeader(p.Header.Get("Subject")))
	}

	plain, html := p.Parts[0].Parts[0], p.Parts[0].Parts[1]
	if s, err := plain.Text(); err != nil || s != "price € 5" {
		t.Errorf("windows-1252 text = %q, %v", s, err)
	}
	if s, _ := html.Text(); s != "<b>hi</b>" {
		t.Errorf("base64 html = %q", s)
	}

	plain.SetText("price € 6 — ok")
	html.SetText("<b>bye</b>")
	pdf := p.Parts[1]
	pdf.SetBytes([]byte("%PDF-1.7"))
	pdf.SetParam("name", "b.pdf")

	q, err := Parse(p.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	plain2 := q.Parts[0].Parts[0]
	if plain2.Encoding() != "quoted-printable" {
		t.Errorf("8bit non-ASCII text should become quoted-printable, got %s", plain2.Encoding())
	}
	if _, params := plain2.MediaType(); params["charset"] != "utf-8" {
		t.Errorf("charset = %q, want utf-8", params["charset"])
	}
	if s, _ := plain2.Text(); s != "price € 6 — ok" {
		t.Errorf("rewritten text = %q", s)
	}
	if q.Parts[0].Parts[1].Encoding() != "base64" {
		t.Error("base64 part changed encoding")
	}
	if d, _ := q.Parts[1].Decoded(); string(d) != "%PDF-1.7" {
		t.Errorf("attachment = %q", d)
	}
	if _, params := q.Parts[1].MediaType(); params["name"] != "b.pdf" {
		t.Errorf("name param = %v", params)
	}
	if !bytes.Contains(p.Bytes(), []byte("\r\n")) || bytes.Contains(bytes.ReplaceAll(p.Bytes(), []byte("\r\n"), nil), []byte("\n")) {
		t.Error("output does not use CRLF only")
	}
}

func TestHeaderEditing(t *testing.T) {
	var h Header
	h.Fields = []Field{{"To", "a"}, {"X-A", "1"}, {"to", "b"}}
	h.Set("TO", "c")
	if len(h.Fields) != 2 || h.Get("to") != "c" {
		t.Errorf("Set = %+v", h.Fields)
	}
	h.Set("New", "v")
	h.Del("x-a")
	if h.Get("X-A") != "" || h.Get("new") != "v" {
		t.Errorf("Del/Set = %+v", h.Fields)
	}
	if EncodeHeader("plain") != "plain" || !strings.HasPrefix(EncodeHeader("café"), "=?utf-8?q?") {
		t.Errorf("EncodeHeader = %q", EncodeHeader("café"))
	}
	if DecodeHeader("=?x-unknown?q?abc?=") != "=?x-unknown?q?abc?=" {
		t.Error("undecodable word should stay as is")
	}
	long := "Subject: " + strings.Repeat("word ", 300)
	if f := fold(long); !strings.Contains(f, "\r\n") || len(strings.SplitN(f, "\r\n", 2)[0]) > 998 {
		t.Error("long header not folded")
	}
}

func TestEdgeCases(t *testing.T) {
	if s, err := ToUTF8([]byte{0x63, 0x61, 0x66, 0xe9}, "iso-8859-1"); err != nil || s != "café" {
		t.Errorf("latin-1 = %q, %v", s, err)
	}
	if s, _ := ToUTF8([]byte{0xe9}, "utf-8"); s != "é" {
		t.Errorf("invalid UTF-8 fallback = %q", s)
	}
	if _, err := ToUTF8([]byte("x"), "shift_jis"); !errors.Is(err, ErrCharset) {
		t.Errorf("shift_jis err = %v", err)
	}
	// Unclosed multipart and a body with no header.
	p, err := Parse([]byte("Content-Type: multipart/mixed; boundary=B\r\n\r\n--B\r\n\r\nonly part\r\n"))
	if err != nil || len(p.Parts) != 1 {
		t.Fatalf("unclosed = %+v, %v", p, err)
	}
	if p, _ := Parse([]byte("just text")); p.Header.Get("x") != "" || string(p.Body) != "just text" {
		t.Errorf("headerless = %+v", p)
	}
	bad := &Part{Header: Header{Fields: []Field{{"Content-Transfer-Encoding", "base64"}}}, Body: []byte("!!!")}
	if _, err := bad.Decoded(); err == nil {
		t.Error("bad base64 decoded")
	}
	unpadded := &Part{Header: Header{Fields: []Field{{"Content-Transfer-Encoding", "base64"}}}, Body: []byte("aGk")}
	if d, err := unpadded.Decoded(); err != nil || string(d) != "hi" {
		t.Errorf("unpadded = %q, %v", d, err)
	}
	if mt, _ := (&Part{Header: Header{Fields: []Field{{"Content-Type", "text/html; ; bad=="}}}}).MediaType(); mt != "text/html" {
		t.Errorf("malformed params type = %q", mt)
	}
}

// deepMessage builds a message nesting levels multiparts, each with its own
// boundary (the shape that made parsing quadratic), padded to about size
// bytes.
func deepMessage(levels, size int) []byte {
	var b strings.Builder
	b.WriteString("From: a@b.example\r\nContent-Type: multipart/mixed; boundary=\"q0\"\r\n\r\n")
	for i := 0; i < levels; i++ {
		fmt.Fprintf(&b, "--q%d\r\nContent-Type: multipart/mixed; boundary=\"q%d\"\r\n\r\n", i, i+1)
	}
	fmt.Fprintf(&b, "--q%d\r\nContent-Type: text/plain\r\n\r\n", levels)
	pad := size - b.Len()
	for pad > 0 {
		b.WriteString("padding padding padding padding padding padding padding padding\r\n")
		pad -= 66
	}
	for i := levels; i >= 0; i-- {
		fmt.Fprintf(&b, "--q%d--\r\n", i)
	}
	return []byte(b.String())
}

func TestDepthAndPartLimits(t *testing.T) {
	if _, err := Parse(deepMessage(MaxDepth-1, 0)); err != nil {
		t.Errorf("%d levels: %v", MaxDepth-1, err)
	}
	if _, err := Parse(deepMessage(MaxDepth+1, 0)); err == nil || !strings.Contains(err.Error(), "levels deep") {
		t.Errorf("%d levels err = %v", MaxDepth+1, err)
	}

	var many strings.Builder
	many.WriteString("Content-Type: multipart/mixed; boundary=B\r\n\r\n")
	for i := 0; i <= MaxParts; i++ {
		many.WriteString("--B\r\n\r\nx\r\n")
	}
	many.WriteString("--B--\r\n")
	if _, err := Parse([]byte(many.String())); err == nil || !strings.Contains(err.Error(), "parts") {
		t.Errorf("%d parts err = %v", MaxParts+1, err)
	}

	// An attached message continues its parent's depth.
	root, err := Parse(deepMessage(3, 0))
	if err != nil {
		t.Fatal(err)
	}
	leaf := root
	for leaf.IsMultipart() {
		leaf = leaf.Parts[0]
	}
	if leaf.Depth != 4 {
		t.Errorf("leaf depth = %d", leaf.Depth)
	}
	if _, err := ParseAt(deepMessage(3, 0), MaxDepth-2); err == nil {
		t.Error("an attached message got a fresh depth budget")
	}
}

func TestDeepLargeMessagesAreCheap(t *testing.T) {
	// Strix's payloads: before, 10 MB at 150,000 levels did not finish in
	// 20 s even with a depth cap, and 1.4 MB at 20,000 levels used 19 GB.
	for _, tt := range []struct{ levels, size int }{{150000, 10 << 20}, {20000, 1 << 20}, {MaxDepth - 1, 10 << 20}} {
		data := deepMessage(tt.levels, tt.size)
		start := time.Now()
		_, err := Parse(data)
		if took := time.Since(start); took > 10*time.Second {
			t.Errorf("%d levels, %d bytes: %v (err %v)", tt.levels, len(data), took, err)
		}
		if tt.levels > MaxDepth && err == nil {
			t.Errorf("%d levels accepted", tt.levels)
		}
		if tt.levels < MaxDepth && err != nil {
			t.Errorf("%d levels, %d bytes: %v", tt.levels, len(data), err)
		}
	}
}

func TestLineEndingsAreKept(t *testing.T) {
	// LF-only input parses the same as CRLF input.
	crlf := "Content-Type: multipart/mixed; boundary=B\r\n\r\npre\r\n--B\r\nContent-Type: text/plain\r\n\r\nline 1\r\nline 2\r\n--B--\r\npost\r\n"
	lf := strings.ReplaceAll(crlf, "\r\n", "\n")
	a, _ := Parse([]byte(crlf))
	b, _ := Parse([]byte(lf))
	if string(a.Parts[0].Body) != "line 1\r\nline 2" || string(b.Parts[0].Body) != string(a.Parts[0].Body) ||
		string(a.Preamble) != "pre" || string(b.Epilogue) != "post" {
		t.Errorf("bodies %q / %q, preamble %q, epilogue %q", a.Parts[0].Body, b.Parts[0].Body, a.Preamble, b.Epilogue)
	}
	if !bytes.Equal(a.Bytes(), b.Bytes()) {
		t.Error("CRLF and LF input serialise differently")
	}
}

func TestSetBoundary(t *testing.T) {
	msg := "Content-Type: multipart/mixed; charset=x; boundary=\"old@sender\"\r\n\r\n--old@sender\r\nContent-Type: text/plain\r\n\r\nhi\r\n--old@sender--\r\n"
	p, err := Parse([]byte(msg))
	if err != nil {
		t.Fatal(err)
	}
	p.SetBoundary("=_new")
	out := string(p.Bytes())
	if strings.Contains(out, "old@sender") {
		t.Errorf("old boundary survived:\n%s", out)
	}
	again, err := Parse([]byte(out))
	if err != nil {
		t.Fatal(err)
	}
	if _, params := again.MediaType(); again.Boundary != "=_new" || params["charset"] != "x" || len(again.Parts) != 1 {
		t.Errorf("reparsed: boundary %q, params %v, %d parts", again.Boundary, params, len(again.Parts))
	}
}
