package emailtemplate

import (
	"bytes"
	"net/mail"
	"regexp"
	"strings"
	"testing"

	"github.com/martinsuchenak/exemplar/internal/mimetree"
	"github.com/martinsuchenak/exemplar/internal/synth"
)

// cast returns three people at two companies, the first two colleagues.
func cast(seed uint64, a, b string) []synth.Person {
	g := synth.New(seed)
	return []synth.Person{g.Person(a), g.Person(a), g.Person(b)}
}

// visible returns the headers and the non-attachment text a reader sees,
// decoded, including attached messages.
func visible(t *testing.T, data []byte) string {
	t.Helper()
	root, err := mimetree.Parse(data)
	if err != nil {
		t.Fatal(err)
	}
	var b strings.Builder
	var walk func(p *mimetree.Part, headers bool)
	walk = func(p *mimetree.Part, headers bool) {
		if headers {
			for _, f := range p.Header.Fields {
				b.WriteString(f.Name + ": " + mimetree.DecodeHeader(f.Value) + "\n")
			}
		}
		if p.IsMultipart() {
			for _, c := range p.Parts {
				walk(c, false)
			}
			return
		}
		mt, _ := p.MediaType()
		if strings.HasPrefix(strings.ToLower(p.Header.Get("Content-Disposition")), "attachment") && mt != "message/rfc822" {
			return
		}
		switch {
		case mt == "message/rfc822":
			raw, _ := p.Decoded()
			inner, err := mimetree.Parse(raw)
			if err != nil {
				t.Fatal(err)
			}
			walk(inner, true)
		case strings.HasPrefix(mt, "text/") || strings.HasPrefix(mt, "message/"):
			raw, _ := p.Decoded()
			b.Write(raw)
		}
	}
	walk(root, true)
	return b.String()
}

func mentions(text, s string) bool {
	return regexp.MustCompile(`(?i)(^|[^\pL\pN])` + regexp.QuoteMeta(s) + `($|[^\pL\pN])`).MatchString(text)
}

func shapeTree(t *testing.T, data []byte) string {
	t.Helper()
	root, err := mimetree.Parse(data)
	if err != nil {
		t.Fatal(err)
	}
	var types []string
	root.Walk(func(p *mimetree.Part) {
		mt, _ := p.MediaType()
		types = append(types, mt)
	})
	return strings.Join(types, ",")
}

func TestRenderEveryShape(t *testing.T) {
	src := cast(1, "Acme Plumbing Pty Ltd", "Harbour Build Group")
	dst := cast(2, "Noosa Engineering Pty Ltd", "Kestrel Logistics")
	for i, shape := range synth.Shapes {
		emails, err := synth.New(uint64(10 + i)).Emails(synth.EmailInput{Profile: synth.ProfileInsurance, Shape: shape, People: src})
		if err != nil {
			t.Fatalf("%s: %v", shape, err)
		}
		for _, e := range emails {
			swaps := []Swap{{src[0], dst[0]}, {src[1], dst[1]}, {src[2], dst[2]}}
			r, err := Render(e.Data, swaps, synth.New(99))
			if err != nil {
				t.Fatalf("%s: %v", shape, err)
			}
			if _, err := mail.ReadMessage(bytes.NewReader(r.Data)); err != nil {
				t.Fatalf("%s: output is not an email: %v", shape, err)
			}
			if shapeTree(t, r.Data) != shapeTree(t, e.Data) {
				t.Errorf("%s: structure changed", shape)
			}
			text := visible(t, r.Data)
			for _, p := range src {
				for _, v := range []string{p.FirstName, p.LastName, p.Email, p.Phone, synth.CompanyStem(p.Company), synth.CompanyFor(p.Company).Domain} {
					if mentions(text, v) {
						t.Errorf("%s: %q survived:\n%s", shape, v, text)
					}
				}
			}
			if e.MessageID != "" && bytes.Contains(r.Data, []byte(strings.Trim(e.MessageID, "<>"))) {
				t.Errorf("%s: Message-ID kept", shape)
			}
		}
	}
}

func TestThreadIDsStayLinked(t *testing.T) {
	src := cast(3, "Acme Plumbing Pty Ltd", "Harbour Build Group")
	emails, err := synth.New(5).Emails(synth.EmailInput{Profile: synth.ProfileInsurance, Shape: synth.ShapeReply, People: src})
	if err != nil {
		t.Fatal(err)
	}
	dst := cast(4, "Noosa Engineering Pty Ltd", "Kestrel Logistics")
	r, err := Render(emails[0].Data, []Swap{{src[0], dst[0]}, {src[2], dst[2]}}, synth.New(1))
	if err != nil {
		t.Fatal(err)
	}
	root, _ := mimetree.Parse(r.Data)
	irt, refs := root.Header.Get("In-Reply-To"), root.Header.Get("References")
	if irt == "" || !strings.Contains(refs, irt) {
		t.Errorf("In-Reply-To %q not in References %q", irt, refs)
	}
	if !strings.HasSuffix(strings.TrimSpace(root.Header.Get("Message-ID")), "@"+synth.CompanyFor(dst[0].Company).Domain+">") {
		t.Errorf("Message-ID %q not on the new sender's domain", root.Header.Get("Message-ID"))
	}
	again, _ := Render(emails[0].Data, []Swap{{src[0], dst[0]}, {src[2], dst[2]}}, synth.New(1))
	if !bytes.Equal(again.Data, r.Data) {
		t.Error("same swaps and seed rendered differently")
	}
	other, _ := Render(emails[0].Data, []Swap{{src[0], dst[0]}, {src[2], dst[2]}}, synth.New(2))
	if bytes.Equal(other.Data, r.Data) {
		t.Error("a different seed gave the same Message-IDs")
	}
}

