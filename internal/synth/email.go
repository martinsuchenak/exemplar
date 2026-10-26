package synth

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"html"
	"mime"
	"mime/quotedprintable"
	"net/mail"
	"sort"
	"strings"
	"time"
)

// Shape is how an email is built: its MIME structure and headers.
type Shape string

// Email shapes. Each covers a structure that mail parsers commonly get wrong.
const (
	ShapePlain           Shape = "plain"            // text/plain only
	ShapeHTML            Shape = "html"             // multipart/alternative, text and HTML
	ShapeReply           Shape = "reply"            // In-Reply-To/References, quoted text (Gmail or Outlook style)
	ShapeForward         Shape = "forward"          // forwarded inline under a "Forwarded message" header
	ShapeForwardAttached Shape = "forward-attached" // original attached as message/rfc822
	ShapeAttachment      Shape = "attachment"       // multipart/mixed with a PDF, DOCX or XLSX
	ShapeInlineImage     Shape = "inline-image"     // multipart/related with a cid: image
	ShapeCalendar        Shape = "calendar"         // text/calendar meeting request
	ShapeAutoReply       Shape = "auto-reply"       // out-of-office with Auto-Submitted
	ShapeBounce          Shape = "bounce"           // multipart/report delivery status notification
	ShapeNewsletter      Shape = "newsletter"       // bulk mail with List-* headers
	ShapeThread          Shape = "thread"           // several messages replying to each other
)

// Shapes lists every shape.
var Shapes = []Shape{
	ShapePlain, ShapeHTML, ShapeReply, ShapeForward, ShapeForwardAttached, ShapeAttachment,
	ShapeInlineImage, ShapeCalendar, ShapeAutoReply, ShapeBounce, ShapeNewsletter, ShapeThread,
}

// Valid reports whether s is a known shape.
func (s Shape) Valid() bool {
	for _, v := range Shapes {
		if s == v {
			return true
		}
	}
	return false
}

// EmailInput is what to generate.
type EmailInput struct {
	Profile Profile
	Shape   Shape
	// People must hold at least two people; the first sends.
	People []Person
}

// Email is one generated message.
type Email struct {
	Subject   string
	Shape     Shape
	MessageID string
	From      Person
	To        []Person
	Cc        []Person
	Date      time.Time
	// Data is the complete RFC 5322 message with CRLF line endings.
	Data []byte
}

// Emails generates one message, or several for ShapeThread.
func (g *Gen) Emails(in EmailInput) ([]Email, error) {
	if len(in.People) < 2 {
		return nil, fmt.Errorf("an email needs at least two people")
	}
	if !in.Shape.Valid() {
		return nil, fmt.Errorf("unknown email shape %q", in.Shape)
	}
	if !in.Profile.Valid() {
		in.Profile = ProfileInsurance
	}
	if in.Shape == ShapeThread {
		return g.thread(in), nil
	}
	return []Email{g.email(in)}, nil
}

// message is an email being built.
type message struct {
	from    Person
	to, cc  []Person
	subject string
	date    time.Time
	id      string
	extra   [][2]string
	body    *part
}

