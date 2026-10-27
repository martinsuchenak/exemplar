package anonymise

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"net/mail"
	"regexp"
	"strings"
	"testing"

	"github.com/martinsuchenak/exemplar/internal/mimetree"
)

// markerRe finds the slot markers below: each is a unique address on a
// domain the corpus uses nowhere else, so any trace of one is a leak.
var markerRe = regexp.MustCompile(`(?i)xx-[a-z-]+-\d{4}|acme`)

// partSlots is Strix's slot corpus for vuln-0008: one marker in each place a
// MIME part header can carry a value.
var partSlots = []string{
	"Content-Language: xx-lang-4411@acme.com.au",
	"Content-Features: xx-feat-4412@acme.com.au",
	"Content-MD5: xx-md5-4413@acme.com.au",
	"MIME-Version: 1.0 (xx-mv-4414@acme.com.au)",
	"Content-Transfer-Encoding: 7bit (xx-cte-4415@acme.com.au)",
	"Content-Type: application/pdf (xx-ctcm-4416@acme.com.au)",
	`Content-Type: application/pdf; x-px-4417="xx-ctpx-4417@acme.com.au"`,
	`Content-Disposition: attachment; modification-date="xx-cdmd-4418@acme.com.au"`,
	`Content-Disposition: attachment; x-p="xx-cdxp-4419@acme.com.au"`,
	"Content-Disposition: xx-cdraw-4420@acme.com.au",
	"Content-ID: xx-cidraw-4421@acme.com.au",
	"Content-Description: xx-desc-4422@acme.com.au",
	`Content-Type: text/plain; name="xx-ctname-4423@acme.com.au.txt"`,
	`Content-Disposition: attachment; filename="xx-cdfn-4424@acme.com.au.pdf"`,
	"Content-Location: xx-loc-rel-4425@acme.com.au.png",
	"Content-Location: http://xx-loc-abs-4426@acme.com.au/x.png",
}

// messageSlots put a marker in every header a message keeps, in the value,
// a comment or an extra word, and repeat the structural ones.
var messageSlots = []string{
	"To: xx-to-5001@acme.com.au",
	"Cc: Ann (xx-cc-5002@acme.com.au) <ann@other.example>",
	"Bcc: broken <<xx-bcc-5003@acme.com.au",
	"Reply-To: xx-replyto-5004@acme.com.au",
	"Sender: xx-sender-5005@acme.com.au",
	"Return-Path: <xx-returnpath-5006@acme.com.au>",
	"Disposition-Notification-To: xx-dnt-5007@acme.com.au",
	"Date: Mon, 3 Feb 2025 09:15:00 +1100 (xx-date-5008@acme.com.au)",
	"Message-ID: <m1@sender.example> (xx-mid-5009@acme.com.au)",
	"In-Reply-To: xx-irt-5010@acme.com.au <m0@sender.example>",
	"References: <m0@sender.example> xx-refs-5011@acme.com.au",
	"Thread-Topic: about xx-topic-5012@acme.com.au",
	"Thread-Index: xx-tidx-5013@acme.com.au",
	"Auto-Submitted: auto-replied; owner=xx-autosub-5014@acme.com.au",
	"X-Autoreply: xx-autoreply-5015@acme.com.au",
	"X-Autorespond: xx-autorespond-5016@acme.com.au",
	"X-Auto-Response-Suppress: All (xx-arsupp-5017@acme.com.au)",
	"Precedence: bulk (xx-prec-5018@acme.com.au)",
	"List-Id: xx-listid-5019@acme.com.au <list.sender.example>",
	"List-Unsubscribe: <https://sender.example/u>, xx-unsub-5020@acme.com.au",
	"List-Unsubscribe-Post: List-Unsubscribe=One-Click xx-unsubpost-5021@acme.com.au",
	"List-Post: <mailto:xx-listpost-5022@acme.com.au>",
	"List-Help: <https://xx-listhelp-5023.acme.com.au/help>",
	"Importance: high (xx-imp-5024@acme.com.au)",
	"X-Priority: 3 (xx-prio-5025@acme.com.au)",
	// Not "mailer": a local part with an automated marker is kept by design.
	"X-Mailer: Mailer for xx-xm-5026@acme.com.au",
	"User-Agent: Agent xx-ua-5027@acme.com.au",
	"Accept-Language: en-AU, xx-acceptlang-5028@acme.com.au",
	"X-MS-Has-Attach: yes xx-hasattach-5029@acme.com.au",
	"X-MS-TNEF-Correlator: xx-tnef-5030@acme.com.au",
	"Content-Language: en (xx-msglang-5031@acme.com.au)",
	"Content-Description: xx-msgdesc-5032@acme.com.au",
	"MIME-Version: 1.0",
	"MIME-Version: 1.0 (xx-msgmv-5033@acme.com.au)",
	"Subject: Report",
	"Subject: xx-subject2-5034@acme.com.au",
}

