package anonymise

import (
	"bytes"
	"encoding/json"
	"errors"
	"image"
	_ "image/jpeg"
	_ "image/png"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/martinsuchenak/exemplar/internal/mimetree"
	"github.com/martinsuchenak/exemplar/internal/synth"
)

func TestProfiles(t *testing.T) {
	p := defaultProfile(t)
	if p.ScoreThreshold != 0.5 || !p.keepsDomain("mail.gmail.com") || p.keepsDomain("acme.com.au") || !p.isStop("Regards") || !p.keepLocal["noreply"] {
		t.Errorf("default profile = %+v", p)
	}
	if sub, reg := p.splitDomain("mail.eu.acme.com.au"); reg != "acme" || strings.Join(sub, ".") != "mail.eu" {
		t.Errorf("splitDomain = %v %s", sub, reg)
	}
	if sub, reg := p.splitDomain("acme.io"); reg != "acme" || len(sub) != 0 {
		t.Errorf("splitDomain(acme.io) = %v %s", sub, reg)
	}

	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "nz.toml"), []byte("name = \"nz\"\nscore_threshold = 0\nkeep_domains = [\"xtra.co.nz\"]\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	ps, err := LoadProfiles(dir)
	if err != nil || ps["nz"] == nil || ps["default"] == nil || ps["nz"].ScoreThreshold != 0.5 || !ps["nz"].keepsDomain("xtra.co.nz") {
		t.Fatalf("profiles = %v, %v", ps, err)
	}
	if strings.Join(ProfileNames(ps), ",") != "default,nz" {
		t.Errorf("names = %v", ProfileNames(ps))
	}
	if err := os.WriteFile(filepath.Join(dir, "bad.toml"), []byte("name = \"Bad Name\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadProfiles(dir); err == nil {
		t.Error("bad profile name accepted")
	}
	if err := os.WriteFile(filepath.Join(dir, "bad.toml"), []byte("name = [\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadProfiles(dir); err == nil {
		t.Error("invalid TOML accepted")
	}
}

func TestKeys(t *testing.T) {
	if _, err := NewKey([]byte("short")); err == nil {
		t.Error("short key accepted")
	}
	a, b := mustKey(t, "  0123456789abcdef\n"), mustKey(t, "0123456789abcdef")
	if a.ID != b.ID || !strings.HasPrefix(a.ID, "k-") || len(a.ID) != 14 {
		t.Errorf("ids = %s, %s (whitespace should not change the key)", a.ID, b.ID)
	}
	c := mustKey(t, "fedcba9876543210")
	if a.ID == c.ID || a.Hash("x", "v") == c.Hash("x", "v") {
		t.Error("different keys collide")
	}
	if a.Hash("x", "Value") != a.Hash("x", "value") || a.Hash("x", "v") == a.Hash("y", "v") {
		t.Error("hash should ignore case and depend on kind")
	}
	if (Key{}).Valid() {
		t.Error("zero key is valid")
	}
	if _, err := New(Key{}, defaultProfile(t), wordDetector{}, newMemRegistry()); err == nil {
		t.Error("engine without key")
	}
	if _, err := New(a, nil, wordDetector{}, newMemRegistry()); err == nil {
		t.Error("engine without profile")
	}
}

func TestPresidioClient(t *testing.T) {
	var got presidioRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/analyze" {
			http.NotFound(w, r)
			return
		}
		_ = json.NewDecoder(r.Body).Decode(&got)
		// "Zoë Lane": Presidio counts characters, so Lane starts at 4.
		_ = json.NewEncoder(w).Encode([]presidioResult{
			{EntityType: "PERSON", Start: 0, End: 8, Score: 0.9},
			{EntityType: "PERSON", Start: 4, End: 8, Score: 0.9},
			{EntityType: "PERSON", Start: 7, End: 99, Score: 0.9},
		})
	}))
	defer srv.Close()

	spans, err := NewPresidio(srv.URL+"/").Detect(ctx, "Zoë Lane", []string{"Lane"}, 0.6)
	if err != nil {
		t.Fatal(err)
	}
	if len(spans) != 2 || spans[0].End != len("Zoë Lane") || "Zoë Lane"[spans[1].Start:spans[1].End] != "Lane" {
		t.Errorf("spans = %+v (out-of-range results must be dropped)", spans)
	}
	if got.ScoreThreshold != 0.6 || got.Language != "en" || len(got.AdHoc) != 1 || got.AdHoc[0].DenyList[0] != "Lane" {
		t.Errorf("request = %+v", got)
	}
	if spans, err := NewPresidio(srv.URL).Detect(ctx, "   ", nil, 0.5); err != nil || len(spans) != 0 {
		t.Errorf("blank text = %v, %v", spans, err)
	}

	broken := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "model not loaded", http.StatusInternalServerError)
	}))
	defer broken.Close()
	if _, err := NewPresidio(broken.URL).Detect(ctx, "text", nil, 0.5); err == nil || !strings.Contains(err.Error(), "model not loaded") {
		t.Errorf("error = %v", err)
	}
	garbage := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte("{")) }))
	defer garbage.Close()
	if _, err := NewPresidio(garbage.URL).Detect(ctx, "text", nil, 0.5); err == nil {
		t.Error("bad JSON accepted")
	}
	closed := httptest.NewServer(http.NotFoundHandler())
	closed.Close()
	if _, err := NewPresidio(closed.URL).Detect(ctx, "text", nil, 0.5); err == nil {
		t.Error("closed server accepted")
	}
}