func (g *Gen) email(in EmailInput) Email {
	m := g.newMessage(in, g.Subject(in.Profile))
	text := g.bodyText(in.Profile, m)
	switch in.Shape {
	case ShapePlain:
		m.body = textPart(text)
	case ShapeHTML:
		m.body = alternative(text, htmlFromText(text))
	case ShapeReply:
		parent := g.newMessage(EmailInput{Profile: in.Profile, People: swapFirst(in.People)}, m.subject)
		parentText := g.bodyText(in.Profile, parent)
		m.subject = "Re: " + parent.subject
		m.extra = append(m.extra, [2]string{"In-Reply-To", parent.id}, [2]string{"References", parent.id})
		quoted := text + "\r\n" + g.quoteHeader(parent) + "\r\n" + quote(parentText)
		m.body = alternative(quoted, htmlFromText(text)+"<blockquote>"+htmlFromText(parentText)+"</blockquote>")
	case ShapeForward:
		orig := g.newMessage(EmailInput{Profile: in.Profile, People: swapFirst(in.People)}, m.subject)
		origText := g.bodyText(in.Profile, orig)
		m.subject = "Fwd: " + orig.subject
		fwd := g.Opening(in.Profile) + "\r\n\r\n---------- Forwarded message ---------\r\n" +
			"From: " + address(orig.from) + "\r\nDate: " + orig.date.Format("Mon, 2 Jan 2006 at 15:04") +
			"\r\nSubject: " + orig.subject + "\r\nTo: " + addressList(orig.to) + "\r\n\r\n" + origText
		m.body = textPart(fwd)
	case ShapeForwardAttached:
		orig := g.newMessage(EmailInput{Profile: in.Profile, People: swapFirst(in.People)}, m.subject)
		orig.body = textPart(g.bodyText(in.Profile, orig))
		m.subject = "Fwd: " + orig.subject
		m.body = mixed(textPart(text), &part{
			header: [][2]string{{"Content-Type", "message/rfc822"}, {"Content-Disposition", "attachment"}},
			raw:    orig.render(g),
		})
	case ShapeAttachment:
		formats := []string{"pdf", "docx", "xlsx"}
		f, _ := g.File(formats[g.Intn(len(formats))], FileInput{Profile: in.Profile, People: in.People})
		m.body = mixed(alternative(text, htmlFromText(text)), attachmentPart(fileName(f), mimeFor(f.Format), f.Data, false, ""))
	case ShapeInlineImage:
		img, _ := g.File("png", FileInput{Profile: in.Profile, People: in.People})
		cid := g.token() + "@" + domainOf(m.from)
		related := &part{
			header: [][2]string{{"Content-Type", "multipart/related"}},
			children: []*part{
				htmlPart(htmlFromText(text) + `<p><img src="cid:` + cid + `" alt="logo"></p>`),
				attachmentPart("logo.png", "image/png", img.Data, true, cid),
			},
		}
		m.body = &part{header: [][2]string{{"Content-Type", "multipart/alternative"}}, children: []*part{textPart(text), related}}
	case ShapeCalendar:
		m.subject = "Invitation: " + g.Label(in.Profile)
		m.body = &part{
			header:   [][2]string{{"Content-Type", "multipart/alternative"}},
			children: []*part{textPart(text), calendarPart(g, m)},
		}
	case ShapeAutoReply:
		m.subject = "Automatic reply: " + m.subject
		m.extra = append(m.extra, [2]string{"Auto-Submitted", "auto-replied"}, [2]string{"X-Autoreply", "yes"})
		back := m.date.AddDate(0, 0, g.Between(3, 14)).Format("Monday 2 January")
		m.body = textPart(fmt.Sprintf("Thank you for your email. I am out of the office until %s with limited access to email.\r\n\r\n"+
			"For urgent matters please contact our office on %s.\r\n\r\n%s", back, CompanyFor(m.from.Company).Phone, signature(m.from)))
	case ShapeBounce:
		orig := g.newMessage(EmailInput{Profile: in.Profile, People: swapFirst(in.People)}, m.subject)
		orig.body = textPart(g.bodyText(in.Profile, orig))
		failed := orig.to[0]
		failedAddr := "nobody." + g.token()[:6] + "@" + domainOf(failed)
		m.from = Person{FirstName: "Mail Delivery", LastName: "Subsystem", Email: "mailer-daemon@" + domainOf(orig.from), Company: orig.from.Company}
		m.to, m.cc = []Person{orig.from}, nil
		m.subject = "Undelivered Mail Returned to Sender"
		m.extra = append(m.extra, [2]string{"Auto-Submitted", "auto-replied"})
		status := "Reporting-MTA: dns; mail." + domainOf(orig.from) + "\r\n\r\n" +
			"Final-Recipient: rfc822; " + failedAddr + "\r\nAction: failed\r\nStatus: 5.1.1\r\n" +
			"Diagnostic-Code: smtp; 550 5.1.1 <" + failedAddr + ">: Recipient address rejected: User unknown\r\n"
		m.body = &part{
			header: [][2]string{{"Content-Type", "multipart/report; report-type=delivery-status"}},
			children: []*part{
				textPart("This is the mail system. Your message could not be delivered to one or more recipients.\r\n\r\n<" + failedAddr + ">: user unknown"),
				{header: [][2]string{{"Content-Type", "message/delivery-status"}}, raw: []byte(status)},
				{header: [][2]string{{"Content-Type", "message/rfc822"}}, raw: orig.render(g)},
			},
		}
	case ShapeNewsletter:
		company := CompanyFor(m.from.Company)
		m.from = Person{FirstName: company.Name, Email: "news@" + company.Domain, Company: company.Name}
		m.subject = company.Name + " update: " + g.Label(in.Profile)
		m.extra = append(m.extra,
			[2]string{"List-Id", "<news." + company.Domain + ">"},
			[2]string{"List-Unsubscribe", "<" + company.Website + "/unsubscribe?u=" + g.token() + ">, <mailto:unsubscribe@" + company.Domain + ">"},
			[2]string{"List-Unsubscribe-Post", "List-Unsubscribe=One-Click"},
			[2]string{"Precedence", "bulk"})
		var h strings.Builder
		h.WriteString("<table width=\"600\"><tr><td><h1>" + html.EscapeString(m.subject) + "</h1>")
		for _, p := range g.Paragraphs(in.Profile, 3) {
			h.WriteString("<p>" + html.EscapeString(p) + "</p>")
		}
		h.WriteString(`<p><a href="` + company.Website + `/unsubscribe">Unsubscribe</a></p></td></tr></table>`)
		m.body = alternative(text, h.String())
	}
	return m.email(g, in.Shape)
}

