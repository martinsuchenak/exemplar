// Package mimetree parses an email into an editable MIME tree and writes it
// back with the same structure: header order, part order, boundaries,
// preambles and transfer encodings are kept, so a rewritten message parses
// the same way as the original. Output always uses CRLF line endings.
package mimetree

import (
	"bytes"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"mime"
	"mime/quotedprintable"
	"strings"
	"unicode/utf8"
)

// ErrCharset is returned for text in a charset this package cannot convert.
var ErrCharset = errors.New("unsupported charset")

// MaxDepth caps how deeply multiparts and attached messages may nest, counted
// together (ParseAt continues the count into an attached message). Real mail
// rarely nests a dozen levels; each level scans its part once, so the cap
// also bounds parsing time to MaxDepth passes over the message.
const MaxDepth = 32

// MaxParts caps how many parts one message may have.
const MaxParts = 10000

// Field is one header field. Value is unfolded but otherwise raw, so it may
// hold RFC 2047 encoded words.
type Field struct {
	Name  string
	Value string
}

// Header is an ordered list of fields.
type Header struct {
	Fields []Field
}

// Get returns the first value of name (case-insensitive), or "".
func (h *Header) Get(name string) string {
	for _, f := range h.Fields {
		if strings.EqualFold(f.Name, name) {
			return f.Value
		}
	}
	return ""
}

// Set replaces the first field called name and removes the others, or
// appends one when there is none.
func (h *Header) Set(name, value string) {
	out := h.Fields[:0]
	done := false
	for _, f := range h.Fields {
		if strings.EqualFold(f.Name, name) {
			if done {
				continue
			}
			f.Value, done = value, true
		}
		out = append(out, f)
	}
	h.Fields = out
	if !done {
		h.Fields = append(h.Fields, Field{Name: name, Value: value})
	}
}

// Del removes every field called name.
func (h *Header) Del(name string) {
	out := h.Fields[:0]
	for _, f := range h.Fields {
		if !strings.EqualFold(f.Name, name) {
			out = append(out, f)
		}
	}
	h.Fields = out
}

// Part is a MIME entity: a header and either a leaf body or child parts.
type Part struct {
	Header Header
	// Body is a leaf's body as stored (still transfer-encoded).
	Body []byte
	// Parts, Boundary, Preamble and Epilogue describe a multipart entity.
	Parts    []*Part
	Boundary string
	Preamble []byte
	Epilogue []byte
	// Depth is how deeply the part is nested: 0 for a message, one more per
	// multipart level or attached message above it.
	Depth int
}

// Parse reads a message or MIME entity.
func Parse(data []byte) (*Part, error) {
	return ParseAt(data, 0)
}

// ParseAt reads a message nested depth levels deep, such as an attached
// message/rfc822 part (pass its Depth+1), so MaxDepth counts the whole
// structure.
func ParseAt(data []byte, depth int) (*Part, error) {
	return parse(data, depth, new(int))
}

// parse reads one entity; parts counts every part of the message so far.
func parse(data []byte, depth int, parts *int) (*Part, error) {
	if depth > MaxDepth {
		return nil, fmt.Errorf("message nests more than %d levels deep", MaxDepth)
	}
	if *parts++; *parts > MaxParts {
		return nil, fmt.Errorf("message has more than %d parts", MaxParts)
	}
	head, body := splitHeader(data)
	p := &Part{Depth: depth}
	for _, line := range unfold(head) {
		name, value, ok := strings.Cut(line, ":")
		if !ok || strings.TrimSpace(name) == "" || strings.ContainsAny(name, " \t") {
			continue
		}
		p.Header.Fields = append(p.Header.Fields, Field{Name: name, Value: strings.TrimSpace(value)})
	}
	mt, params := p.MediaType()
	if strings.HasPrefix(mt, "multipart/") && params["boundary"] != "" {
		if err := p.parseMultipart(body, params["boundary"], depth, parts); err != nil {
			return nil, err
		}
		return p, nil
	}
	if depth > 0 {
		// A nested part's lines are CRLF-separated, without a final line end.
		body = normalise(body)
	}
	p.Body = body
	return p, nil
}

