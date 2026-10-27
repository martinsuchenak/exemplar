package anonymise

import (
	"encoding/base64"
	"encoding/hex"
	"mime"
	"net/mail"
	"regexp"
	"strings"

	"github.com/martinsuchenak/exemplar/internal/mimetree"
)

// headerKind says how a kept header's value is anonymised. Every header a
// message or MIME part keeps has a kind; every other header is dropped,
// because a value nothing classifies is a value nothing anonymises.
type headerKind int

const (
	// kindAddress is a list of mailboxes, each mapped to its fake person.
	kindAddress headerKind = iota + 1
	// kindID is a list of message IDs, each faked.
	kindID
	// kindText is free text, scanned for personal data like a body.
	kindText
	// kindURLs is a list of <URL>s: hosts mapped, paths hashed.
	kindURLs
	// kindListID is List-Id: a phrase (free text) and an <id> (a host).
	kindListID
	// kindContentID is one ID, faked like a message ID.
	kindContentID
	// kindLocation is Content-Location: a URL or a file name.
	kindLocation
	// kindStructure is rebuilt from its parsed tokens when the message is
	// read (see clean), so nothing else in it survives: no comments, no
	// unknown parameters, no free text.
	kindStructure
)

// partHeaderKinds are the headers a MIME part keeps: the ones it needs to
// parse, and Content-Description and Content-Location, which are rewritten.
// Content-MD5 and Content-Features are dropped; they are not needed and
// their values are not checked.
var partHeaderKinds = map[string]headerKind{
	"content-type":              kindStructure,
	"content-transfer-encoding": kindStructure,
	"content-disposition":       kindStructure,
	"content-language":          kindStructure,
	"mime-version":              kindStructure,
	"content-id":                kindContentID,
	"content-description":       kindText,
	"content-location":          kindLocation,
}

// messageHeaderKinds are the headers a message keeps, ported from
// anonymise-corpus.py, plus the part headers (a message is also its own
// first part). Received, DKIM and ARC signatures, X-MS-Exchange-*,
// X-Originating-IP and the like are dropped: they carry addresses, IPs and
// tenant ids that nothing downstream reads.
var messageHeaderKinds = func() map[string]headerKind {
	m := map[string]headerKind{
		"from": kindAddress, "to": kindAddress, "cc": kindAddress, "bcc": kindAddress,
		"reply-to": kindAddress, "sender": kindAddress, "return-path": kindAddress,
		"disposition-notification-to": kindAddress,
		"message-id":                  kindID, "in-reply-to": kindID, "references": kindID,
		"subject": kindText, "thread-topic": kindText, "x-mailer": kindText, "user-agent": kindText,
		"list-unsubscribe": kindURLs, "list-post": kindURLs, "list-help": kindURLs,
		"list-id": kindListID,
	}
	for name := range structureRules {
		m[name] = kindStructure
	}
	for name, kind := range partHeaderKinds {
		m[name] = kind
	}
	return m
}()

// structureRules rebuild one structural value, or report false to drop the
// header. Each keeps only what the header's grammar allows, so a comment or
// an unexpected word cannot carry personal data through.
var structureRules = map[string]func(e *Engine, p *mimetree.Part, v string) (string, bool){
	"content-type":              (*Engine).cleanContentType,
	"content-disposition":       func(_ *Engine, p *mimetree.Part, v string) (string, bool) { return cleanDisposition(p, v) },
	"content-transfer-encoding": oneOf("7bit", "8bit", "binary", "quoted-printable", "base64"),
	"content-language":          languages(false),
	"accept-language":           languages(true),
	"mime-version":              func(*Engine, *mimetree.Part, string) (string, bool) { return "1.0", true },
	"date":                      func(_ *Engine, _ *mimetree.Part, v string) (string, bool) { return cleanDate(v) },
	"thread-index":              func(e *Engine, _ *mimetree.Part, v string) (string, bool) { return e.fakeThreadIndex(v) },
	"auto-submitted":            oneOf("no", "auto-generated", "auto-replied", "auto-notified"),
	"precedence":                oneOf("bulk", "list", "junk", "first-class", "auto_reply"),
	"importance":                oneOf("high", "normal", "low"),
	"x-ms-has-attach":           oneOf("", "yes", "no"),
	"list-unsubscribe-post":     exactly("List-Unsubscribe=One-Click"),
	"x-priority":                matching(regexp.MustCompile(`^[1-5](?: \((?:Highest|High|Normal|Low|Lowest)\))?$`)),
	"x-auto-response-suppress":  matching(regexp.MustCompile(`(?i)^(?:All|None|DR|NDR|RN|NRN|OOF|AutoReply)(?:\s*,\s*(?:All|None|DR|NDR|RN|NRN|OOF|AutoReply))*$`)),
	// Their presence is what classifiers read; an unexpected value becomes yes.
	"x-autoreply":   presence,
	"x-autorespond": presence,
	// Exchange's TNEF link is kept only when empty, as Outlook sends it.
	"x-ms-tnef-correlator": exactly(""),
}

