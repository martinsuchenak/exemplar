package synth

import (
	"archive/zip"
	"bytes"
	"encoding/base64"
	"encoding/csv"
	"encoding/json"
	"encoding/xml"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"mime"
	"mime/multipart"
	"mime/quotedprintable"
	"net/mail"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// people returns n registry-like people with IDs.
func people(g *Gen, n int) []Person {
	company := g.CompanyName()
	out := make([]Person, n)
	for i := range out {
		if i%2 == 1 {
			company = g.CompanyName()
		}
		out[i] = g.Person(company)
		out[i].ID = "p" + strconv.Itoa(i+1)
	}
	return out
}

func TestSameSeedSameOutput(t *testing.T) {
	gen := func() ([]byte, []byte, []byte) {
		g := New(42)
		ps := people(g, 4)
		f, err := g.File("pdf", FileInput{Profile: ProfileInsurance, People: ps})
		if err != nil {
			t.Fatal(err)
		}
		e, err := g.Emails(EmailInput{Profile: ProfileManufacturing, Shape: ShapeAttachment, People: ps})
		if err != nil {
			t.Fatal(err)
		}
		r, err := g.Record(SchemaCompany, RecordInput{People: ps}, 1)
		if err != nil {
			t.Fatal(err)
		}
		return f.Data, e[0].Data, r.Data
	}
	a1, b1, c1 := gen()
	a2, b2, c2 := gen()
	if !bytes.Equal(a1, a2) || !bytes.Equal(b1, b2) || !bytes.Equal(c1, c2) {
		t.Error("the same seed produced different output")
	}
	g := New(43)
	f, _ := g.File("pdf", FileInput{People: people(g, 4)})
	if bytes.Equal(f.Data, a1) {
		t.Error("a different seed produced the same PDF")
	}
}

func TestPeopleAndCompanies(t *testing.T) {
	g := New(7)
	name := g.CompanyName()
	c1, c2 := CompanyFor(name), CompanyFor(name)
	if c1 != c2 {
		t.Errorf("CompanyFor is not stable: %+v vs %+v", c1, c2)
	}
	if !strings.HasSuffix(c1.Domain, ".example") || !strings.HasPrefix(c1.Website, "https://www.") || c1.ABN == "" || c1.Address.Postcode == "" {
		t.Errorf("company = %+v", c1)
	}
	p := g.Person(name)
	if !strings.HasSuffix(p.Email, "@"+c1.Domain) || p.Company != name || !strings.HasPrefix(p.Phone, "04") {
		t.Errorf("person = %+v", p)
	}
	if p.Name() != p.FirstName+" "+p.LastName || p.Address.String() == "" {
		t.Errorf("name/address = %q / %q", p.Name(), p.Address.String())
	}
	if emailLocal("Te Rangi", "O'Brien-Smith") != "terangi.obriensmith" {
		t.Errorf("emailLocal = %q", emailLocal("Te Rangi", "O'Brien-Smith"))
	}
	for in, want := range map[string]string{"Harlow & Co Pty Ltd": "harlow-co-pty-ltd", "  ": "item", strings.Repeat("ab ", 30): "ab-ab-ab-ab-ab-ab-ab-ab-ab-ab-ab-ab-ab-ab-ab-ab"} {
		if got := Slug(in); got != want {
			t.Errorf("Slug(%q) = %q, want %q", in, got, want)
		}
	}
	if d := CompanyFor("Eucalypt Medical Co Pty Ltd").Domain; d != "eucalypt-medical.example" {
		t.Errorf("domain = %s, want the longest suffix trimmed", d)
	}
	if trimSuffix("No Suffix Here") != "No Suffix Here" {
		t.Error("trimSuffix changed a name without a suffix")
	}
	if CompanyFor("Unknown Name").Industry != "General" {
		t.Error("a name without a trade should have industry General")
	}
}

func TestTextHelpers(t *testing.T) {
	g := New(1)
	for _, p := range Profiles {
		if !p.Valid() || g.Sentence(p) == "" || g.Title(p) == "" || g.Label(p) == "" || g.Subject(p) == "" || g.Closing(p) == "" {
			t.Errorf("profile %s produced empty text", p)
		}
		if op := g.Opening(p); !strings.Contains(op, ", ") {
			t.Errorf("opening %q has no comma", op)
		}
	}
	if Profile("legal").Valid() {
		t.Error("unknown profile is valid")
	}
	if formatMoney(123456789) != "$1,234,567.89" || formatMoney(5) != "$0.05" {
		t.Errorf("formatMoney = %s, %s", formatMoney(123456789), formatMoney(5))
	}
	if !regexp.MustCompile(`^[A-Z]+-\d{6}$`).MatchString(g.Reference()) {
		t.Error("reference format")
	}
	if g.Between(5, 5) != 5 || g.Intn(0) != 0 {
		t.Error("Between/Intn edge cases")
	}
}

func TestEveryFileFormatIsValid(t *testing.T) {
	g := New(99)
	ps := people(g, 5)
	for _, format := range FileFormats() {
		t.Run(format, func(t *testing.T) {
			f, err := g.File(format, FileInput{Profile: ProfileManufacturing, People: ps})
			if err != nil {
				t.Fatal(err)
			}
			if f.Format != format || f.Title == "" || len(f.Data) == 0 {
				t.Fatalf("file = %q %q %d bytes", f.Format, f.Title, len(f.Data))
			}
			checkFile(t, format, f.Data, ps)
		})
	}
	if _, err := g.File("exe", FileInput{People: ps}); err == nil {
		t.Error("unknown format accepted")
	}
	if _, err := g.File("pdf", FileInput{}); err == nil {
		t.Error("a file without people was generated")
	}
	if !IsFileFormat("pdf") || IsFileFormat("webp") {
		t.Error("IsFileFormat")
	}
}

// checkFile parses data as format the way a reader of that format would.
// With ps, it also checks the file mentions those people.
func checkFile(t *testing.T, format string, data []byte, ps []Person) {
	t.Helper()
	switch format {
	case "pdf":
		checkPDF(t, data)
	case "docx":
		parts := unzip(t, data)
		for _, name := range []string{"[Content_Types].xml", "_rels/.rels", "word/document.xml"} {
			wellFormed(t, name, parts[name])
		}
		if len(ps) > 0 && !strings.Contains(parts["word/document.xml"], ps[0].LastName) {
			t.Error("docx does not mention the author")
		}
	case "xlsx":
		parts := unzip(t, data)
		for _, name := range []string{"[Content_Types].xml", "_rels/.rels", "xl/workbook.xml", "xl/_rels/workbook.xml.rels", "xl/worksheets/sheet1.xml"} {
			wellFormed(t, name, parts[name])
		}
		if len(ps) > 0 && !strings.Contains(parts["xl/worksheets/sheet1.xml"], ps[len(ps)-1].Email) {
			t.Error("xlsx does not list every person")
		}
	case "csv":
		rows, err := csv.NewReader(bytes.NewReader(data)).ReadAll()
		if err != nil || len(ps) > 0 && (len(rows) != len(ps)+1 || rows[1][3] != ps[0].Email) {
			t.Errorf("csv rows = %d, %v", len(rows), err)
		}
	case "txt", "html":
		if len(ps) > 0 && !strings.Contains(string(data), ps[0].Name()) && !strings.Contains(string(data), ps[0].Email) {
			t.Errorf("%s does not mention the author", format)
		}
		if format == "html" && !strings.HasPrefix(string(data), "<!DOCTYPE html>") {
			t.Error("html has no doctype")
		}
	case "rtf":
		if !strings.HasPrefix(string(data), `{\rtf1`) || !strings.HasSuffix(string(data), "}") {
			t.Error("rtf is not wrapped in {\\rtf1 ... }")
		}
	case "png", "jpg", "gif":
		_, got, err := image.Decode(bytes.NewReader(data))
		want := map[string]string{"png": "png", "jpg": "jpeg", "gif": "gif"}[format]
		if err != nil || got != want {
			t.Errorf("decoded as %q, %v", got, err)
		}
	default:
		t.Fatalf("no check for %s", format)
	}
}

func checkPDF(t *testing.T, data []byte) {
	t.Helper()
	s := string(data)
	if !strings.HasPrefix(s, "%PDF-1.4") || !strings.HasSuffix(s, "%%EOF\n") {
		t.Fatal("pdf header or trailer missing")
	}
	m := regexp.MustCompile(`startxref\n(\d+)\n`).FindStringSubmatch(s)
	if m == nil {
		t.Fatal("no startxref")
	}
	xref, _ := strconv.Atoi(m[1])
	if !strings.HasPrefix(s[xref:], "xref\n") {
		t.Fatal("startxref does not point at the xref table")
	}
	// Every xref entry must point at the start of its object.
	entries := regexp.MustCompile(`(\d{10}) 00000 n`).FindAllStringSubmatch(s[xref:], -1)
	for i, e := range entries {
		off, _ := strconv.Atoi(e[1])
		if !strings.HasPrefix(s[off:], strconv.Itoa(i+1)+" 0 obj") {
			t.Errorf("xref entry %d points at %q", i+1, s[off:off+10])
		}
	}
	if len(entries) < 6 || !strings.Contains(s, "/Type /Catalog") || !strings.Contains(s, "/BaseFont /Helvetica") {
		t.Error("pdf is missing objects")
	}
}

func TestPDFPaginatesAndEscapes(t *testing.T) {
	d := document{title: "A (test) \\ title ü", people: []Person{{FirstName: "A", LastName: "B"}}}
	for i := 0; i < 80; i++ {
		d.paragraphs = append(d.paragraphs, strings.Repeat("word ", 40))
	}
	data, err := writePDF(nil, d)
	if err != nil {
		t.Fatal(err)
	}
	checkPDF(t, data)
	if n := strings.Count(string(data), "/Type /Page "); n < 3 {
		t.Errorf("pages = %d, want several", n)
	}
	if !strings.Contains(string(data), `A \(test\) \\ title ?`) {
		t.Error("title not escaped")
	}
	if got := wrap("", 10); len(got) != 1 || got[0] != "" {
		t.Errorf("wrap empty = %q", got)
	}
	if got := wrap("aaa bbb ccc", 7); len(got) != 2 {
		t.Errorf("wrap = %q", got)
	}
}

func TestSpreadsheetHelpers(t *testing.T) {
	for i, want := range map[int]string{0: "A", 25: "Z", 26: "AA", 27: "AB", 701: "ZZ", 702: "AAA"} {
		if got := columnName(i); got != want {
			t.Errorf("columnName(%d) = %s, want %s", i, got, want)
		}
	}
	if got := sheetName("Policy [Schedule]: a/b? very long title text"); len(got) > 31 || strings.ContainsAny(got, "[]:/?") {
		t.Errorf("sheetName = %q", got)
	}
	if rtfEscape(`a{b}\ü`) != `a\{b\}\\\u252?` {
		t.Errorf("rtfEscape = %q", rtfEscape(`a{b}\ü`))
	}
}

func unzip(t *testing.T, data []byte) map[string]string {
	t.Helper()
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatalf("not a zip: %v", err)
	}
	out := map[string]string{}
	for _, f := range zr.File {
		rc, err := f.Open()
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(rc)
		_ = rc.Close()
		out[f.Name] = string(b)
	}
	return out
}