// MediaType returns the Content-Type's media type (lower-case, default
// text/plain) and parameters.
func (p *Part) MediaType() (string, map[string]string) {
	ct := p.Header.Get("Content-Type")
	if ct == "" {
		return "text/plain", map[string]string{}
	}
	mt, params, err := mime.ParseMediaType(ct)
	if err != nil {
		// Malformed parameters: keep the type, drop the parameters.
		mt = strings.ToLower(strings.TrimSpace(strings.SplitN(ct, ";", 2)[0]))
		params = map[string]string{}
	}
	return mt, params
}

// SetParam sets one Content-Type parameter, keeping the media type and the others.
func (p *Part) SetParam(name, value string) {
	mt, params := p.MediaType()
	params[name] = value
	p.Header.Set("Content-Type", mime.FormatMediaType(mt, params))
}

// IsMultipart reports whether the part has child parts.
func (p *Part) IsMultipart() bool {
	return p.Boundary != ""
}

// Walk calls fn for p and every part below it, depth first.
func (p *Part) Walk(fn func(*Part)) {
	fn(p)
	for _, c := range p.Parts {
		c.Walk(fn)
	}
}

// Encoding returns the Content-Transfer-Encoding, lower-case (default 7bit).
func (p *Part) Encoding() string {
	cte := strings.ToLower(strings.TrimSpace(p.Header.Get("Content-Transfer-Encoding")))
	if cte == "" {
		return "7bit"
	}
	return cte
}

// Decoded returns the leaf body with its transfer encoding removed.
func (p *Part) Decoded() ([]byte, error) {
	switch p.Encoding() {
	case "base64":
		clean := bytes.Map(func(r rune) rune {
			if r == '\r' || r == '\n' || r == ' ' || r == '\t' {
				return -1
			}
			return r
		}, p.Body)
		out := make([]byte, base64.StdEncoding.DecodedLen(len(clean)))
		n, err := base64.StdEncoding.Decode(out, clean)
		if err != nil {
			// Tolerate missing padding, as mail clients do.
			unpadded := bytes.TrimRight(clean, "=")
			out = make([]byte, base64.RawStdEncoding.DecodedLen(len(unpadded)))
			n, err = base64.RawStdEncoding.Decode(out, unpadded)
			if err != nil {
				return nil, fmt.Errorf("decoding base64: %w", err)
			}
		}
		return out[:n], nil
	case "quoted-printable":
		return io.ReadAll(quotedprintable.NewReader(bytes.NewReader(p.Body)))
	default:
		return p.Body, nil
	}
}

// Text returns a text leaf's content as UTF-8.
func (p *Part) Text() (string, error) {
	raw, err := p.Decoded()
	if err != nil {
		return "", err
	}
	_, params := p.MediaType()
	return ToUTF8(raw, params["charset"])
}

// SetText replaces a leaf's content with s, re-encoded in the part's own
// transfer encoding (7bit/8bit text that is no longer ASCII becomes
// quoted-printable) and labelled UTF-8.
func (p *Part) SetText(s string) {
	if cs := p.charset(); cs != "" && cs != "utf-8" && cs != "us-ascii" {
		p.SetParam("charset", "utf-8")
	}
	p.setEncoded([]byte(s), true)
}

// SetBytes replaces a leaf's content with data, base64-encoded.
func (p *Part) SetBytes(data []byte) {
	p.Header.Set("Content-Transfer-Encoding", "base64")
	p.Body = Base64Lines(data)
}

func (p *Part) charset() string {
	_, params := p.MediaType()
	return strings.ToLower(params["charset"])
}

func (p *Part) setEncoded(data []byte, text bool) {
	cte := p.Encoding()
	switch {
	case cte == "base64":
		p.Body = Base64Lines(data)
	case cte == "quoted-printable" || text && !isASCII(data):
		p.Header.Set("Content-Transfer-Encoding", "quoted-printable")
		var b bytes.Buffer
		w := quotedprintable.NewWriter(&b)
		_, _ = w.Write(data)
		_ = w.Close()
		p.Body = b.Bytes()
	default:
		p.Body = toCRLF(data)
	}
}