func TestPatterns(t *testing.T) {
	text := "Call 0412 345 678 or (02) 9876 5432 or 1300 123 456. ABN: 51 824 753 556, ACN 004 085 616. " +
		"Pay $1,234.56 or 99.50 AUD. Unit 3, 14 Smith Street. PO Box 123. Acme Plumbing Pty Ltd. " +
		"See https://acme.example/x?y=1 and www.acme.com.au/path; mail bob@acme.com.au."
	want := map[string]string{
		"0412 345 678": EntityPhone, "(02) 9876 5432": EntityPhone, "1300 123 456": EntityPhone,
		"51 824 753 556": EntityABN, "004 085 616": EntityACN, "$1,234.56": EntityMoney, "99.50 AUD": EntityMoney,
		"Unit 3, 14 Smith Street.": EntityStreet, "PO Box 123": EntityStreet, "Acme Plumbing Pty Ltd": EntityCompany,
		"https://acme.example/x?y=1": EntityURL, "www.acme.com.au/path;": EntityURL, "bob@acme.com.au": EntityEmail,
	}
	got := map[string]string{}
	for _, s := range patternSpans(text) {
		got[text[s.Start:s.End]] = s.Entity
	}
	for v, e := range want {
		if got[v] != e {
			t.Errorf("%q = %q, want %s", v, got[v], e)
		}
	}
}

func TestFoundFilters(t *testing.T) {
	f := newFound(defaultProfile(t))
	f.add("x", EntityPerson)
	f.add("Regards", EntityPerson)
	f.add("Kind Regards", EntityPerson)
	f.add(" (Sarah). ", EntityPerson)
	f.add("bob@acme.com.au", EntityURL)
	f.add("bob@acme.com.au", EntityEmail)
	f.add("bob@acme.com.au", EntityPerson)
	f.add("acme.com", EntityURL)
	f.dropFragments()
	if len(f.entities) != 2 || f.entities["Sarah"] != EntityPerson || f.entities["bob@acme.com.au"] != EntityEmail {
		t.Errorf("entities = %v", f.entities)
	}
	for v, want := range map[string]bool{"Sarah Lane": true, "Sarah\nOps": false, "UID:x": false, "ABN": false, "123": false} {
		if plausibleName(v) != want {
			t.Errorf("plausibleName(%q) != %v", v, want)
		}
	}
	h := htmlText(`<html><head><style>.x{}</style></head><body><p>Hi&nbsp;Tom</p><img alt="Acme Logo"><a href="mailto:bob@x.com">x</a></body></html>`)
	if !strings.Contains(h, "Hi Tom") || !strings.Contains(h, "Acme Logo") || !strings.Contains(h, "bob@x.com") || strings.Contains(h, ".x{}") {
		t.Errorf("htmlText = %q", h)
	}
}