func wellFormed(t *testing.T, name, doc string) {
	t.Helper()
	if doc == "" {
		t.Errorf("%s missing", name)
		return
	}
	d := xml.NewDecoder(strings.NewReader(doc))
	for {
		if _, err := d.Token(); err == io.EOF {
			return
		} else if err != nil {
			t.Errorf("%s is not well-formed XML: %v", name, err)
			return
		}
	}
}

// mimePart is a parsed MIME entity for assertions.
type mimePart struct {
	mediaType string
	params    map[string]string
	header    map[string][]string
	body      []byte
	children  []*mimePart
}

func parseMessage(t *testing.T, data []byte) (*mail.Message, *mimePart) {
	t.Helper()
	if bytes.Contains(bytes.ReplaceAll(data, []byte("\r\n"), nil), []byte("\n")) {
		t.Fatal("message has bare LF line endings")
	}
	msg, err := mail.ReadMessage(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("not a valid message: %v", err)
	}
	body, _ := io.ReadAll(msg.Body)
	return msg, parsePart(t, msg.Header, body)
}

func parsePart(t *testing.T, header map[string][]string, body []byte) *mimePart {
	t.Helper()
	ct := "text/plain"
	if v := header["Content-Type"]; len(v) > 0 {
		ct = v[0]
	}
	mt, params, err := mime.ParseMediaType(ct)
	if err != nil {
		t.Fatalf("bad Content-Type %q: %v", ct, err)
	}
	p := &mimePart{mediaType: mt, params: params, header: header}
	if strings.HasPrefix(mt, "multipart/") {
		r := multipart.NewReader(bytes.NewReader(body), params["boundary"])
		for {
			part, err := r.NextRawPart()
			if err == io.EOF {
				break
			}
			if err != nil {
				t.Fatalf("multipart: %v", err)
			}
			b, _ := io.ReadAll(part)
			p.children = append(p.children, parsePart(t, part.Header, b))
		}
		if len(p.children) == 0 {
			t.Fatalf("%s has no parts", mt)
		}
		return p
	}
	switch first(header["Content-Transfer-Encoding"]) {
	case "base64":
		dec, err := base64.StdEncoding.DecodeString(strings.ReplaceAll(string(body), "\r\n", ""))
		if err != nil {
			t.Fatalf("bad base64 in %s: %v", mt, err)
		}
		body = dec
	case "quoted-printable":
		dec, err := io.ReadAll(quotedprintable.NewReader(bytes.NewReader(body)))
		if err != nil {
			t.Fatalf("bad quoted-printable in %s: %v", mt, err)
		}
		body = dec
	}
	p.body = body
	return p
}

