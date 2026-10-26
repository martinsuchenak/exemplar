package mimetree

import (
	"bytes"
	"errors"
	"net/mail"
	"strings"
	"testing"

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
