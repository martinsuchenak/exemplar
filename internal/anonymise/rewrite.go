package anonymise

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"hash/crc32"
	"mime"
	"net/mail"
	"path"
	"regexp"
	"strings"

	"github.com/martinsuchenak/exemplar/internal/mimetree"
	"github.com/martinsuchenak/exemplar/internal/synth"
)

// signatureImageLimit is the size mail-processor uses to tell signature
// images from real attachments; replacements stay on the same side of it.
const signatureImageLimit = 50 * 1024

var urlInAngles = regexp.MustCompile(`<([^<>]+)>`)

// rewrite replaces personal data in msg's headers and parts. top is true for
// the outer message, whose people are linked to the fixture.
func (e *Engine) rewrite(ctx context.Context, msg *mimetree.Part, rep *replacer, out *Output, top bool) error {
	return e.rewriteAt(ctx, msg, rep, out, top, 0)
}

func (e *Engine) rewriteAt(ctx context.Context, msg *mimetree.Part, rep *replacer, out *Output, top bool, depth int) error {
	if depth > mimetree.MaxDepth {
		return fmt.Errorf("message nests attached emails more than %d levels deep", mimetree.MaxDepth)
	}
	people, err := e.rewriteHeaders(ctx, msg, rep, out, top)
	if err != nil {
		return err
	}
	if len(people) == 0 {
		people = e.fillerPeople()
	}

	msg.Walk(func(p *mimetree.Part) {
		if err != nil {
			return
		}
		if p != msg {
			if _, err = e.rewriteHeaders(ctx, p, rep, out, false); err != nil {
				return
			}
		}
		if p.IsMultipart() {
			// The preamble and epilogue are text around the boundary lines;
			// mailing-list footers there often carry addresses.
			p.Preamble = []byte(rep.Apply(string(p.Preamble)))
			p.Epilogue = []byte(rep.Apply(string(p.Epilogue)))
			return
		}
		err = e.rewriteLeaf(ctx, p, rep, out, people, depth)
	})
	return err
}

// rewriteHeaders rewrites each of p's headers by its kind (see headers.go;
// clean has already dropped the rest and rebuilt the structural ones) and
// returns the registry people its address headers name. top is true for
// the outer message, whose people are linked to the fixture.
func (e *Engine) rewriteHeaders(ctx context.Context, p *mimetree.Part, rep *replacer, out *Output, top bool) ([]synth.Person, error) {
	var people []synth.Person
	kept := p.Header.Fields[:0:0]
	for _, field := range p.Header.Fields {
		name := strings.ToLower(field.Name)
		value := mimetree.DecodeHeader(field.Value)
		switch messageHeaderKinds[name] {
		case kindAddress:
			v, ps, err := e.rewriteAddresses(ctx, value, rep)
			if err != nil {
				return nil, err
			}
			if v == "" {
				out.DroppedHeaders = append(out.DroppedHeaders, field.Name)
				continue
			}
			field.Value = v
			people = append(people, ps...)
			if top {
				for _, p := range ps {
					if p.ID != "" && headerRoles[name] != "" {
						out.People = appendRole(out.People, PersonRole{PersonID: p.ID, Role: headerRoles[name]})
					}
				}
			}
		case kindID:
			field.Value = e.fakeMessageIDs(value)
		case kindContentID:
			field.Value = "<" + e.fakeID(strings.Trim(value, "<> ")) + ">"
		case kindText:
			field.Value = mimetree.EncodeHeader(rep.Apply(value))
		case kindURLs, kindListID:
			var err error
			v := urlInAngles.ReplaceAllStringFunc(value, func(m string) string {
				if err != nil {
					return m
				}
				var u string
				u, err = e.fakeURL(ctx, strings.Trim(m, "<>"))
				return "<" + u + ">"
			})
			if err != nil {
				return nil, err
			}
			// List-Id's phrase is free text, scanned by collect.
			field.Value = mimetree.EncodeHeader(rep.Apply(v))
		case kindLocation:
			v, err := e.fakeLocation(ctx, value)
			if err != nil {
				return nil, err
			}
			field.Value = v
		}
		kept = append(kept, field)
	}
	p.Header.Fields = kept
	// multipart/related names its root part by Content-ID, faked above.
	if _, params := p.MediaType(); params["start"] != "" {
		p.SetParam("start", "<"+e.fakeID(strings.Trim(params["start"], "<>"))+">")
	}
	return people, nil
}