func first(v []string) string {
	if len(v) == 0 {
		return ""
	}
	return v[0]
}

// find returns the first part with media type mt, depth first.
func (p *mimePart) find(mt string) *mimePart {
	if p.mediaType == mt {
		return p
	}
	for _, c := range p.children {
		if f := c.find(mt); f != nil {
			return f
		}
	}
	return nil
}

func TestEveryEmailShapeParses(t *testing.T) {
	g := New(2026)
	ps := people(g, 4)
	for _, shape := range Shapes {
		t.Run(string(shape), func(t *testing.T) {
			emails, err := g.Emails(EmailInput{Profile: ProfileInsurance, Shape: shape, People: ps})
			if err != nil {
				t.Fatal(err)
			}
			for _, e := range emails {
				msg, root := parseMessage(t, e.Data)
				if msg.Header.Get("Message-ID") != e.MessageID || msg.Header.Get("MIME-Version") != "1.0" {
					t.Errorf("headers = %v", msg.Header)
				}
				if _, err := msg.Header.Date(); err != nil {
					t.Errorf("bad Date: %v", err)
				}
				if subj, err := new(mime.WordDecoder).DecodeHeader(msg.Header.Get("Subject")); err != nil || subj != e.Subject {
					t.Errorf("subject %q, want %q (%v)", subj, e.Subject, err)
				}
				for _, h := range []string{"From", "To"} {
					list, err := mail.ParseAddressList(msg.Header.Get(h))
					if err != nil || len(list) == 0 {
						t.Fatalf("%s: %v", h, err)
					}
					for _, a := range list {
						if !strings.HasSuffix(a.Address, ".example") {
							t.Errorf("%s address %s is not on a reserved domain", h, a.Address)
						}
					}
				}
				checkShape(t, shape, msg, root, e)
			}
		})
	}
	if _, err := g.Emails(EmailInput{Shape: ShapePlain, People: ps[:1]}); err == nil {
		t.Error("an email with one person was generated")
	}
	if _, err := g.Emails(EmailInput{Shape: "fax", People: ps}); err == nil {
		t.Error("unknown shape accepted")
	}
	if Shape("fax").Valid() || len(ShapeNames()) != len(Shapes) {
		t.Error("shape helpers")
	}
}