const encoded = "From: Ann Lee <ann.lee@acme.example>\r\nTo: accounts@acme.example\r\nSubject: =?utf-8?q?Caf=C3=A9_for_Ann?=\r\nMessage-ID: <x1@acme.example>\r\nMIME-Version: 1.0\r\nContent-Type: multipart/mixed; boundary=B\r\n\r\n--B\r\nContent-Type: text/html; charset=utf-8\r\nContent-Transfer-Encoding: base64\r\n\r\nPHA+SGkgQW5uLCBzZWUgPGEgaHJlZj0iaHR0cHM6Ly93d3cuYWNtZS5leGFtcGxlL3EiPmxpbms8L2E+PC9wPg==\r\n--B\r\nContent-Type: text/plain; name=notes.txt\r\nContent-Disposition: attachment; filename=notes.txt\r\n\r\nAnn's notes\r\n--B--\r\n"

func TestEncodedPartsAndAttachments(t *testing.T) {
	ann := synth.Person{FirstName: "Ann", LastName: "Lee", Email: "ann.lee@acme.example"}
	bea := synth.Person{FirstName: "Bea", LastName: "Ray", Email: "bea.ray@zeta.example"}
	r, err := Render([]byte(encoded), []Swap{{ann, bea}}, synth.New(1))
	if err != nil {
		t.Fatal(err)
	}
	root, _ := mimetree.Parse(r.Data)
	html, _ := root.Parts[0].Text()
	if html != `<p>Hi Bea, see <a href="https://www.zeta.example/q">link</a></p>` {
		t.Errorf("html = %q", html)
	}
	if r.Subject != "Café for Bea" || root.Header.Get("To") != "accounts@zeta.example" {
		t.Errorf("subject %q, to %q", r.Subject, root.Header.Get("To"))
	}
	if note, _ := root.Parts[1].Text(); !strings.Contains(note, "Ann's notes") {
		t.Errorf("attachment changed: %q", note)
	}
	if _, err := Render([]byte("not an email"), nil, synth.New(1)); err == nil {
		t.Error("garbage accepted")
	}
	sjis := "From: ann.lee@acme.example\r\nContent-Type: text/plain; charset=shift_jis\r\n\r\n\x82\xa0\r\n"
	if _, err := Render([]byte(sjis), []Swap{{ann, bea}}, synth.New(1)); err == nil {
		t.Error("unsupported charset accepted")
	}
}

func TestPairs(t *testing.T) {
	a := synth.Person{FirstName: "Ann", LastName: "Lee", Email: "ann@acme.example", Company: "Acme Pty Ltd"}
	b := synth.Person{FirstName: "Bo", LastName: "Lee", Email: "bo@acme.example", Company: "Acme Pty Ltd"}
	x := synth.Person{FirstName: "Xi", LastName: "Kent", Email: "xi@zeta.example", Company: "Zeta Group"}
	y := synth.Person{FirstName: "Yu", LastName: "Moss", Email: "yu@omega.example", Company: "Omega Group"}
	anyCase, exact := Pairs([]Swap{{a, x}, {b, y}, {a, a}})
	if exact["Lee"] != "Kent" || anyCase["acme.example"] != "zeta.example" || exact["Acme"] != "Zeta" || anyCase["acme"] != "zeta" || exact["Lee, Ann"] != "Kent, Xi" {
		t.Errorf("first swap should win: %v %v", anyCase, exact)
	}
	if _, ok := anyCase["ann@acme.example"]; !ok {
		t.Error("email missing")
	}
	for _, m := range []map[string]string{anyCase, exact} {
		for k, v := range m {
			if strings.EqualFold(k, v) {
				t.Errorf("unchanged pair %q", k)
			}
		}
	}
}

func TestNamesThatAreWords(t *testing.T) {
	grace := synth.Person{FirstName: "Grace", LastName: "Price", Email: "grace.price@acme.example", JobTitle: "Manager"}
	ann := synth.Person{FirstName: "Ann", LastName: "Lee", Email: "ann.lee@zeta.example", JobTitle: "Director"}
	msg := "From: Grace Price <grace.price@acme.example>\r\nTo: ops@acme.example\r\nSubject: Price check from GRACE\r\n\r\n" +
		"Within the grace period the price is fixed; the manager agreed.\r\nRegards, Grace PRICE, Manager\r\n"
	r, err := Render([]byte(msg), []Swap{{grace, ann}}, synth.New(1))
	if err != nil {
		t.Fatal(err)
	}
	out := string(r.Data)
	for _, want := range []string{"Within the grace period the price is fixed; the manager agreed.", "Regards, Ann LEE, Director", "Subject: Lee check from ANN", "To: ops@zeta.example"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
}