// slotMessage builds a multipart message whose parts each carry one slot,
// once after the part's Content-Type (a duplicate, when the slot is a
// Content-Type) and once before it (the slot wins the parse).
func slotMessage(header string, slots []string) string {
	var b strings.Builder
	b.WriteString("From: \"Ops\" <ops@sender.example>\r\n" + header +
		"Subject: slots\r\nMIME-Version: 1.0\r\nContent-Type: multipart/mixed; boundary=IB3\r\n\r\n")
	for _, slot := range slots {
		fmt.Fprintf(&b, "--IB3\r\nContent-Type: text/plain\r\n%s\r\n\r\nbody\r\n", slot)
		fmt.Fprintf(&b, "--IB3\r\n%s\r\nContent-Type: text/plain\r\n\r\nbody\r\n", slot)
	}
	// A boundary is the sender's text too.
	b.WriteString("--IB3\r\nContent-Type: multipart/alternative; boundary=\"xx-bnd-4427@acme.com.au\"\r\n\r\n" +
		"--xx-bnd-4427@acme.com.au\r\nContent-Type: text/plain\r\n\r\ninner\r\n--xx-bnd-4427@acme.com.au--\r\n")
	b.WriteString("--IB3--\r\n")
	return b.String()
}

// leaks returns every marker in data, headers decoded.
func leaks(t *testing.T, data []byte) []string {
	t.Helper()
	return markerRe.FindAllString(string(data)+decodedText(t, data), -1)
}

func TestPartHeaderSlotsLeakNothing(t *testing.T) {
	msg := slotMessage("To: Bo <bo@other.example>\r\n", partSlots)
	out := run(t, newEngine(t, newMemRegistry()), msg)[0]
	if found := leaks(t, out.Data); len(found) > 0 {
		t.Fatalf("markers survived: %v\n%s", found, out.Data)
	}
	root, err := mimetree.Parse(out.Data)
	if err != nil {
		t.Fatal(err)
	}
	if len(root.Parts) != 2*len(partSlots)+1 {
		t.Errorf("parts = %d, want %d", len(root.Parts), 2*len(partSlots)+1)
	}
	if inner := root.Parts[len(root.Parts)-1]; len(inner.Parts) != 1 || !strings.HasPrefix(inner.Boundary, "=_") {
		t.Errorf("nested multipart: %d parts, boundary %q", len(inner.Parts), inner.Boundary)
	}
	dropped := strings.Join(out.DroppedHeaders, ",")
	for _, h := range []string{"Content-Features", "Content-MD5", "Content-Type"} {
		if !strings.Contains(dropped, h) {
			t.Errorf("%s not dropped (duplicate or unchecked): %v", h, out.DroppedHeaders)
		}
	}
}

func TestMessageHeaderSlotsLeakNothing(t *testing.T) {
	msg := slotMessage(strings.Join(messageSlots, "\r\n")+"\r\n", []string{"Content-Description: part"})
	out := run(t, newEngine(t, newMemRegistry()), msg)[0]
	if found := leaks(t, out.Data); len(found) > 0 {
		t.Fatalf("markers survived: %v\n%s", found, out.Data)
	}
	if _, err := mail.ReadMessage(bytes.NewReader(out.Data)); err != nil {
		t.Fatal(err)
	}

	// The same headers on an attached email.
	attached := "From: a@sender.example\r\nTo: b@other.example\r\nSubject: fwd\r\nMIME-Version: 1.0\r\n" +
		"Content-Type: multipart/mixed; boundary=W\r\n\r\n--W\r\nContent-Type: text/plain\r\n\r\nsee attached\r\n" +
		"--W\r\nContent-Type: message/rfc822\r\n\r\n" + msg + "\r\n--W--\r\n"
	out = run(t, newEngine(t, newMemRegistry()), attached)[0]
	if found := leaks(t, out.Data); len(found) > 0 {
		t.Fatalf("markers survived in the attached email: %v\n%s", found, out.Data)
	}
}