func checkShape(t *testing.T, shape Shape, msg *mail.Message, root *mimePart, e Email) {
	t.Helper()
	text := root.find("text/plain")
	switch shape {
	case ShapePlain:
		if root.mediaType != "text/plain" {
			t.Errorf("type %s", root.mediaType)
		}
	case ShapeHTML:
		if root.mediaType != "multipart/alternative" || root.find("text/html") == nil {
			t.Error("not text plus HTML")
		}
	case ShapeReply:
		if msg.Header.Get("In-Reply-To") == "" || !strings.HasPrefix(e.Subject, "Re: ") || !bytes.Contains(text.body, []byte("\r\n> ")) {
			t.Error("reply has no In-Reply-To, Re: subject or quoted text")
		}
		if root.find("text/html") == nil || !bytes.Contains(root.find("text/html").body, []byte("<blockquote>")) {
			t.Error("reply HTML has no blockquote")
		}
	case ShapeForward:
		if !strings.HasPrefix(e.Subject, "Fwd: ") || !bytes.Contains(text.body, []byte("Forwarded message")) {
			t.Error("forward has no Fwd: subject or forwarded block")
		}
	case ShapeForwardAttached:
		inner := root.find("message/rfc822")
		if inner == nil {
			t.Fatal("no attached message")
		}
		parseMessage(t, inner.body)
	case ShapeAttachment:
		if root.mediaType != "multipart/mixed" || len(root.children) != 2 {
			t.Fatalf("structure %s with %d parts", root.mediaType, len(root.children))
		}
		att := root.children[1]
		_, params, _ := mime.ParseMediaType(first(att.header["Content-Disposition"]))
		format := strings.TrimPrefix(params["filename"][strings.LastIndex(params["filename"], "."):], ".")
		checkFile(t, format, att.body, nil)
	case ShapeInlineImage:
		related := root.find("multipart/related")
		img := root.find("image/png")
		if related == nil || img == nil {
			t.Fatal("no related part or image")
		}
		cid := strings.Trim(first(img.header["Content-Id"]), "<>")
		if cid == "" || !bytes.Contains(related.find("text/html").body, []byte("cid:"+cid)) {
			t.Error("HTML does not reference the image's Content-ID")
		}
	case ShapeCalendar:
		cal := root.find("text/calendar")
		if cal == nil || cal.params["method"] != "REQUEST" || !bytes.Contains(cal.body, []byte("BEGIN:VEVENT")) || !bytes.Contains(cal.body, []byte("ATTENDEE;")) {
			t.Error("no meeting request")
		}
	case ShapeAutoReply:
		if msg.Header.Get("Auto-Submitted") != "auto-replied" || !strings.HasPrefix(e.Subject, "Automatic reply: ") {
			t.Error("auto-reply headers missing")
		}
	case ShapeBounce:
		if root.mediaType != "multipart/report" || root.params["report-type"] != "delivery-status" {
			t.Fatalf("type %s %v", root.mediaType, root.params)
		}
		if ds := root.find("message/delivery-status"); ds == nil || !bytes.Contains(ds.body, []byte("Status: 5.1.1")) {
			t.Error("no delivery status")
		}
		parseMessage(t, root.find("message/rfc822").body)
		if e.From.ID != "" || !strings.HasPrefix(e.From.Email, "mailer-daemon@") {
			t.Errorf("bounce sender = %+v", e.From)
		}
	case ShapeNewsletter:
		if msg.Header.Get("List-Unsubscribe") == "" || msg.Header.Get("List-Id") == "" || msg.Header.Get("Precedence") != "bulk" {
			t.Error("newsletter List-* headers missing")
		}
		if e.From.ID != "" {
			t.Error("newsletter sender should not be a registry person")
		}
	case ShapeThread:
		// Checked across the thread in TestThreadsLinkUp.
	}
}