// rewriteAddresses maps every address in an address header and returns the
// registry people in it.
func (e *Engine) rewriteAddresses(ctx context.Context, value string, rep *replacer) (string, []synth.Person, error) {
	addrs, err := mail.ParseAddressList(value)
	if err != nil {
		// Unparseable: keep only the addresses in it, each faked. An empty
		// value drops the header, except the null sender.
		if strings.TrimSpace(value) == "<>" {
			return "<>", nil, nil
		}
		var fakes []string
		for _, a := range emailRe.FindAllString(value, -1) {
			fake, err := e.fakeEmail(ctx, a)
			if err != nil {
				return "", nil, err
			}
			fakes = append(fakes, fake)
		}
		return strings.Join(fakes, ", "), nil, nil
	}
	var people []synth.Person
	parts := make([]string, len(addrs))
	for i, a := range addrs {
		id, err := e.resolve(ctx, a.Name, a.Address)
		if err != nil {
			return "", nil, err
		}
		fake := a.Address
		if id != nil {
			fake = id.fake
			if id.person.Email != "" {
				people = append(people, id.person)
			}
		}
		parts[i] = (&mail.Address{Name: rep.Apply(a.Name), Address: fake}).String()
	}
	return strings.Join(parts, ", "), people, nil
}

func appendRole(roles []PersonRole, r PersonRole) []PersonRole {
	for _, x := range roles {
		if x == r {
			return roles
		}
	}
	return append(roles, r)
}

// rewriteLeaf rewrites one leaf: attached messages recursively, reports and
// body text in place, and every other leaf replaced by a dummy of its type.
func (e *Engine) rewriteLeaf(ctx context.Context, p *mimetree.Part, rep *replacer, out *Output, people []synth.Person, depth int) error {
	mt, _ := p.MediaType()
	name := filename(p)
	if name != "" {
		inline := strings.HasPrefix(strings.ToLower(p.Header.Get("Content-Disposition")), "inline") || p.Header.Get("Content-ID") != ""
		e.renameAttachment(p, name, inline)
	}
	switch {
	case mt == "message/rfc822":
		raw, err := p.Decoded()
		if err != nil {
			return err
		}
		inner, dropped, err := e.parse(raw, p.Depth+1)
		if err != nil {
			return err
		}
		out.DroppedHeaders = append(out.DroppedHeaders, dropped...)
		if err := e.rewriteAt(ctx, inner, rep, out, false, depth+1); err != nil {
			return err
		}
		if p.Encoding() == "base64" {
			p.SetBytes(inner.Bytes())
		} else {
			p.Body = inner.Bytes()
		}
	case reportTypes[mt]:
		raw, err := p.Decoded()
		if err != nil {
			return err
		}
		text := rep.Apply(string(raw))
		var ferr error
		text = emailRe.ReplaceAllStringFunc(text, func(a string) string {
			if ferr != nil {
				return a
			}
			var fake string
			fake, ferr = e.fakeEmail(ctx, a)
			return fake
		})
		if ferr != nil {
			return ferr
		}
		p.Body = []byte(text)
	case isBodyText(p):
		text, err := p.Text()
		if err != nil {
			return err
		}
		if mt == "text/html" {
			p.SetText(applyHTML(text, rep))
		} else {
			p.SetText(rep.Apply(text))
		}
	default:
		if e.rewriteAttachment(ctx, p, mt, name, out) {
			return nil
		}
		data, err := p.Decoded()
		if err != nil {
			return err
		}
		p.SetBytes(e.placeholder(mt, filename(p), len(data), people))
		if strings.HasPrefix(mt, "text/") {
			p.SetParam("charset", "utf-8")
		}
		out.Attachments++
	}
	return nil
}

// attachmentFormats are the formats read for an attachment whose name does
// not give one, by its media type.
var attachmentFormats = map[string]Format{
	"text/plain": FormatText, "text/markdown": FormatMarkdown, "text/csv": FormatCSV, "text/tab-separated-values": FormatTSV,
	"application/json": FormatJSON, "application/x-ndjson": FormatJSONL, "text/html": FormatHTML,
	"application/rtf": FormatRTF, "text/rtf": FormatRTF,
	"application/vnd.openxmlformats-officedocument.wordprocessingml.document": FormatDOCX,
	"application/vnd.openxmlformats-officedocument.spreadsheetml.sheet":       FormatXLSX,
}