func TestPseudonymRules(t *testing.T) {
	e := newEngine(t, newMemRegistry())

	if d := e.fakeDomain("mail.acme.com.au"); !strings.HasPrefix(d, "mail.") || !strings.HasSuffix(d, ".example") || strings.Contains(d, "acme") {
		t.Errorf("fakeDomain = %s", d)
	}
	if e.fakeDomain("smtp.gmail.com") != "smtp.gmail.com" || e.fakeDomain("localhost") != "localhost" {
		t.Error("kept or dotless domains changed")
	}
	if e.fakeDomain("acme.com.au") != e.fakeDomain("ACME.com.au") || e.fakeDomain("acme.com.au") == e.fakeDomain("other.com.au") {
		t.Error("fakeDomain not consistent")
	}

	for in, check := range map[string]func(string) bool{
		"Acme Plumbing Pty Ltd": func(s string) bool { return strings.HasSuffix(s, " Pty Ltd") && !strings.Contains(s, "Acme") },
		"acmeplumbing":          func(s string) bool { return s == strings.ToLower(s) && !strings.Contains(s, " ") },
		"Acme Group":            func(s string) bool { return strings.HasSuffix(s, " Group") },
	} {
		if got := e.fakeCompany(in); !check(got) {
			t.Errorf("fakeCompany(%q) = %q", in, got)
		}
	}
	if e.fakeCompany("Acme Plumbing") != synth.CompanyStem(e.companyName("acmeplumbing")) {
		t.Error("a company and its domain should share the fake")
	}

	for in, want := range map[string]string{"0412 345 678": "04", "(02) 9876 5432": "(02", "1300 123 456": "1", "$1,234.56": "$1"} {
		got := e.fakeDigits(EntityPhone, in)
		if !strings.HasPrefix(got, want) || len(got) != len(in) || got == in {
			t.Errorf("fakeDigits(%q) = %q", in, got)
		}
	}

	for in, check := range map[string]func(string) bool{
		"noreply@acme.com.au":  func(s string) bool { return strings.HasPrefix(s, "noreply@") },
		"jane.doe@acme.com.au": func(s string) bool { return !strings.Contains(s, "jane") && strings.Count(s, ".") >= 2 },
		"x7781@acme.com.au":    func(s string) bool { return strings.HasPrefix(s, "user") },
		"not-an-address":       func(s string) bool { return s == "not-an-address" },
	} {
		got, err := e.fakeEmail(ctx, in)
		if err != nil || !check(got) {
			t.Errorf("fakeEmail(%q) = %q, %v", in, got, err)
		}
	}
	for in, check := range map[string]func(string) bool{
		"mailto:bob@acme.com.au":    func(s string) bool { return strings.HasPrefix(s, "mailto:") && !strings.Contains(s, "acme") },
		"https://acme.com.au/a?b=1": func(s string) bool { return strings.HasPrefix(s, "https://") && !strings.Contains(s, "a?b") },
		"acme.com.au/path":          func(s string) bool { return !strings.Contains(s, "://") && !strings.Contains(s, "path") },
		"https://www.gmail.com/x":   func(s string) bool { return s == "https://www.gmail.com/x" },
		"https://acme.com.au":       func(s string) bool { return strings.HasSuffix(s, ".example") },
		"http://[bad":               func(s string) bool { return s == "https://anonymised.example/" },
	} {
		got, err := e.fakeURL(ctx, in)
		if err != nil || !check(got) {
			t.Errorf("fakeURL(%q) = %q, %v", in, got, err)
		}
	}
	if got := e.fakeMessageIDs("<a@acme.com.au> <noat>"); strings.Contains(got, "acme") || strings.Count(got, "<") != 2 {
		t.Errorf("fakeMessageIDs = %q", got)
	}
	if got, _ := e.fake(ctx, "UNKNOWN_THING", "v"); !strings.HasPrefix(got, "[unknown_thing-") {
		t.Errorf("unknown entity = %q", got)
	}
	for o, want := range map[string]string{"SARAH": "KENT", "sarah": "kent", "Sarah": "Kent", "S": "Kent"} {
		if got := matchCase(o, "Kent"); got != want {
			t.Errorf("matchCase(%q) = %q", o, got)
		}
	}
}