func TestThreadsLinkUp(t *testing.T) {
	g := New(5)
	emails, err := g.Emails(EmailInput{Shape: ShapeThread, People: people(g, 3)})
	if err != nil {
		t.Fatal(err)
	}
	if len(emails) < 3 || len(emails) > 5 {
		t.Fatalf("thread has %d messages", len(emails))
	}
	for i := 1; i < len(emails); i++ {
		msg, _ := parseMessage(t, emails[i].Data)
		if msg.Header.Get("In-Reply-To") != emails[i-1].MessageID {
			t.Errorf("message %d does not reply to %d", i, i-1)
		}
		if refs := strings.Fields(msg.Header.Get("References")); len(refs) != i || refs[0] != emails[0].MessageID {
			t.Errorf("message %d references %v", i, refs)
		}
		if !emails[i].Date.After(emails[i-1].Date) {
			t.Errorf("message %d is not later than %d", i, i-1)
		}
		if emails[i].From.ID == emails[i-1].From.ID {
			t.Errorf("message %d has the same sender as the one before", i)
		}
	}
}

func TestRecords(t *testing.T) {
	g := New(11)
	ps := people(g, 4)
	for i, schema := range Schemas {
		r, err := g.Record(schema, RecordInput{Profile: ProfileManufacturing, People: ps}, i)
		if err != nil {
			t.Fatalf("%s: %v", schema, err)
		}
		var doc map[string]any
		if err := json.Unmarshal(r.Data, &doc); err != nil {
			t.Fatalf("%s: not JSON: %v", schema, err)
		}
		if doc["schema"] != string(schema) || r.Schema != schema || r.Title == "" || len(r.People) == 0 {
			t.Errorf("%s record = %+v", schema, r)
		}
		for _, pr := range r.People {
			if !strings.HasPrefix(pr.PersonID, "p") || pr.Role == "" {
				t.Errorf("%s link = %+v", schema, pr)
			}
		}
	}
	company, _ := g.Record(SchemaCompany, RecordInput{People: ps}, 0)
	var doc map[string]any
	_ = json.Unmarshal(company.Data, &doc)
	if contacts := doc["contacts"].([]any); len(contacts) != len(company.People) || len(contacts) < 1 {
		t.Errorf("company contacts = %v, links %v", contacts, company.People)
	}
	if _, err := g.Record("invoice", RecordInput{People: ps}, 0); err == nil {
		t.Error("unknown schema accepted")
	}
	if _, err := g.Record(SchemaContact, RecordInput{}, 0); err == nil {
		t.Error("record without people generated")
	}
	if RecordSchema("invoice").Valid() {
		t.Error("unknown schema is valid")
	}
}