// attachmentFile returns the handler and content of an attachment the
// profile rewrites in place, named name (its original name), or false when
// it is replaced: the profile replaces attachments, or the anonymiser does
// not read its format. Text is converted to UTF-8 from its charset first.
// An attached .eml is replaced: real attached emails are message/rfc822,
// rewritten already, and rewriting one through the email handler would
// restart the nesting limit.
func (e *Engine) attachmentFile(p *mimetree.Part, name string) (*formatDef, File, bool) {
	if e.profile.Attachments != AttachmentsAnonymise {
		return nil, File{}, false
	}
	mt, _ := p.MediaType()
	def, err := findFormat(name)
	if err != nil {
		f, ok := attachmentFormats[mt]
		if !ok {
			return nil, File{}, false
		}
		name = NameFor(f)
		if def, err = findFormat(name); err != nil {
			return nil, File{}, false
		}
	}
	if def.format == FormatEmail {
		return nil, File{}, false
	}
	var data []byte
	if strings.HasPrefix(mt, "text/") {
		text, err := p.Text()
		if err != nil {
			return nil, File{}, false
		}
		data = []byte(text)
	} else if data, err = p.Decoded(); err != nil {
		return nil, File{}, false
	}
	return def, File{Name: name, Data: data}, true
}

// prepareAttachment lets an attachment the profile rewrites add its names
// to the batch, as a document of its own would.
func (e *Engine) prepareAttachment(ctx context.Context, p *mimetree.Part) {
	if def, file, ok := e.attachmentFile(p, filename(p)); ok {
		// One that cannot be read is replaced when the email is rewritten.
		_ = def.handler.prepare(ctx, e, file)
	}
}

// rewriteAttachment rewrites an attachment in place through its format's
// handler, when the profile says so, and reports what it replaced and
// dropped with the email's own. It returns false to have the attachment
// replaced instead: the profile replaces attachments, its format is not
// one the anonymiser reads, or its handler failed (a broken file is never
// passed through).
func (e *Engine) rewriteAttachment(ctx context.Context, p *mimetree.Part, mt, name string, out *Output) bool {
	def, file, ok := e.attachmentFile(p, name)
	if !ok {
		return false
	}
	att, err := def.handler.anonymise(ctx, e, file)
	if err != nil {
		return false
	}
	if strings.HasPrefix(mt, "text/") {
		p.SetText(string(att.Data))
	} else {
		p.SetBytes(att.Data)
	}
	for orig, fake := range att.Replacements {
		if _, ok := out.Replacements[orig]; !ok {
			out.Replacements[orig] = fake
		}
	}
	for orig, entity := range att.Entities {
		if _, ok := out.Entities[orig]; !ok {
			out.Entities[orig] = entity
		}
	}
	// Listed under the attachment's fake name, or its format when it has
	// no name (or one with a comma, which separates the list in a header).
	label := filename(p)
	if label == "" || strings.Contains(label, ",") {
		label = "attachment (" + string(def.format) + ")"
	}
	for _, part := range att.DroppedParts {
		out.DroppedParts = append(out.DroppedParts, label+": "+part)
	}
	out.AttachmentsRewritten++
	return true
}

// fillerPeople are who a replaced file is about when nothing names anyone.
func (e *Engine) fillerPeople() []synth.Person {
	return []synth.Person{synth.New(e.key.Seed("filler", "people")).Person(synth.New(1).CompanyName())}
}

// fakeLocation rewrites a Content-Location. A relative reference is a file
// name ("ann-lee-photo.png"): renamed like an attachment, keeping the
// extension, and the same name gets the same fake, so references between
// parts still match. An absolute one is a URL, faked like any other.
func (e *Engine) fakeLocation(ctx context.Context, loc string) (string, error) {
	if !strings.Contains(loc, "://") && !strings.HasPrefix(strings.ToLower(loc), "www.") {
		fake := "part-" + e.key.Hex("location", loc, 6)
		if ext := strings.TrimPrefix(path.Ext(loc), "."); ext != "" && len(ext) <= 5 {
			fake += "." + strings.ToLower(ext)
		}
		return fake, nil
	}
	return e.fakeURL(ctx, loc)
}