// TestKeptHeadersKeepTheirMeaning checks that clean rebuilds well-formed
// values rather than dropping them: classifiers read these.
func TestKeptHeadersKeepTheirMeaning(t *testing.T) {
	msg := "From: Ops <ops@sender.example>\r\nTo: Bo <bo@other.example>\r\nSubject: s\r\n" +
		"Date: Mon, 3 Feb 2025 09:15:00 +1100 (AEDT)\r\nAuto-Submitted: auto-replied\r\nPrecedence: bulk\r\n" +
		"Importance: High\r\nX-Priority: 3 (Normal)\r\nX-Auto-Response-Suppress: DR, OOF, AutoReply\r\n" +
		"X-Autoreply: yes\r\nAccept-Language: en-AU, en;q=0.8\r\nContent-Language: en-AU\r\nX-MS-Has-Attach:\r\n" +
		"X-MS-TNEF-Correlator:\r\nList-Unsubscribe-Post: List-Unsubscribe=One-Click\r\nList-Post: NO\r\n" +
		"Return-Path: <>\r\nMIME-Version: 1.0\r\n" +
		"Content-Type: multipart/related; type=\"text/html\"; start=\"<root@sender.example>\"; boundary=B\r\n\r\n" +
		"--B\r\nContent-Type: text/html; charset=\"iso-8859-1\"\r\nContent-ID: <root@sender.example>\r\n" +
		"Content-Transfer-Encoding: Quoted-Printable\r\n\r\n<p>hi</p>\r\n" +
		"--B\r\nContent-Type: text/calendar; method=REQUEST; charset=utf-8\r\n\r\nBEGIN:VCALENDAR\r\nEND:VCALENDAR\r\n" +
		"--B\r\nContent-Type: text/plain; format=flowed; delsp=yes\r\nContent-Disposition: inline\r\n\r\nflowed\r\n--B--\r\n"
	out := run(t, newEngine(t, newMemRegistry()), msg)[0]
	root, err := mimetree.Parse(out.Data)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		"Date": "Mon, 3 Feb 2025 09:15:00 +1100", "Auto-Submitted": "auto-replied", "Precedence": "bulk",
		"Importance": "High", "X-Priority": "3 (Normal)", "X-Auto-Response-Suppress": "DR, OOF, AutoReply",
		"X-Autoreply": "yes", "Accept-Language": "en-AU, en;q=0.8", "Content-Language": "en-AU",
		"List-Unsubscribe-Post": "List-Unsubscribe=One-Click", "List-Post": "NO", "Return-Path": "<>",
	}
	for h, v := range want {
		if got := root.Header.Get(h); got != v {
			t.Errorf("%s = %q, want %q", h, got, v)
		}
	}
	for _, h := range []string{"X-MS-Has-Attach", "X-MS-TNEF-Correlator"} {
		if !hasField(root, h) {
			t.Errorf("empty %s dropped", h)
		}
	}
	mt, params := root.MediaType()
	cid := root.Parts[0].Header.Get("Content-ID")
	if mt != "multipart/related" || params["type"] != "text/html" || params["start"] != cid || cid == "<root@sender.example>" {
		t.Errorf("related: %s %v, root part %q", mt, params, cid)
	}
	if _, p := root.Parts[0].MediaType(); p["charset"] == "" || root.Parts[0].Encoding() != "quoted-printable" {
		t.Errorf("html part: %v, %s", p, root.Parts[0].Encoding())
	}
	if _, p := root.Parts[1].MediaType(); p["method"] != "REQUEST" {
		t.Errorf("calendar: %v", p)
	}
	if _, p := root.Parts[2].MediaType(); p["format"] != "flowed" || p["delsp"] != "yes" || root.Parts[2].Header.Get("Content-Disposition") != "inline" {
		t.Errorf("flowed: %v", root.Parts[2].Header.Fields)
	}
	if len(out.DroppedHeaders) != 0 {
		t.Errorf("dropped %v; every header here is well formed", out.DroppedHeaders)
	}
}

