package anonymise

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	_ "image/jpeg"
	_ "image/png"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/martinsuchenak/exemplar/internal/mimetree"
	"github.com/martinsuchenak/exemplar/internal/synth"
	"github.com/martinsuchenak/exemplar/internal/textswap"
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

// A detector's failure comes back marked ErrDetector, wherever the text was,
// with its detail kept for local use; PublicError hides that detail.
func TestDetectorErrorsAreMarked(t *testing.T) {
	down := httptest.NewServer(http.NotFoundHandler())
	down.Close()
	e, err := New(testKey(t), defaultProfile(t), NewPresidio(down.URL), newMemRegistry())
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range []File{
		{Name: "notes.md", Data: []byte("Priya called.\n")},
		{Name: "data.csv", Data: []byte("name\nPriya\n")},
	} {
		_, err := e.Anonymise(ctx, f)
		if !errors.Is(err, ErrDetector) || !strings.Contains(err.Error(), down.URL) {
			t.Errorf("%s: err = %v", f.Name, err)
		}
		if msg := PublicError(err); msg != DetectorUnavailable {
			t.Errorf("%s: public error = %q", f.Name, msg)
		}
	}
	other := errors.New("not a valid email")
	if PublicError(other) != "not a valid email" || PublicError(fmt.Errorf("wrapped: %w", ErrDetector)) != DetectorUnavailable {
		t.Error("PublicError changed an error that is not the detector's, or missed a wrapped one")
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

// A number with a calling code is a phone number in any country's format;
// without one, or with too few or too many digits, it is not taken for one.
func TestInternationalPhones(t *testing.T) {
	for text, want := range map[string]string{
		"Call +1 469-555-0100 today":   "+1 469-555-0100",
		"Tel: +213 662 00 00 10, fax":  "+213 662 00 00 10",
		"(+254 700 000123)":            "+254 700 000123",
		"+44 (0)20 7946 0000.":         "+44 (0)20 7946 0000",
		"+223 -0200 000-771 x":         "+223 -0200 000-771",
		"+6591234567":                  "+6591234567",
		"up +12.5% on 2025":            "",
		"x+1 469-555-0100":             "", // no word right before the +
		"+1 234 567":                   "", // 7 digits
		"+1 234 567 890 123 456 789":   "", // 19 digits: not one number
		"call 0801 234 5678 from home": "",
	} {
		got := ""
		for _, s := range patternSpans(text) {
			if s.Entity == EntityPhone && strings.HasPrefix(text[s.Start:s.End], "+") {
				got = text[s.Start:s.End]
			}
		}
		if got != want {
			t.Errorf("%q: phone %q, want %q", text, got, want)
		}
	}
}

// A column whose values are mostly known people is a column of people: its
// other names are people too, and so are those of a column that becomes
// one by them.
func TestAddColumnNames(t *testing.T) {
	f := newFound(defaultProfile(t))
	f.add("Wendy Moss", EntityPerson)
	f.add("Ravi Nair", EntityPerson)
	col := func(column string, values ...string) []docText {
		var out []docText
		for _, v := range values {
			out = append(out, docText{value: v, column: column})
		}
		return out
	}
	var texts []docText
	texts = append(texts, col("lead", "Wendy Moss", "Ravi Nair", "Lee Ford", "Lee Ford", "Vacant", "TBD", "")...)
	texts = append(texts, col("manager", "Lee Ford", "Ravi Nair", "Ana De La Cruz", "Sales Team 2")...) // by Lee Ford
	texts = append(texts, col("city", "Wendy Moss", "Perth West", "Port Hedland", "Mount Isa")...)      // one in four
	// One detected name on most rows makes a column of people by its cells;
	// on two of four it does not.
	texts = append(texts, col("owner", "Wendy Moss", "Wendy Moss", "Wendy Moss", "Kai Lund")...)
	texts = append(texts, col("region", "Wendy Moss", "Wendy Moss", "Swan Valley", "Swan Valley")...)
	texts = append(texts, col("notes", "Wendy Moss", "Ravi Nair", "call back", "Max Tan")...) // not counted: searched only
	texts[len(texts)-1].column = ""
	f.addColumnNames(texts)
	for v, want := range map[string]string{
		"Lee Ford": EntityPerson, "Ana De La Cruz": EntityPerson, "Kai Lund": EntityPerson, "Swan Valley": "",
		"Vacant": "", "TBD": "", "Sales Team 2": "", "Perth West": "", "Port Hedland": "", "Max Tan": "",
	} {
		if got := f.entities[v]; got != want {
			t.Errorf("%q: %q, want %q", v, got, want)
		}
	}
	for v, want := range map[string]bool{
		"Lee Ford": true, "Robert (Lachlan) Smith": true, "Vacant": false, "Lee 2": false,
		"a b c d e f": false, "SALES TEAM": true, "info@x.com": false, "Mr / Mrs": false,
	} {
		if columnName(v) != want {
			t.Errorf("columnName(%q) = %v", v, !want)
		}
	}
}

// Bank details are found by their labels; a number without one is not
// taken for an account.
func TestBankAccounts(t *testing.T) {
	for text, want := range map[string][]string{
		"Bank Details Account No: 1234 5678 BSB: 062-000":  {"1234 5678", "062-000"},
		"Account Number: 987 654 321\nBSB Number: 083-004": {"987 654 321", "083-004"},
		"Acct # 12345678, bsb 062000":                      {"12345678", "062000"},
		"A/C No. 0012-3456-78":                             {"0012-3456-78"},
		"Account Name: Lane Plumbing; invoice 1234 5678":   nil,
		"BSB: 30-6085": nil,
	} {
		var got []string
		for _, sp := range patternSpans(text) {
			if sp.Entity == EntityBankAccount {
				got = append(got, text[sp.Start:sp.End])
			}
		}
		slices.Sort(got)
		slices.Sort(want)
		if !slices.Equal(got, want) {
			t.Errorf("%q: %q, want %q", text, got, want)
		}
	}
	e := pdfEngine(t)
	out, err := e.Anonymise(ctx, File{Name: "bank.txt", Data: []byte("Pay to Account No: 1234 5678 BSB: 062-000.\n")})
	if err != nil {
		t.Fatal(err)
	}
	if got := string(out.Data); strings.Contains(got, "1234 5678") || strings.Contains(got, "062-000") ||
		!regexp.MustCompile(`Account No: \d{4} \d{4} BSB: \d{3}-\d{3}\.`).MatchString(got) {
		t.Errorf("out = %q", got)
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
	for v, want := range map[string]string{
		"Sarah Lane 0412": "Sarah Lane", "0412 Sarah Lane, 08": "Sarah Lane,", " Sarah ": "Sarah",
		"Newco 7": "Newco", "Studio54 Group": "Studio54 Group", "R2 D2": "R2 D2", "0412 158": "0412", "": "",
	} {
		if got := trimName(v); got != want {
			t.Errorf("trimName(%q) = %q, want %q", v, got, want)
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
	pairs, err := e.replacements(ctx, f)
	if err != nil {
		t.Fatal(err)
	}
	r, err := textswap.New(pairs)
	if err != nil {
		t.Fatal(err)
	}
	for in, want := range map[string]string{
		"Tom, Tomas, atom, TOM.":     "Ann, Tomas, atom, ANN.",
		"tom@x.com Tom-x x-Tom @Tom": "tom@x.com Tom-x x-Tom @Ann", // a mention is a name
		"see acme.com. acme.com.au":  "see fake.example. acme.com.au",
		"A&amp;B and A&B":            "C&amp;D and C&D",
		"nothing here":               "nothing here",
	} {
		if got := r.Apply(in); got != want {
			t.Errorf("apply(%q) = %q, want %q", in, got, want)
		}
	}
	empty := &replacer{}
	if empty.Apply("x") != "x" {
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
	if _, err := e.Anonymise(ctx, eml("no headers at all")); err == nil {
		t.Error("non-email accepted")
	}
	sjis := "From: a@b.com.au\r\nTo: c@d.com.au\r\nContent-Type: text/plain; charset=shift_jis\r\n\r\n\x82\xa0\r\n"
	if _, err := e.Anonymise(ctx, eml(sjis)); !errors.Is(err, mimetree.ErrCharset) {
		t.Errorf("unsupported charset err = %v", err)
	}
	broken := "From: \"Unclosed <a@b.com.au\r\nTo: c@d.com.au\r\nSubject: x\r\n\r\nbody\r\n"
	out, err := e.Anonymise(ctx, eml(broken))
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

// ownCompany is mail from a company's own systems: its name turns up in
// subdomains, identifiers, X-Mailer and file names, and its product and a
// client code in the subject.
const ownCompany = "From: Ops Bot <ops.bot@acmeco.com.au>\r\nTo: Priya Shah <priya.shah@acmeco.com.au>\r\n" +
	"Subject: KLIX - Widgetron-PROD-11 alert\r\nX-Mailer: Acmeco Widgetron 3\r\nUser-Agent: acmeco-mailer/2\r\n" +
	"List-Id: <widgetron-prod.acmeco-6o.acmeco.com.au>\r\nMessage-ID: <x1@klix.acmeco.com.au>\r\n" +
	"Content-Type: text/html; charset=utf-8\r\n\r\n" +
	`<p>Hi Priya, see <a href="https://acmeco-6o.acmeco.com.au/jobs/1">the job</a> and ` +
	`<a href="https://mail.acmeco.com.au/x">mail</a>.</p><img src="https://cdn.example.org/2021/official_acmeco_logo.png">` +
	"<p>Widgetron for KLIX</p>\r\n"

func TestOwnCompanyIdentifiers(t *testing.T) {
	p := defaultProfile(t)
	p.Terms = []string{"Widgetron", "KLIX"}
	e, err := New(testKey(t), p, wordDetector{names: []string{"Priya"}}, newMemRegistry())
	if err != nil {
		t.Fatal(err)
	}
	out := run(t, e, ownCompany)[0]
	all := strings.ToLower(string(out.Data) + decodedText(t, out.Data))
	for _, s := range []string{"acmeco", "widgetron", "klix", "priya"} {
		if strings.Contains(all, s) {
			t.Errorf("%q survived:\n%s", s, out.Data)
		}
	}
	for _, s := range []string{"https://mail.", "x-mailer: ", "-prod-11 alert", "_logo.png"} {
		if !strings.Contains(all, s) {
			t.Errorf("structure %q lost:\n%s", s, out.Data)
		}
	}
	if out.Entities["KLIX"] != EntityTerm {
		t.Errorf("term entity = %q", out.Entities["KLIX"])
	}
	// The same label gets the same fake everywhere; generic ones stay.
	first := e.fakeLabel("acmeco-6o")
	if first == "acmeco-6o" || e.fakeLabel("Acmeco-6o") != first || e.fakeLabel("mail2") != "mail2" || e.fakeLabel("www") != "www" {
		t.Errorf("labels: %q, generic mail2 %q, www %q", first, e.fakeLabel("mail2"), e.fakeLabel("www"))
	}
}

// dmarcReport is an aggregate report whose attachment's description names
// the reported domain.
const dmarcReport = "From: noreply-dmarc-support@google.com\r\nTo: dmarc@acmeco.com.au\r\nSubject: Report domain: acmeco.com.au\r\n" +
	"MIME-Version: 1.0\r\nContent-Type: multipart/mixed; boundary=B\r\n\r\n--B\r\nContent-Type: text/plain\r\n\r\nReport for acmeco.com.au\r\n" +
	"--B\r\nContent-Type: application/gzip; name=\"google.com!acmeco.com.au!1792800000!1792886400.xml.gz\"\r\n" +
	"Content-Description: google.com!acmeco.com.au!1792800000!1792886400.xml.gz\r\n" +
	"Content-Disposition: attachment; filename=\"google.com!acmeco.com.au!1792800000!1792886400.xml.gz\"\r\n" +
	"Content-Transfer-Encoding: base64\r\n\r\nH4sIAAAAAAAAA0tMSgYAWJhzBwQAAAA=\r\n--B--\r\n"

func TestPartDescriptions(t *testing.T) {
	out := run(t, newEngine(t, newMemRegistry()), dmarcReport)[0]
	if strings.Contains(strings.ToLower(string(out.Data)), "acmeco") {
		t.Errorf("the reported domain survived:\n%s", out.Data)
	}
	root, _ := mimetree.Parse(out.Data)
	if d := root.Parts[1].Header.Get("Content-Description"); !strings.Contains(d, "google.com!") || !strings.Contains(d, "!1792800000!") {
		t.Errorf("description lost its shape: %q", d)
	}
}

// An automated DMARC sender at a public provider, a report subject naming
// the company's domain, and a person whose display name holds the company
// name (a profile term).
const (
	dmarcFromProvider = "From: DMARC Report <dmarcreport@microsoft.com>\r\nTo: Acmeco Support <support@acmeco.com.au>\r\n" +
		"Subject: Report Domain: acmeco.com.au Submitter: protection.outlook.com\r\n\r\nReport for acmeco.com.au from Acmeco.\r\n"
	termInDisplayName = "From: Acmeco Ops <acmeco.ops@acmeco.com.au>\r\nTo: Priya Shah <priya.shah@acmeco.com.au>\r\n" +
		"Subject: Acmeco weekly\r\n\r\nHi Priya, the Acmeco report is ready.\r\n"
)

func TestAutomatedSendersAndTerms(t *testing.T) {
	render := func() []*Output {
		p := defaultProfile(t)
		p.Terms = []string{"Acmeco"}
		e, err := New(testKey(t), p, wordDetector{names: []string{"Priya"}}, newMemRegistry())
		if err != nil {
			t.Fatal(err)
		}
		return run(t, e, dmarcFromProvider, termInDisplayName)
	}
	outs := render()
	dmarc, team := string(outs[0].Data), string(outs[1].Data)

	// The automated sender stays as it is; "Report" is not a surname.
	if !strings.Contains(dmarc, `From: "DMARC Report" <dmarcreport@microsoft.com>`) {
		t.Errorf("automated sender changed:\n%s", dmarc)
	}
	root, _ := mimetree.Parse(outs[0].Data)
	subject := mimetree.DecodeHeader(root.Header.Get("Subject"))
	if !strings.HasPrefix(subject, "Report Domain: ") || !strings.Contains(subject, ".example Submitter: protection.outlook.com") {
		t.Errorf("subject = %q", subject)
	}

	// The term gets the company fake everywhere, the domain its fake domain,
	// even where a display name also made "Acmeco" a first name.
	e, _ := New(testKey(t), defaultProfile(t), wordDetector{}, newMemRegistry())
	company := e.companyName(companyKey("acmeco"))
	stem, domain := synth.CompanyStem(company), synth.CompanyFor(company).Domain
	for _, out := range []string{dmarc, team} {
		if strings.Contains(strings.ToLower(out), "acmeco") {
			t.Errorf("acmeco survived:\n%s", out)
		}
	}
	if !strings.Contains(dmarc, "Report for "+domain+" from "+stem+".") {
		t.Errorf("body should name %s and %s:\n%s", domain, stem, dmarc)
	}
	if !strings.Contains(team, "the "+stem+" report is ready") || !strings.Contains(team, "Subject: "+stem+" weekly") {
		t.Errorf("term not replaced by the company %q:\n%s", stem, team)
	}

	// The same input gives the same output, whatever the map order.
	for i := 0; i < 5; i++ {
		again := render()
		if string(again[0].Data) != dmarc || string(again[1].Data) != team {
			t.Fatal("output differs between runs")
		}
	}
}

func TestKeptLocalPartsAreScrubbed(t *testing.T) {
	msg := "From: Alerts <acmeco-alerts@acmeco.com.au>\r\nTo: Priya Shah <priya.shah@acmeco.com.au>\r\n" +
		"Subject: Report Domain: acmeco.systems Report-ID: acmeco.systems-1793025610@mail.reporter.example\r\n\r\nSee report.\r\n"
	p := defaultProfile(t)
	p.Terms = []string{"KLIX"}
	e, err := New(testKey(t), p, wordDetector{names: []string{"Priya"}}, newMemRegistry())
	if err != nil {
		t.Fatal(err)
	}
	out := string(run(t, e, msg)[0].Data)
	if strings.Contains(strings.ToLower(out), "acmeco") {
		t.Errorf("company survived in a kept local part:\n%s", out)
	}
	fake := companyKey(e.fakeCompany("acmeco"))
	if !strings.Contains(out, fake+"-alerts@") || !strings.Contains(out, "Report-ID: "+fake+".systems-1793025610@") {
		t.Errorf("kept local parts lost their shape (want %s-alerts@, %s.systems-…):\n%s", fake, fake, out)
	}
	if got := e.scrubLocal("klix-noreply"); got != companyKey(e.fakeCompany("KLIX"))+"-noreply" {
		t.Errorf("term in local part = %q", got)
	}
}

// partHeaderMail hides personal data where Strix found the anonymiser did
// not look: custom part headers (also on a nested multipart), a
// Content-Location URL, and the multipart preamble and epilogue.
const partHeaderMail = "From: Ann Lee <ann.lee@acme.com.au>\r\nTo: Bo Ray <bo.ray@other.com.au>\r\nSubject: parts\r\n" +
	"MIME-Version: 1.0\r\nContent-Type: multipart/mixed; boundary=\"OUT\"\r\n\r\n" +
	"Mailing list for cy.dean@third.com.au\r\n" +
	"--OUT\r\nContent-Type: multipart/related; boundary=\"IN\"\r\nX-Owner: dee.fox@fourth.com.au\r\n\r\n" +
	"--IN\r\nContent-Type: text/plain; charset=utf-8\r\nX-Zimbra-Custom: owner eve.gray@fifth.com.au\r\nComments: from Ann\r\n\r\nbody\r\n" +
	"--IN\r\nContent-Type: image/png\r\nContent-Location: https://intranet.acme.com.au/staff/ann-lee.png\r\nContent-Transfer-Encoding: base64\r\n\r\niVBORw0KGgo=\r\n" +
	"--IN\r\nContent-Type: image/png\r\nContent-Location: ann-lee-photo.png\r\nContent-Transfer-Encoding: base64\r\n\r\niVBORw0KGgo=\r\n" +
	"--IN--\r\n--OUT--\r\nAfter: also cc gus.hill@sixth.com.au\r\n"

func TestPartHeadersPreambleAndEpilogue(t *testing.T) {
	e, err := New(testKey(t), defaultProfile(t), wordDetector{names: []string{"Ann", "Ann Lee"}}, newMemRegistry())
	if err != nil {
		t.Fatal(err)
	}
	out := run(t, e, partHeaderMail)[0]
	all := strings.ToLower(string(out.Data))
	for _, s := range []string{"cy.dean", "dee.fox", "eve.gray", "gus.hill", "acme.com.au", "third.com.au", "ann-lee", "ann lee", "comments: from ann"} {
		if strings.Contains(all, s) {
			t.Errorf("%q survived:\n%s", s, out.Data)
		}
	}
	dropped := strings.Join(out.DroppedHeaders, ",")
	for _, h := range []string{"X-Owner", "X-Zimbra-Custom", "Comments"} {
		if !strings.Contains(dropped, h) {
			t.Errorf("%s not dropped: %v", h, out.DroppedHeaders)
		}
	}
	root, _ := mimetree.Parse(out.Data)
	related := root.Parts[0]
	if loc := related.Parts[1].Header.Get("Content-Location"); !strings.HasPrefix(loc, "https://") || !strings.Contains(loc, ".example/") {
		t.Errorf("absolute Content-Location = %q", loc)
	}
	if loc := related.Parts[2].Header.Get("Content-Location"); !strings.HasPrefix(loc, "part-") || !strings.HasSuffix(loc, ".png") {
		t.Errorf("relative Content-Location = %q", loc)
	}
	if !strings.HasPrefix(string(root.Preamble), "Mailing list for ") || !strings.HasPrefix(string(root.Epilogue), "After: also cc ") {
		t.Errorf("preamble %q, epilogue %q", root.Preamble, root.Epilogue)
	}
}

func TestHostileNestingIsRefused(t *testing.T) {
	var b strings.Builder
	b.WriteString("From: a@b.com.au\r\nTo: c@d.com.au\r\nContent-Type: multipart/mixed; boundary=q0\r\n\r\n")
	for i := 0; i < 500; i++ {
		fmt.Fprintf(&b, "--q%d\r\nContent-Type: multipart/mixed; boundary=q%d\r\n\r\n", i, i+1)
	}
	e := newEngine(t, newMemRegistry())
	if _, err := e.Anonymise(ctx, eml(b.String())); err == nil || !strings.Contains(err.Error(), "levels deep") {
		t.Errorf("err = %v", err)
	}
}

// A server logs what failed when Presidio answers with an error, not what
// it said, which could quote the text it was sent; locally both are shown.
func TestDetectorFailure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "cannot parse: Sarah Lane called", http.StatusInternalServerError)
	}))
	defer srv.Close()
	_, err := NewPresidio(srv.URL).Detect(ctx, "Sarah Lane called", nil, 0.5)
	if err == nil || !strings.Contains(err.Error(), "Sarah Lane") {
		t.Fatalf("err = %v", err)
	}
	if got := DetectorFailure(err); got != "presidio returned 500 Internal Server Error" {
		t.Errorf("DetectorFailure = %q", got)
	}
	other := errors.New("calling Presidio: dial tcp: connection refused")
	if got := DetectorFailure(other); got != other.Error() {
		t.Errorf("DetectorFailure = %q", got)
	}
}

// A number after a phone label is a phone number in any country's format;
// one too short to be one, or a word that only starts like a label, is not.
func TestLabelledPhones(t *testing.T) {
	for text, want := range map[string]string{
		"Tel: 0803 424 8179 (office)":       "0803 424 8179",
		"Mobile 07064 794884":               "07064 794884",
		"Phone (08) 9123 4567, ask for Jo":  "(08) 9123 4567",
		"phone number: +44 20 7946 0000":    "+44 20 7946 0000",
		"Fax. 653-242-050":                  "653-242-050",
		"Ph 390001234":                      "390001234",
		"Phone ext 42":                      "",
		"Mobile phones sold in 2025: 12345": "",
		"Telstra account 1234567":           "",
		"Mobile 0412 345 678 2 units":       "0412 345 678",
		"Direct debit 1234567":              "",
		"Paid by direct 1234567":            "",
		"Direct: 9123 4567":                 "9123 4567",
		"Direct line 9123 4567":             "9123 4567",
	} {
		got := ""
		for _, sp := range patternSpans(text) {
			if sp.Entity == EntityPhone {
				got = text[sp.Start:sp.End]
			}
		}
		if got != want {
			t.Errorf("%q: phone %q, want %q", text, got, want)
		}
	}
}

// capitalsDetector finds names only as Presidio's model mostly does:
// written in title case. It records what it was sent, and adds malformed
// spans when bad is set.
type capitalsDetector struct {
	names []string
	sent  *[]string
	bad   bool
}

func (d capitalsDetector) Detect(_ context.Context, text string, _ []string, _ float64) ([]Span, error) {
	if d.sent != nil {
		*d.sent = append(*d.sent, text)
	}
	var spans []Span
	if d.bad {
		spans = append(spans, Span{Start: 5, End: 2, Entity: EntityPerson}, Span{Start: -1, End: 3, Entity: EntityPerson},
			Span{Start: len(text) - 1, End: len(text) + 10, Entity: EntityPerson})
	}
	for _, n := range d.names {
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

// A name written in capitals is read again title-cased, and replaced as it
// was written.
func TestNamesInCapitals(t *testing.T) {
	for in, want := range map[string]string{
		"PETRA VANCLIFF and Rui TALLOW, ABN": "Petra Vancliff and Rui Tallow, ABN",
		"TAX INVOICE":                        "Tax Invoice",
		"no capitals here":                   "no capitals here",
		"KİRAZ ÖZTÜRK":                       "KİRAZ Öztürk", // İ's lower case is longer: left as it is
	} {
		if got, _ := titleCaps(in); got != want {
			t.Errorf("titleCaps(%q) = %q, want %q", in, got, want)
		}
	}
	e, err := New(testKey(t), defaultProfile(t), capitalsDetector{names: []string{"Petra Vancliff"}}, newMemRegistry())
	if err != nil {
		t.Fatal(err)
	}
	out, err := e.Anonymise(ctx, File{Name: "note.txt", Data: []byte("ORCHARD MUTUAL PETRA VANCLIFF 0412 345 678\nTAX INVOICE\n")})
	if err != nil {
		t.Fatal(err)
	}
	got := string(out.Data)
	if strings.Contains(got, "PETRA") || strings.Contains(got, "VANCLIFF") || !strings.Contains(got, "TAX INVOICE") || !strings.Contains(got, "ORCHARD MUTUAL ") {
		t.Errorf("out = %q", got)
	}
	if fake := strings.Fields(strings.TrimPrefix(got, "ORCHARD MUTUAL "))[0]; strings.ToUpper(fake) != fake {
		t.Errorf("the fake %q is not in capitals, as the name was", fake)
	}
}

// The second read gets only the lines with capitals, cut to some context
// either side, and its spans map back to the text; one that is malformed
// or crosses two parts is dropped.
func TestCapsParts(t *testing.T) {
	long := strings.Repeat("plain words here ", 30)
	text := "Intro line\n" + long + "PETRA VANCLIFF " + long + "\nmiddle line\nKİRAZ ÖZTÜRK and TOM BAKER\nlast"
	c, ok := capsParts(text)
	if !ok {
		t.Fatal("no parts")
	}
	if strings.Contains(c.text, "Intro") || strings.Contains(c.text, "middle") || strings.Contains(c.text, "last") {
		t.Errorf("sent lines with no capitals: %q", c.text)
	}
	if len(c.text) > 2*capsContext+len("Petra Vancliff ")+len("\n\nKİRAZ Öztürk and Tom Baker") {
		t.Errorf("sent %d bytes", len(c.text))
	}
	for _, name := range []string{"Petra Vancliff", "Tom Baker"} {
		i := strings.Index(c.text, name)
		m, ok := c.at(Span{Start: i, End: i + len(name), Entity: EntityPerson})
		if !ok || !strings.EqualFold(text[m.Start:m.End], name) {
			t.Errorf("%s maps to %+v", name, m)
		}
	}
	gap := strings.Index(c.text, "\n\n")
	for _, s := range []Span{{Start: 5, End: 2}, {Start: -1, End: 3}, {Start: gap - 2, End: gap + 4}, {Start: 0, End: len(c.text) + 1}} {
		if m, ok := c.at(s); ok {
			t.Errorf("at(%+v) = %+v", s, m)
		}
	}
	if _, ok := capsParts("no capitals here, Only Title Case"); ok {
		t.Error("parts for text with no capitals")
	}

	// Through the engine: malformed spans are dropped, a lone word in
	// capitals ("BILL" of "BILL TO") is no name, and only the capitals
	// line is sent again.
	var sent []string
	e, err := New(testKey(t), defaultProfile(t), capitalsDetector{names: []string{"Petra Vancliff", "Bill"}, sent: &sent, bad: true}, newMemRegistry())
	if err != nil {
		t.Fatal(err)
	}
	out, err := e.Anonymise(ctx, File{Name: "note.txt", Data: []byte("Dear all,\nBILL TO\nPETRA VANCLIFF\nthanks\n")})
	if err != nil {
		t.Fatal(err)
	}
	if got := string(out.Data); strings.Contains(got, "VANCLIFF") || !strings.Contains(got, "BILL TO") {
		t.Errorf("out = %q", got)
	}
	if len(sent) != 2 || sent[1] != "Bill To\n\nPetra Vancliff" {
		t.Errorf("sent = %q", sent)
	}
}