func TestFakeNamesNeverEqualOriginals(t *testing.T) {
	e := newEngine(t, newMemRegistry())
	g := synth.New(3)
	for i := 0; i < 300; i++ {
		for _, role := range []string{roleFirst, roleLast} {
			name := g.FirstName()
			if role == roleLast {
				name = g.LastName()
			}
			fake, err := e.nameToken(ctx, role, name)
			if err != nil || strings.EqualFold(fake, name) {
				t.Fatalf("%s %q mapped to itself", role, name)
			}
		}
	}
	if got, _ := e.fakeName(ctx, "Regards Sarah"); !strings.HasPrefix(got, "Regards ") {
		t.Errorf("stop word replaced: %q", got)
	}
}

func TestReplacerBoundaries(t *testing.T) {
	e := newEngine(t, newMemRegistry())
	f := newFound(e.profile)
	e.pairs = map[string]string{"Tom": "Ann", "A&B": "C&D", "acme.com": "fake.example"}
	r, err := e.replacer(ctx, f)
	if err != nil {
		t.Fatal(err)
	}
	for in, want := range map[string]string{
		"Tom, Tomas, atom, TOM.":     "Ann, Tomas, atom, ANN.",
		"tom@x.com Tom-x x-Tom @Tom": "tom@x.com Tom-x x-Tom @Tom",
		"see acme.com. acme.com.au":  "see fake.example. acme.com.au",
		"A&amp;B and A&B":            "C&amp;D and C&D",
		"nothing here":               "nothing here",
	} {
		if got := r.apply(in); got != want {
			t.Errorf("apply(%q) = %q, want %q", in, got, want)
		}
	}
	empty := &replacer{}
	if empty.apply("x") != "x" {
		t.Error("empty replacer changed text")
	}
}

const forwarded = "From: Ann Lee <ann.lee@acme.com.au>\r\nTo: Bo Ray <bo.ray@other.com.au>\r\nSubject: Fwd\r\nMessage-ID: <f1@acme.com.au>\r\nMIME-Version: 1.0\r\nContent-Type: multipart/mixed; boundary=\"B\"\r\n\r\n--B\r\nContent-Type: text/plain\r\n\r\nSee below, Ann\r\n--B\r\nContent-Type: message/rfc822\r\n\r\nFrom: Cy Dean <cy.dean@third.com.au>\r\nTo: Ann Lee <ann.lee@acme.com.au>\r\nSubject: Original from Cy\r\nReceived: x\r\n\r\nHello Ann, regards Cy\r\n--B\r\nContent-Type: text/calendar; method=REQUEST\r\n\r\nBEGIN:VCALENDAR\r\nORGANIZER;CN=Ann Lee:mailto:ann.lee@acme.com.au\r\nEND:VCALENDAR\r\n--B\r\nContent-Type: text/plain; name=\"notes for Ann.txt\"\r\nContent-Disposition: attachment; filename=\"notes for Ann.txt\"\r\n\r\nprivate notes about Cy\r\n--B--\r\n"

const bounce = "From: Mail Delivery <mailer-daemon@acme.com.au>\r\nTo: ann.lee@acme.com.au\r\nSubject: Undelivered\r\nMIME-Version: 1.0\r\nContent-Type: multipart/report; report-type=delivery-status; boundary=\"R\"\r\n\r\n--R\r\nContent-Type: text/plain\r\n\r\nCould not deliver to gone@other.com.au\r\n--R\r\nContent-Type: message/delivery-status\r\n\r\nFinal-Recipient: rfc822; gone@other.com.au\r\nAction: failed\r\n--R--\r\n"