func TestSmallHelpers(t *testing.T) {
	if domainOf(Person{Email: "nobody"}) != "example.example" || domainOf(Person{Email: "a@b.example"}) != "b.example" {
		t.Error("domainOf")
	}
	if mimeFor("json") != "application/json" || mimeFor("bin") != "application/octet-stream" {
		t.Error("mimeFor")
	}
	if xmlEscape(`<a href="x">&</a>`) != "&lt;a href=&quot;x&quot;&gt;&amp;&lt;/a&gt;" {
		t.Errorf("xmlEscape = %q", xmlEscape(`<a href="x">&</a>`))
	}
	if lowerFirst("") != "" || lowerFirst("The CNC job") != "the CNC job" {
		t.Error("lowerFirst")
	}
	if got := quote("a\r\nb\r\n"); got != "> a\r\n> b\r\n" {
		t.Errorf("quote = %q", got)
	}
	if !strings.Contains(htmlFromText("a<b\r\nc\r\n\r\nd"), "<p>a&lt;b<br>c</p><p>d</p>") {
		t.Errorf("htmlFromText = %q", htmlFromText("a<b\r\nc\r\n\r\nd"))
	}
}

func TestRecordRotation(t *testing.T) {
	// 20 people: the five schemas start 4 people apart.
	for schema, want := range map[RecordSchema][]int{
		SchemaContact: {0, 1, 2}, SchemaProject: {8, 9, 10}, SchemaAddress: {16, 17, 18},
	} {
		for k, w := range want {
			if got := personIndex(schema, k, 20); got != w {
				t.Errorf("%s record %d: person %d, want %d", schema, k, got, w)
			}
		}
	}
	// Fewer people than schemas still stays in range, and a schema uses
	// every person before repeating one.
	seen := map[int]bool{}
	for k := 0; k < 3; k++ {
		i := personIndex(SchemaAddress, k, 3)
		if i < 0 || i >= 3 || seen[i] {
			t.Errorf("address record %d: person %d", k, i)
		}
		seen[i] = true
	}

	ps := []Person{{ID: "a", Company: "X"}, {ID: "b", Company: "X"}, {ID: "c", Company: "Y"}}
	for k, want := range []string{"a", "c", "a"} {
		if got := companyPerson(ps, k).ID; got != want {
			t.Errorf("company record %d is about %s, want %s", k, got, want)
		}
	}
}