// Bytes writes the part back as a message with CRLF line endings.
func (p *Part) Bytes() []byte {
	var b bytes.Buffer
	p.write(&b)
	return b.Bytes()
}

func (p *Part) write(b *bytes.Buffer) {
	for _, f := range p.Header.Fields {
		b.WriteString(fold(f.Name + ": " + f.Value))
		b.WriteString("\r\n")
	}
	b.WriteString("\r\n")
	if !p.IsMultipart() {
		b.Write(toCRLF(p.Body))
		return
	}
	if len(p.Preamble) > 0 {
		b.Write(toCRLF(p.Preamble))
		if !bytes.HasSuffix(b.Bytes(), []byte("\r\n")) {
			b.WriteString("\r\n")
		}
	}
	for _, c := range p.Parts {
		b.WriteString("--" + p.Boundary + "\r\n")
		c.write(b)
		if !bytes.HasSuffix(b.Bytes(), []byte("\r\n")) {
			b.WriteString("\r\n")
		}
	}
	b.WriteString("--" + p.Boundary + "--\r\n")
	b.Write(toCRLF(p.Epilogue))
}

// parseMultipart splits body at the boundary lines. Children are slices of
// body, not copies, so each nesting level costs one scan of its own part.
func (p *Part) parseMultipart(body []byte, boundary string, depth int, parts *int) error {
	p.Boundary = boundary
	delim, closing := []byte("--"+boundary), []byte("--"+boundary+"--")
	start, end := 0, 0 // the current section: from start to the end of its last line
	inPart := false
	flush := func() error {
		section := body[start:end]
		if !inPart {
			p.Preamble = normalise(section)
			return nil
		}
		child, err := parse(section, depth+1, parts)
		if err != nil {
			return err
		}
		p.Parts = append(p.Parts, child)
		return nil
	}
	for pos := 0; pos < len(body); {
		next := len(body)
		line := body[pos:]
		if nl := bytes.IndexByte(line, '\n'); nl >= 0 {
			line, next = line[:nl], pos+nl+1
		}
		line = bytes.TrimSuffix(line, []byte("\r"))
		switch trimmed := bytes.TrimRight(line, " \t"); {
		case bytes.Equal(trimmed, closing):
			if err := flush(); err != nil {
				return err
			}
			p.Epilogue = normalise(body[next:])
			return nil
		case bytes.Equal(trimmed, delim):
			if err := flush(); err != nil {
				return err
			}
			start, end, inPart = next, next, true
		default:
			end = pos + len(line)
		}
		pos = next
	}
	return flush()
}

// normalise joins b's lines with CRLF, without a final line end.
func normalise(b []byte) []byte {
	return []byte(strings.Join(splitLines(b), "\r\n"))
}

// splitHeader separates the header block from the body at the first blank line.
func splitHeader(data []byte) (head, body []byte) {
	for i := 0; i < len(data); i++ {
		if data[i] != '\n' {
			continue
		}
		rest := data[i+1:]
		if bytes.HasPrefix(rest, []byte("\r\n")) {
			return data[:i+1], rest[2:]
		}
		if bytes.HasPrefix(rest, []byte("\n")) {
			return data[:i+1], rest[1:]
		}
	}
	if i := bytes.IndexByte(data, ':'); i < 0 {
		return nil, data
	}
	return data, nil
}

func unfold(head []byte) []string {
	var out []string
	for _, line := range splitLines(head) {
		if line == "" {
			continue
		}
		if (line[0] == ' ' || line[0] == '\t') && len(out) > 0 {
			out[len(out)-1] += " " + strings.TrimLeft(line, " \t")
			continue
		}
		out = append(out, line)
	}
	return out
}