func TestNestedCalendarAttachmentAndBounce(t *testing.T) {
	outs := run(t, newEngine(t, newMemRegistry()), forwarded, bounce)
	all := string(outs[0].Data) + string(outs[1].Data)
	for _, s := range []string{"Ann", "Lee", "Cy", "Dean", "acme.com.au", "other.com.au", "third.com.au", "private notes", "notes for", "gone@"} {
		if strings.Contains(all, s) {
			t.Errorf("output still contains %q", s)
		}
	}
	root, _ := mimetree.Parse(outs[0].Data)
	inner, _ := root.Parts[1].Decoded()
	if bytes.Contains(inner, []byte("Received:")) || !bytes.Contains(inner, []byte("Subject: Original from ")) {
		t.Errorf("attached message not rewritten:\n%s", inner)
	}
	cal, _ := root.Parts[2].Text()
	if !strings.Contains(cal, "ORGANIZER;CN=") || !strings.Contains(cal, ".example") {
		t.Errorf("calendar = %q", cal)
	}
	if outs[0].Attachments != 1 || !strings.HasPrefix(filename(root.Parts[3]), "attachment-") {
		t.Errorf("text attachment not replaced/renamed: %d %s", outs[0].Attachments, filename(root.Parts[3]))
	}
	b, _ := fromAddress(outs[1].Data)
	if !strings.HasPrefix(b, "mailer-daemon@") {
		t.Errorf("bounce sender = %s", b)
	}
}

func fromAddress(data []byte) (string, error) {
	root, err := mimetree.Parse(data)
	if err != nil {
		return "", err
	}
	v := root.Header.Get("From")
	i := strings.Index(v, "<")
	return strings.Trim(v[i:], "<>"), nil
}

func TestBadInput(t *testing.T) {
	e := newEngine(t, newMemRegistry())
	if _, err := e.Anonymise(ctx, []byte("no headers at all")); err == nil {
		t.Error("non-email accepted")
	}
	sjis := "From: a@b.com.au\r\nTo: c@d.com.au\r\nContent-Type: text/plain; charset=shift_jis\r\n\r\n\x82\xa0\r\n"
	if _, err := e.Anonymise(ctx, []byte(sjis)); !errors.Is(err, mimetree.ErrCharset) {
		t.Errorf("unsupported charset err = %v", err)
	}
	broken := "From: \"Unclosed <a@b.com.au\r\nTo: c@d.com.au\r\nSubject: x\r\n\r\nbody\r\n"
	out, err := e.Anonymise(ctx, []byte(broken))
	if err != nil || strings.Contains(string(out.Data), "b.com.au") {
		t.Errorf("unparseable address header = %v\n%s", err, out.Data)
	}
}

func TestPlaceholders(t *testing.T) {
	e := newEngine(t, newMemRegistry())
	people := []synth.Person{synth.New(1).Person("X Pty Ltd")}
	big := e.placeholder("image/png", "logo.png", 80_000, people)
	if len(big) < signatureImageLimit {
		t.Errorf("large png replacement is %d bytes", len(big))
	}
	if _, f, err := image.Decode(bytes.NewReader(big)); err != nil || f != "png" {
		t.Errorf("padded png invalid: %v", err)
	}
	jpg := e.placeholder("image/jpeg", "photo.jpeg", 120_000, people)
	if _, f, err := image.Decode(bytes.NewReader(jpg)); err != nil || f != "jpeg" || len(jpg) < signatureImageLimit {
		t.Errorf("padded jpeg = %d bytes, %v", len(jpg), err)
	}
	small := e.placeholder("image/png", "icon.png", 900, people)
	if len(small) >= signatureImageLimit {
		t.Error("small image grew past the limit")
	}
	if docx := e.placeholder("application/octet-stream", "Contract.DOCX", 5000, people); !bytes.HasPrefix(docx, []byte("PK")) {
		t.Error("docx by extension not generated")
	}
	if pdf := e.placeholder("application/pdf", "", 5000, people); !bytes.HasPrefix(pdf, []byte("%PDF")) {
		t.Error("pdf by type not generated")
	}
	if bin := e.placeholder("application/zip", "a.zip", 70_000, people); len(bin) < signatureImageLimit || !bytes.HasPrefix(bin, []byte("anonymised attachment")) {
		t.Errorf("unknown type = %d bytes", len(bin))
	}
	if tiny := e.placeholder("application/zip", "a.zip", 3, people); len(tiny) != 64 {
		t.Errorf("tiny unknown = %d bytes", len(tiny))
	}
	for mt, want := range map[string]string{"text/csv": "csv", "text/html": "html", "text/plain": "txt", "text/rtf": "rtf", "image/gif": "gif",
		"application/vnd.openxmlformats-officedocument.spreadsheetml.sheet": "xlsx", "application/x-thing": ""} {
		if got := formatFor(mt, ""); got != want {
			t.Errorf("formatFor(%s) = %q", mt, got)
		}
	}
	if padImage("gif", []byte("GIF89a"), 90_000) == nil {
		t.Error("gif padding returned nil")
	}
}