// isUnique reports whether a header may appear only once. A duplicate of a
// structural or single-valued header is dropped (the parser reads the
// first), so a second Content-Type cannot smuggle a value past the rules.
func isUnique(kind headerKind) bool {
	return kind != kindAddress && kind != kindURLs
}

// clean drops the headers p and its parts may not keep, rebuilds the
// structural ones and regenerates multipart boundaries, before anything
// reads the message. message is true when p's headers are a message's.
// It returns the names of the dropped headers. Attached messages are not
// descended into: parse cleans each when it is read.
func (e *Engine) clean(p *mimetree.Part, message bool) []string {
	kinds := partHeaderKinds
	if message {
		kinds = messageHeaderKinds
	}
	var dropped []string
	seen := map[string]bool{}
	kept := p.Header.Fields[:0:0]
	for _, f := range p.Header.Fields {
		name := strings.ToLower(f.Name)
		kind := kinds[name]
		if kind == 0 || isUnique(kind) && seen[name] {
			dropped = append(dropped, f.Name)
			continue
		}
		seen[name] = true
		v, ok := e.cleanValue(p, kind, name, f.Value)
		if !ok {
			dropped = append(dropped, f.Name)
			continue
		}
		f.Value = v
		kept = append(kept, f)
	}
	p.Header.Fields = kept
	if p.IsMultipart() {
		// The boundary is the sender's text; a fresh one carries nothing.
		// "=_" cannot start a base64 or quoted-printable line.
		p.SetBoundary("=_" + e.key.Hex("boundary", p.Boundary, 24))
		for _, c := range p.Parts {
			dropped = append(dropped, e.clean(c, false)...)
		}
	}
	return dropped
}

// cleanValue reduces one kept value to the form its kind rewrites: the IDs,
// URLs or name it holds, without the text around them.
func (e *Engine) cleanValue(p *mimetree.Part, kind headerKind, name, v string) (string, bool) {
	switch kind {
	case kindStructure:
		return structureRules[name](e, p, strings.TrimSpace(v))
	case kindID:
		ids := messageIDRe.FindAllString(v, -1)
		return strings.Join(ids, " "), len(ids) > 0
	case kindContentID:
		if id := messageIDRe.FindString(v); id != "" {
			return id, true
		}
		if id := strings.TrimSpace(v); bareIDRe.MatchString(id) {
			return "<" + id + ">", true
		}
		return "", false
	case kindURLs:
		if strings.EqualFold(strings.TrimSpace(v), "NO") {
			return "NO", true // List-Post: NO
		}
		urls := urlInAngles.FindAllString(v, -1)
		return strings.Join(urls, ", "), len(urls) > 0
	case kindLocation:
		// <URL> or a bare URL or name, without anything after it.
		v = strings.TrimSpace(v)
		if rest, ok := strings.CutPrefix(v, "<"); ok {
			v, _, _ = strings.Cut(rest, ">")
		}
		loc := strings.Fields(v)
		if len(loc) == 0 {
			return "", false
		}
		return loc[0], true
	}
	return v, true
}