// thread is 3 to 5 messages between the first two people, each replying to
// the one before and quoting it.
func (g *Gen) thread(in EmailInput) []Email {
	n := g.Between(3, 5)
	subject := g.Subject(in.Profile)
	var out []Email
	var refs []string
	var prev *message
	var prevText string
	people := in.People
	for i := 0; i < n; i++ {
		m := g.newMessage(EmailInput{Profile: in.Profile, People: people}, subject)
		text := g.bodyText(in.Profile, m)
		if prev != nil {
			m.subject = "Re: " + subject
			m.date = prev.date.Add(time.Duration(g.Between(20, 2880)) * time.Minute)
			m.extra = append(m.extra, [2]string{"In-Reply-To", prev.id}, [2]string{"References", strings.Join(refs, " ")})
			text += "\r\n" + g.quoteHeader(prev) + "\r\n" + quote(prevText)
		}
		m.body = textPart(text)
		e := m.email(g, ShapeThread)
		out = append(out, e)
		refs = append(refs, m.id)
		prev, prevText = m, text
		people = swapFirst(people)
	}
	return out
}

func (g *Gen) newMessage(in EmailInput, subject string) *message {
	from := in.People[0]
	m := &message{from: from, to: []Person{in.People[1]}, subject: subject, date: g.Date(120)}
	for _, p := range in.People[2:] {
		if g.Chance(2) {
			m.cc = append(m.cc, p)
		}
	}
	m.id = "<" + g.token() + "@" + domainOf(from) + ">"
	return m
}

func (g *Gen) bodyText(p Profile, m *message) string {
	var b strings.Builder
	b.WriteString("Hi " + m.to[0].FirstName + ",\r\n\r\n")
	b.WriteString(g.Opening(p) + "\r\n\r\n")
	for _, para := range g.Paragraphs(p, g.Between(1, 3)) {
		b.WriteString(para + "\r\n\r\n")
	}
	b.WriteString(g.Closing(p) + "\r\n\r\n")
	b.WriteString(signature(m.from))
	return b.String()
}

// quoteHeader is the line above quoted text, in Gmail or Outlook style.
func (g *Gen) quoteHeader(m *message) string {
	if g.Chance(2) {
		return fmt.Sprintf("On %s, %s wrote:", m.date.Format("Mon, 2 Jan 2006 at 15:04"), address(m.from))
	}
	return "________________________________\r\nFrom: " + address(m.from) +
		"\r\nSent: " + m.date.Format("Monday, 2 January 2006 3:04 PM") +
		"\r\nTo: " + addressList(m.to) + "\r\nSubject: " + m.subject + "\r\n"
}