// filename returns a part's file name from Content-Disposition or Content-Type.
func filename(p *mimetree.Part) string {
	if d := p.Header.Get("Content-Disposition"); d != "" {
		if _, params, err := parseDisposition(d); err == nil && params["filename"] != "" {
			return params["filename"]
		}
	}
	_, params := p.MediaType()
	return params["name"]
}

// renameAttachment replaces a file name with attachment-<hash>.<ext> or
// image-<hash>.<ext>, keeping a short extension.
func (e *Engine) renameAttachment(p *mimetree.Part, name string, inline bool) {
	prefix := "attachment"
	if inline {
		prefix = "image"
	}
	fake := prefix + "-" + e.key.Hex("file", name, 6)
	if ext := strings.TrimPrefix(path.Ext(name), "."); ext != "" && len(ext) <= 5 {
		fake += "." + strings.ToLower(ext)
	}
	if d := p.Header.Get("Content-Disposition"); d != "" {
		if disp, params, err := parseDisposition(d); err == nil {
			if _, ok := params["filename"]; ok {
				params["filename"] = fake
				p.Header.Set("Content-Disposition", formatDisposition(disp, params))
			}
		}
	}
	if _, params := p.MediaType(); params["name"] != "" {
		p.SetParam("name", fake)
	}
}

// placeholder makes valid content of the same type as an attachment, about
// the message's people, on the same side of the signature-image size limit.
func (e *Engine) placeholder(mediaType, name string, size int, people []synth.Person) []byte {
	g := synth.New(e.key.Seed("attachment", name+"\x00"+mediaType))
	format := formatFor(mediaType, name)
	if format != "" {
		if f, err := g.File(format, synth.FileInput{People: people}); err == nil {
			return padImage(format, f.Data, size)
		}
	}
	target := size
	if size >= signatureImageLimit {
		target = signatureImageLimit + 16*1024
	} else if target < 64 {
		target = 64
	}
	line := []byte("anonymised attachment\n")
	return bytes.Repeat(line, target/len(line)+1)[:target]
}

func formatFor(mediaType, name string) string {
	ext := strings.ToLower(strings.TrimPrefix(path.Ext(name), "."))
	if ext == "jpeg" {
		ext = "jpg"
	}
	if synth.IsFileFormat(ext) {
		return ext
	}
	switch mediaType {
	case "application/pdf":
		return "pdf"
	case "image/png":
		return "png"
	case "image/jpeg":
		return "jpg"
	case "image/gif":
		return "gif"
	case "text/csv":
		return "csv"
	case "text/html":
		return "html"
	case "text/plain":
		return "txt"
	case "application/rtf", "text/rtf":
		return "rtf"
	case "application/vnd.openxmlformats-officedocument.wordprocessingml.document":
		return "docx"
	case "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet":
		return "xlsx"
	}
	return ""
}

// padImage grows a generated image past the signature-image limit when the
// original was over it, with a comment block the image ignores.
func padImage(format string, data []byte, originalSize int) []byte {
	if originalSize < signatureImageLimit || len(data) >= signatureImageLimit {
		return data
	}
	need := signatureImageLimit + 1024 - len(data)
	switch format {
	case "png":
		chunk := pngChunk("tEXt", append([]byte("comment\x00"), bytes.Repeat([]byte("x"), need)...))
		end := len(data) - 12 // before IEND
		return append(append(append([]byte{}, data[:end]...), chunk...), data[end:]...)
	case "jpg":
		var com []byte
		for need > 0 {
			n := min(need, 65000)
			seg := []byte{0xFF, 0xFE, 0, 0}
			binary.BigEndian.PutUint16(seg[2:], uint16(n+2))
			com = append(append(com, seg...), bytes.Repeat([]byte("x"), n)...)
			need -= n
		}
		return append(append(append([]byte{}, data[:2]...), com...), data[2:]...)
	}
	return data
}

func pngChunk(kind string, data []byte) []byte {
	out := make([]byte, 8, 12+len(data))
	binary.BigEndian.PutUint32(out, uint32(len(data)))
	copy(out[4:], kind)
	out = append(out, data...)
	crc := crc32.ChecksumIEEE(append([]byte(kind), data...))
	return binary.BigEndian.AppendUint32(out, crc)
}

// parseDisposition parses a Content-Disposition value.
func parseDisposition(v string) (string, map[string]string, error) {
	return mime.ParseMediaType(v)
}

func formatDisposition(disp string, params map[string]string) string {
	return mime.FormatMediaType(disp, params)
}