func TestNameTokens(t *testing.T) {
	for _, tt := range []struct {
		display, local string
		want           string
		lastFirst      bool
	}{
		{"Lane, Sarah", "x", "Sarah Lane", true},
		{"Sarah Jane Lane", "x", "Sarah Jane Lane", false},
		{"", "sarah.lane", "sarah lane", false},
		{"", "s7781", "", false},
		{"sarah@x.com", "sarah.lane", "sarah lane", false},
	} {
		got, lf := nameTokens(tt.display, tt.local)
		if strings.Join(got, " ") != tt.want || lf != tt.lastFirst {
			t.Errorf("nameTokens(%q, %q) = %v %v", tt.display, tt.local, got, lf)
		}
	}
}

const newsletter = "From: Ann Lee <ann.lee@acme.com.au>\r\nSender: Ann Lee <ann.lee@acme.com.au>\r\nTo: Bo Ray <bo.ray@other.com.au>\r\nSubject: News for Bo\r\nThread-Topic: News for Bo\r\nList-Unsubscribe: <https://lists.acme.com.au/u?id=bo.ray>, <mailto:unsub@acme.com.au>\r\nList-Id: Acme news <news.acme.com.au>\r\nX-Originating-IP: [10.0.0.1]\r\nContent-Type: text/plain\r\n\r\nHi Bo\r\n"

func TestListHeadersAndRoles(t *testing.T) {
	out := run(t, newEngine(t, newMemRegistry()), newsletter)[0]
	root, _ := mimetree.Parse(out.Data)
	h := string(out.Data[:bytes.Index(out.Data, []byte("\r\n\r\n"))])
	for _, s := range []string{"acme", "Ann", "Bo ", "bo.ray", "10.0.0.1"} {
		if strings.Contains(h, s) {
			t.Errorf("headers still contain %q:\n%s", s, h)
		}
	}
	if u := root.Header.Get("List-Unsubscribe"); !strings.Contains(u, "<https://lists.") || !strings.Contains(u, "<mailto:") {
		t.Errorf("List-Unsubscribe = %q", u)
	}
	if len(out.DroppedHeaders) != 1 || out.DroppedHeaders[0] != "X-Originating-IP" {
		t.Errorf("dropped = %v", out.DroppedHeaders)
	}
	roles := map[string]int{}
	for _, p := range out.People {
		roles[p.Role]++
	}
	if roles["from"] != 1 || roles["to"] != 1 {
		t.Errorf("roles = %v (From and Sender should give one from link)", out.People)
	}
}

func TestNoAddressHeaders(t *testing.T) {
	msg := "Subject: Draft\r\nMIME-Version: 1.0\r\nContent-Type: multipart/mixed; boundary=X\r\n\r\n--X\r\nContent-Type: text/plain\r\n\r\nDraft\r\n--X\r\nContent-Type: application/pdf\r\nContent-Disposition: attachment; filename=a.pdf\r\n\r\n%PDF-1.4\r\n--X--\r\n"
	out := run(t, newEngine(t, newMemRegistry()), msg)[0]
	if out.Attachments != 1 || len(out.People) != 0 {
		t.Errorf("attachments %d, people %v", out.Attachments, out.People)
	}
}