func (m *message) email(g *Gen, shape Shape) Email {
	return Email{Subject: m.subject, Shape: shape, MessageID: m.id, From: m.from, To: m.to, Cc: m.cc, Date: m.date, Data: m.render(g)}
}

func (m *message) render(g *Gen) []byte {
	var b bytes.Buffer
	header := func(k, v string) { b.WriteString(k + ": " + v + "\r\n") }
	header("From", address(m.from))
	header("To", addressList(m.to))
	if len(m.cc) > 0 {
		header("Cc", addressList(m.cc))
	}
	header("Subject", mime.QEncoding.Encode("utf-8", m.subject))
	header("Date", m.date.Format(time.RFC1123Z))
	header("Message-ID", m.id)
	for _, kv := range m.extra {
		header(kv[0], kv[1])
	}
	header("MIME-Version", "1.0")
	m.body.write(&b, g)
	return b.Bytes()
}

// part is a MIME entity: headers plus either children, raw bytes or text.
type part struct {
	header   [][2]string
	children []*part
	raw      []byte
}

func (p *part) write(b *bytes.Buffer, g *Gen) {
	if len(p.children) == 0 {
		for _, kv := range p.header {
			b.WriteString(kv[0] + ": " + kv[1] + "\r\n")
		}
		b.WriteString("\r\n")
		b.Write(p.raw)
		if !bytes.HasSuffix(p.raw, []byte("\r\n")) {
			b.WriteString("\r\n")
		}
		return
	}
	boundary := "=_" + g.token()
	for _, kv := range p.header {
		v := kv[1]
		if kv[0] == "Content-Type" {
			v += `; boundary="` + boundary + `"`
		}
		b.WriteString(kv[0] + ": " + v + "\r\n")
	}
	b.WriteString("\r\n")
	for _, c := range p.children {
		b.WriteString("--" + boundary + "\r\n")
		c.write(b, g)
	}
	b.WriteString("--" + boundary + "--\r\n")
}

func textPart(s string) *part {
	return &part{
		header: [][2]string{{"Content-Type", `text/plain; charset="utf-8"`}, {"Content-Transfer-Encoding", "quoted-printable"}},
		raw:    qp(s),
	}
}

func htmlPart(s string) *part {
	return &part{
		header: [][2]string{{"Content-Type", `text/html; charset="utf-8"`}, {"Content-Transfer-Encoding", "quoted-printable"}},
		raw:    qp("<html><body>" + s + "</body></html>"),
	}
}

func alternative(text, htmlBody string) *part {
	return &part{header: [][2]string{{"Content-Type", "multipart/alternative"}}, children: []*part{textPart(text), htmlPart(htmlBody)}}
}

func mixed(children ...*part) *part {
	return &part{header: [][2]string{{"Content-Type", "multipart/mixed"}}, children: children}
}

func attachmentPart(name, mimeType string, data []byte, inline bool, cid string) *part {
	disposition := "attachment"
	if inline {
		disposition = "inline"
	}
	h := [][2]string{
		{"Content-Type", mime.FormatMediaType(mimeType, map[string]string{"name": name})},
		{"Content-Disposition", mime.FormatMediaType(disposition, map[string]string{"filename": name})},
		{"Content-Transfer-Encoding", "base64"},
	}
	if cid != "" {
		h = append(h, [2]string{"Content-ID", "<" + cid + ">"})
	}
	return &part{header: h, raw: base64Lines(data)}
}