var (
	bareIDRe = regexp.MustCompile(`^[^\s<>()",;\\]+$`)
	// mediaTypeRe is a type/subtype at the start of a value.
	mediaTypeRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9!#$&^_.+-]*/[A-Za-z0-9][A-Za-z0-9!#$&^_.+-]*`)
	tokenRe     = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:+-]{0,39}$`)
	languageRe  = regexp.MustCompile(`^(?:[A-Za-z]{2,3}|[iIxX])(?:-[A-Za-z0-9]{1,8})*$`)
	qualityRe   = regexp.MustCompile(`^q=(?:0(?:\.\d{1,3})?|1(?:\.0{1,3})?)$`)
)

// contentTypeParams are the Content-Type parameters kept, each with its
// check. The boundary is regenerated by clean, and name (a leaf's file
// name) is replaced by the rewrite like filename.
var contentTypeParams = map[string]func(string) bool{
	"charset":     tokenRe.MatchString,
	"report-type": tokenRe.MatchString,
	"micalg":      tokenRe.MatchString,
	"type":        fullMatch(mediaTypeRe),
	"protocol":    fullMatch(mediaTypeRe),
	"start":       regexp.MustCompile(`^<[^<>\s]+>$`).MatchString, // a Content-ID, faked by the rewrite
	"method":      in("PUBLISH", "REQUEST", "REPLY", "ADD", "CANCEL", "REFRESH", "COUNTER", "DECLINECOUNTER"),
	"format":      in("flowed", "fixed"),
	"delsp":       in("yes", "no"),
	"reply-type":  in("original", "response"),
}

// cleanContentType keeps the media type and the parameters it is known to
// need. A value that does not parse keeps its leading type/subtype only.
func (e *Engine) cleanContentType(p *mimetree.Part, v string) (string, bool) {
	mt, params, err := mime.ParseMediaType(v)
	if err != nil {
		mt, params = strings.ToLower(mediaTypeRe.FindString(v)), nil
	}
	if !fullMatch(mediaTypeRe)(mt) {
		return "", false
	}
	kept := map[string]string{}
	for k, val := range params {
		if check := contentTypeParams[k]; check != nil && check(val) {
			kept[k] = val
		}
	}
	if !p.IsMultipart() && params["name"] != "" {
		kept["name"] = params["name"]
	}
	if p.IsMultipart() {
		kept["boundary"] = p.Boundary // replaced by clean straight after
	}
	if out := mime.FormatMediaType(mt, kept); out != "" {
		return out, true
	}
	return mt, true
}

// cleanDisposition keeps inline or attachment and a leaf's file name, which
// the rewrite replaces.
func cleanDisposition(p *mimetree.Part, v string) (string, bool) {
	disp, params, err := mime.ParseMediaType(v)
	if err != nil {
		disp, params = strings.ToLower(leadingToken(v)), nil
	}
	if disp != "inline" && disp != "attachment" {
		return "", false
	}
	kept := map[string]string{}
	if !p.IsMultipart() && params["filename"] != "" {
		kept["filename"] = params["filename"]
	}
	if out := mime.FormatMediaType(disp, kept); out != "" {
		return out, true
	}
	return disp, true
}

// cleanDate rewrites a date in one standard form, dropping any comment.
func cleanDate(v string) (string, bool) {
	t, err := mail.ParseDate(v)
	if err != nil {
		return "", false
	}
	return t.Format("Mon, 2 Jan 2006 15:04:05 -0700"), true
}

// fakeThreadIndex replaces Outlook's Thread-Index (a 22-byte conversation
// header plus 5 bytes per reply, base64) block by block, each fake block
// keyed by everything up to it: emails in one conversation keep sharing a
// prefix, and nothing of the original bytes survives.
func (e *Engine) fakeThreadIndex(v string) (string, bool) {
	raw, err := base64.StdEncoding.DecodeString(v)
	if err != nil || len(raw) < 22 || (len(raw)-22)%5 != 0 {
		return "", false
	}
	out := make([]byte, 0, len(raw))
	for start, end := 0, 22; end <= len(raw); start, end = end, end+5 {
		out = append(out, e.key.digest("thread-index", hex.EncodeToString(raw[:end]))[:end-start]...)
	}
	return base64.StdEncoding.EncodeToString(out), true
}

// languages keeps a list of language tags (Content-Language: en-AU, en), or
// with quality values for Accept-Language (en-AU, en;q=0.8).
func languages(weights bool) func(*Engine, *mimetree.Part, string) (string, bool) {
	return func(_ *Engine, _ *mimetree.Part, v string) (string, bool) {
		var tags []string
		for _, item := range strings.Split(v, ",") {
			tag, q, hasQ := strings.Cut(strings.TrimSpace(item), ";")
			tag = strings.TrimSpace(tag)
			if !languageRe.MatchString(tag) && !(weights && tag == "*") {
				return "", false
			}
			if hasQ {
				if q = strings.TrimSpace(q); !weights || !qualityRe.MatchString(q) {
					return "", false
				}
				tag += ";" + q
			}
			tags = append(tags, tag)
		}
		return strings.Join(tags, ", "), len(tags) > 0
	}
}

// oneOf keeps a value's leading token, as written, when it is one of values
// in any case; anything after it (parameters, a comment) is dropped.
func oneOf(values ...string) func(*Engine, *mimetree.Part, string) (string, bool) {
	ok := in(values...)
	return func(_ *Engine, _ *mimetree.Part, v string) (string, bool) {
		t := leadingToken(v)
		return t, ok(t) && (t != "" || v == "")
	}
}

// exactly keeps the value only when it is want.
func exactly(want string) func(*Engine, *mimetree.Part, string) (string, bool) {
	return func(_ *Engine, _ *mimetree.Part, v string) (string, bool) {
		return want, v == want
	}
}

// matching keeps the value only when it matches re in full.
func matching(re *regexp.Regexp) func(*Engine, *mimetree.Part, string) (string, bool) {
	return func(_ *Engine, _ *mimetree.Part, v string) (string, bool) {
		return v, fullMatch(re)(v)
	}
}

// presence keeps a header whose value does not matter, as yes.
func presence(*Engine, *mimetree.Part, string) (string, bool) {
	return "yes", true
}

var leadingTokenRe = regexp.MustCompile(`^\s*([A-Za-z0-9_-]*)`)

func leadingToken(v string) string {
	return leadingTokenRe.FindStringSubmatch(v)[1]
}

func fullMatch(re *regexp.Regexp) func(string) bool {
	return func(s string) bool {
		return re.FindString(s) == s && s != ""
	}
}

func in(values ...string) func(string) bool {
	ok := map[string]bool{}
	for _, v := range values {
		ok[strings.ToLower(v)] = true
	}
	return func(s string) bool { return ok[strings.ToLower(s)] }
}