func hasField(p *mimetree.Part, name string) bool {
	for _, f := range p.Header.Fields {
		if strings.EqualFold(f.Name, name) {
			return true
		}
	}
	return false
}

func TestStructureRules(t *testing.T) {
	e := newEngine(t, newMemRegistry())
	leaf := &mimetree.Part{}
	tests := []struct {
		header, value, want string
		ok                  bool
	}{
		{"content-type", "Text/Plain; charset=utf-8; x-owner=ann", "text/plain; charset=utf-8", true},
		{"content-type", "application/pdf (comment)", "application/pdf", true},
		{"content-type", "garbage", "", false},
		{"content-type", `text/plain; charset="utf-8 (x)"`, "text/plain", true},
		{"content-type", `image/png; name="a.png"`, "image/png; name=a.png", true},
		{"content-type", "text/calendar; method=STEAL", "text/calendar", true},
		{"content-disposition", `attachment; filename="a b.pdf"; size=10; creation-date="x"`, `attachment; filename="a b.pdf"`, true},
		{"content-disposition", "INLINE", "inline", true},
		{"content-disposition", "attachment (owner)", "attachment", true},
		{"content-disposition", "form-data", "", false},
		{"content-transfer-encoding", "BASE64", "BASE64", true},
		{"content-transfer-encoding", "x-uuencode", "", false},
		{"content-language", "en-AU, de", "en-AU, de", true},
		{"content-language", "en;q=0.5", "", false},
		{"accept-language", "*, fr;q=0.1", "*, fr;q=0.1", true},
		{"accept-language", "en;q=high", "", false},
		{"mime-version", "1.0 (x)", "1.0", true},
		{"date", "not a date", "", false},
		{"date", "3 Feb 2025 09:15 GMT", "Mon, 3 Feb 2025 09:15:00 +0000", true},
		{"auto-submitted", "AUTO-GENERATED (x)", "AUTO-GENERATED", true},
		{"auto-submitted", "sure", "", false},
		{"x-ms-has-attach", "", "", true},
		{"x-ms-has-attach", "maybe", "", false},
		{"x-priority", "1", "1", true},
		{"x-priority", "3 (Ann)", "", false},
		{"x-autorespond", "anything", "yes", true},
		{"x-ms-tnef-correlator", "<x@y>", "", false},
		{"list-unsubscribe-post", "List-Unsubscribe=One-Click", "List-Unsubscribe=One-Click", true},
		{"thread-index", "AQHZ", "", false},
	}
	for _, tt := range tests {
		got, ok := structureRules[tt.header](e, leaf, tt.value)
		if ok != tt.ok || ok && got != tt.want {
			t.Errorf("%s: %q = %q %v, want %q %v", tt.header, tt.value, got, ok, tt.want, tt.ok)
		}
	}
}

func TestCleanValueByKind(t *testing.T) {
	e := newEngine(t, newMemRegistry())
	tests := []struct {
		kind      headerKind
		value     string
		want      string
		ok        bool
		header    string
		multipart bool
	}{
		{kind: kindID, value: "<a@b> (Ann) <c@d>", want: "<a@b> <c@d>", ok: true},
		{kind: kindID, value: "a@b", ok: false},
		{kind: kindContentID, value: "<a@b> (Ann)", want: "<a@b>", ok: true},
		{kind: kindContentID, value: "a@b", want: "<a@b>", ok: true},
		{kind: kindContentID, value: "a b", ok: false},
		{kind: kindURLs, value: "<https://x.example/u> (Ann), <mailto:u@x.example>", want: "<https://x.example/u>, <mailto:u@x.example>", ok: true},
		{kind: kindURLs, value: "mailto:u@x.example", ok: false},
		{kind: kindURLs, value: "no", want: "NO", ok: true},
		{kind: kindLocation, value: "<a.png> (Ann)", want: "a.png", ok: true},
		{kind: kindLocation, value: " ", ok: false},
		{kind: kindText, value: "free text", want: "free text", ok: true},
	}
	for _, tt := range tests {
		got, ok := e.cleanValue(&mimetree.Part{}, tt.kind, tt.header, tt.value)
		if ok != tt.ok || ok && got != tt.want {
			t.Errorf("kind %d: %q = %q %v, want %q %v", tt.kind, tt.value, got, ok, tt.want, tt.ok)
		}
	}
}