func splitLines(b []byte) []string {
	s := strings.ReplaceAll(string(b), "\r\n", "\n")
	s = strings.TrimSuffix(s, "\n")
	if s == "" {
		return nil
	}
	return strings.Split(s, "\n")
}

// fold wraps a header line longer than 998 characters at spaces.
func fold(line string) string {
	const limit = 998
	if len(line) <= limit {
		return line
	}
	var b strings.Builder
	for len(line) > limit {
		i := strings.LastIndexAny(line[:limit], " \t")
		if i <= 0 {
			break
		}
		b.WriteString(line[:i] + "\r\n")
		line = line[i:]
	}
	b.WriteString(line)
	return b.String()
}

func toCRLF(b []byte) []byte {
	return bytes.ReplaceAll(bytes.ReplaceAll(b, []byte("\r\n"), []byte("\n")), []byte("\n"), []byte("\r\n"))
}

func isASCII(b []byte) bool {
	for _, c := range b {
		if c >= 0x80 {
			return false
		}
	}
	return true
}

// Base64Lines encodes data as base64 in 76-character CRLF lines.
func Base64Lines(data []byte) []byte {
	enc := base64.StdEncoding.EncodeToString(data)
	var b bytes.Buffer
	for len(enc) > 76 {
		b.WriteString(enc[:76] + "\r\n")
		enc = enc[76:]
	}
	b.WriteString(enc + "\r\n")
	return b.Bytes()
}

// ToUTF8 converts raw text in charset to UTF-8. It handles UTF-8, US-ASCII,
// ISO-8859-1 and Windows-1252, the charsets most mail uses; others return
// ErrCharset. Invalid UTF-8 labelled as UTF-8 is read as Windows-1252, as mail
// clients do.
func ToUTF8(raw []byte, charset string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(charset)) {
	case "", "utf-8", "utf8", "us-ascii", "ascii":
		if utf8.Valid(raw) {
			return string(raw), nil
		}
		return decode1252(raw), nil
	case "iso-8859-1", "latin1", "iso8859-1", "l1":
		var b strings.Builder
		for _, c := range raw {
			b.WriteRune(rune(c))
		}
		return b.String(), nil
	case "windows-1252", "cp1252":
		return decode1252(raw), nil
	default:
		return "", fmt.Errorf("%w %q", ErrCharset, charset)
	}
}

// cp1252 maps the 0x80–0x9F range, where Windows-1252 differs from Latin-1.
var cp1252 = [32]rune{
	'€', 0x81, '‚', 'ƒ', '„', '…', '†', '‡', 'ˆ', '‰', 'Š', '‹', 'Œ', 0x8D, 'Ž', 0x8F,
	0x90, '‘', '’', '“', '”', '•', '–', '—', '˜', '™', 'š', '›', 'œ', 0x9D, 'ž', 'Ÿ',
}

func decode1252(raw []byte) string {
	var b strings.Builder
	for _, c := range raw {
		if c >= 0x80 && c <= 0x9F {
			b.WriteRune(cp1252[c-0x80])
		} else {
			b.WriteRune(rune(c))
		}
	}
	return b.String()
}

// DecodeHeader decodes RFC 2047 encoded words in a header value, with the
// same charsets as ToUTF8. Undecodable words are left as they are.
func DecodeHeader(v string) string {
	d := mime.WordDecoder{CharsetReader: func(charset string, input io.Reader) (io.Reader, error) {
		raw, err := io.ReadAll(input)
		if err != nil {
			return nil, err
		}
		s, err := ToUTF8(raw, charset)
		if err != nil {
			return nil, err
		}
		return strings.NewReader(s), nil
	}}
	out, err := d.DecodeHeader(v)
	if err != nil {
		return v
	}
	return out
}

// EncodeHeader returns v, with non-ASCII text as RFC 2047 Q-encoded words.
func EncodeHeader(v string) string {
	for i := 0; i < len(v); i++ {
		if v[i] >= 0x80 {
			return mime.QEncoding.Encode("utf-8", v)
		}
	}
	return v
}