func calendarPart(g *Gen, m *message) *part {
	start := m.date.AddDate(0, 0, g.Between(2, 20)).Truncate(time.Hour)
	end := start.Add(time.Duration(g.Between(1, 3)) * 30 * time.Minute)
	stamp := func(t time.Time) string { return t.UTC().Format("20060102T150405Z") }
	var attendees strings.Builder
	for _, p := range append(append([]Person{}, m.to...), m.cc...) {
		fmt.Fprintf(&attendees, "ATTENDEE;CN=%s;ROLE=REQ-PARTICIPANT;RSVP=TRUE:mailto:%s\r\n", p.Name(), p.Email)
	}
	ics := "BEGIN:VCALENDAR\r\nVERSION:2.0\r\nPRODID:-//exemplar//synth//EN\r\nMETHOD:REQUEST\r\nBEGIN:VEVENT\r\n" +
		"UID:" + strings.Trim(m.id, "<>") + "\r\nDTSTAMP:" + stamp(m.date) + "\r\nDTSTART:" + stamp(start) + "\r\nDTEND:" + stamp(end) + "\r\n" +
		"SUMMARY:" + strings.TrimPrefix(m.subject, "Invitation: ") + "\r\n" +
		"ORGANIZER;CN=" + m.from.Name() + ":mailto:" + m.from.Email + "\r\n" + attendees.String() +
		"LOCATION:" + CompanyFor(m.from.Company).Address.String() + "\r\nEND:VEVENT\r\nEND:VCALENDAR\r\n"
	return &part{
		header: [][2]string{{"Content-Type", `text/calendar; charset="utf-8"; method=REQUEST`}, {"Content-Transfer-Encoding", "7bit"}},
		raw:    []byte(ics),
	}
}

func qp(s string) []byte {
	var b bytes.Buffer
	w := quotedprintable.NewWriter(&b)
	_, _ = w.Write([]byte(s))
	_ = w.Close()
	return b.Bytes()
}

func base64Lines(data []byte) []byte {
	enc := base64.StdEncoding.EncodeToString(data)
	var b bytes.Buffer
	for len(enc) > 76 {
		b.WriteString(enc[:76] + "\r\n")
		enc = enc[76:]
	}
	b.WriteString(enc + "\r\n")
	return b.Bytes()
}

func htmlFromText(text string) string {
	var b strings.Builder
	for _, p := range strings.Split(strings.TrimSpace(text), "\r\n\r\n") {
		b.WriteString("<p>" + strings.ReplaceAll(html.EscapeString(p), "\r\n", "<br>") + "</p>")
	}
	return b.String()
}

func quote(text string) string {
	lines := strings.Split(strings.TrimRight(text, "\r\n"), "\r\n")
	for i, l := range lines {
		lines[i] = "> " + l
	}
	return strings.Join(lines, "\r\n") + "\r\n"
}

func signature(p Person) string {
	return fmt.Sprintf("Kind regards,\r\n%s\r\n%s | %s\r\n%s\r\n", p.Name(), p.JobTitle, p.Company, p.Phone)
}

func address(p Person) string {
	return (&mail.Address{Name: strings.TrimSpace(p.Name()), Address: p.Email}).String()
}

func addressList(ps []Person) string {
	parts := make([]string, len(ps))
	for i, p := range ps {
		parts[i] = address(p)
	}
	return strings.Join(parts, ", ")
}

func domainOf(p Person) string {
	if i := strings.LastIndexByte(p.Email, '@'); i >= 0 {
		return p.Email[i+1:]
	}
	return "example.example"
}

// swapFirst returns people with the first two swapped, so the other side writes.
func swapFirst(people []Person) []Person {
	out := append([]Person{}, people...)
	out[0], out[1] = out[1], out[0]
	return out
}

// token returns 24 hex characters for IDs and boundaries.
func (g *Gen) token() string {
	return fmt.Sprintf("%08x%08x%08x", g.r.Uint32(), g.r.Uint32(), g.r.Uint32())
}

// fileName names an attached generated file after its title.
func fileName(f File) string {
	return slugify(f.Title) + "." + f.Format
}

var mimeTypes = map[string]string{
	"pdf":  "application/pdf",
	"docx": "application/vnd.openxmlformats-officedocument.wordprocessingml.document",
	"xlsx": "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet",
	"csv":  "text/csv",
	"txt":  "text/plain",
	"rtf":  "application/rtf",
	"html": "text/html",
	"png":  "image/png",
	"jpg":  "image/jpeg",
	"gif":  "image/gif",
	"json": "application/json",
	"eml":  "message/rfc822",
}

func mimeFor(format string) string {
	if t, ok := mimeTypes[format]; ok {
		return t
	}
	return "application/octet-stream"
}

// ShapeNames returns the shape names sorted, for help text.
func ShapeNames() []string {
	out := make([]string, len(Shapes))
	for i, s := range Shapes {
		out[i] = string(s)
	}
	sort.Strings(out)
	return out
}