func TestFakeThreadIndexKeepsConversations(t *testing.T) {
	e := newEngine(t, newMemRegistry())
	enc := base64.StdEncoding.EncodeToString
	// A conversation's first email, a reply (5 more bytes) and another
	// conversation. The header bytes spell an address, as a hostile one could.
	head := []byte("\x01\xd9ann.lee@acme.com.au---")
	firstRaw := head[:22]
	first, reply := enc(firstRaw), enc(append(append([]byte{}, firstRaw...), "reply"...))
	other := enc(append([]byte("\x02"), firstRaw[1:]...))
	ff, ok1 := e.fakeThreadIndex(first)
	fr, ok2 := e.fakeThreadIndex(reply)
	fo, ok3 := e.fakeThreadIndex(other)
	if !ok1 || !ok2 || !ok3 {
		t.Fatalf("not faked: %v %v %v", ok1, ok2, ok3)
	}
	if len(ff) != len(first) || len(fr) != len(reply) || ff == first {
		t.Errorf("fakes %q %q", ff, fr)
	}
	// The first 21 bytes are the first 28 base64 characters.
	if fr[:28] != ff[:28] || fo[:28] == ff[:28] {
		t.Errorf("conversation prefix not kept: %q %q %q", ff, fr, fo)
	}
	for _, f := range []string{ff, fr, fo} {
		if raw, _ := base64.StdEncoding.DecodeString(f); bytes.Contains(raw, []byte("acme")) {
			t.Errorf("original bytes survived in %q", f)
		}
	}
	if again, _ := e.fakeThreadIndex(first); again != ff {
		t.Error("not deterministic")
	}
	for _, bad := range []string{enc(firstRaw[:21]), enc(append(append([]byte{}, firstRaw...), "rep"...)), "not base64!"} {
		if _, ok := e.fakeThreadIndex(bad); ok {
			t.Errorf("%q kept", bad)
		}
	}
}

func TestPrepareFailsPerEmail(t *testing.T) {
	var deep strings.Builder
	deep.WriteString("From: a@b.com.au\r\nContent-Type: multipart/mixed; boundary=q0\r\n\r\n")
	for i := 0; i < 50; i++ {
		fmt.Fprintf(&deep, "--q%d\r\nContent-Type: multipart/mixed; boundary=q%d\r\n\r\n", i, i+1)
	}
	e := newEngine(t, newMemRegistry())
	errs := e.Prepare(ctx, [][]byte{[]byte(original), []byte(deep.String()), []byte(reply)})
	if len(errs) != 3 || errs[0] != nil || errs[2] != nil || errs[1] == nil || !strings.Contains(errs[1].Error(), "levels deep") {
		t.Fatalf("errs = %v", errs)
	}
	// The good emails were still read: the reply's body names Sarah, known
	// only from the first email's From.
	out, err := e.Anonymise(ctx, []byte(reply))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(decodedText(t, out.Data), "Sarah") {
		t.Error("the batch was not prepared past the bad email")
	}

	reg := &failingRegistry{memRegistry: newMemRegistry(), err: errors.New("registry down")}
	e, err = New(testKey(t), defaultProfile(t), wordDetector{}, reg)
	if err != nil {
		t.Fatal(err)
	}
	if errs := e.Prepare(ctx, [][]byte{[]byte(original)}); errs[0] == nil || !strings.Contains(errs[0].Error(), "registry down") {
		t.Errorf("registry error = %v", errs)
	}
}

// failingRegistry fails every person lookup.
type failingRegistry struct {
	*memRegistry
	err error
}

func (f *failingRegistry) Token(context.Context, string, string) (string, error) { return "", f.err }
