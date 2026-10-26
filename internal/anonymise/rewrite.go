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
	var people []synth.Person
	kept := msg.Header.Fields[:0:0]
	for _, field := range msg.Header.Fields {
		name := strings.ToLower(field.Name)
		if !keepHeaders[name] {
			out.DroppedHeaders = append(out.DroppedHeaders, field.Name)
			continue
		}
		value := mimetree.DecodeHeader(field.Value)
		switch {
		case addressHeaders[name]:
			v, ps, err := e.rewriteAddresses(ctx, value, rep)
			if err != nil {
				return err
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
		case idHeaders[name]:
			field.Value = e.fakeMessageIDs(value)
		case textHeaders[name]:
			field.Value = mimetree.EncodeHeader(rep.Apply(value))
		case urlHeaders[name]:
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
				return err
			}
			field.Value = mimetree.EncodeHeader(rep.Apply(v))
		}
		kept = append(kept, field)
	}
	msg.Header.Fields = kept
	if len(people) == 0 {
		people = []synth.Person{synth.New(e.key.Seed("filler", "people")).Person(synth.New(1).CompanyName())}
	}

	var err error
	msg.Walk(func(p *mimetree.Part) {
		if err != nil {
			return
		}
		if p.IsMultipart() {
			// A nested multipart's own headers get the same pass as a leaf's.
			if p != msg {
				dropPartHeaders(p, out)
				if err = e.rewritePartHeaders(ctx, p, rep); err != nil {
					return
				}
			}
			// The preamble and epilogue are text around the boundary lines;
			// mailing-list footers there often carry addresses.
			p.Preamble = []byte(rep.Apply(string(p.Preamble)))
			p.Epilogue = []byte(rep.Apply(string(p.Epilogue)))
			return
		}
		err = e.rewriteLeaf(ctx, p, rep, out, people, p == msg, depth)
	})
	return err
}

// rewriteAddresses maps every address in an address header and returns the
// registry people in it.
func (e *Engine) rewriteAddresses(ctx context.Context, value string, rep *replacer) (string, []synth.Person, error) {
	addrs, err := mail.ParseAddressList(value)
	if err != nil {
		// Unparseable: replace what can be recognised.
		var ferr error
		v := emailRe.ReplaceAllStringFunc(value, func(a string) string {
			if ferr != nil {
				return a
			}
			var fake string
			fake, ferr = e.fakeEmail(ctx, a)
			return fake
		})
		return mimetree.EncodeHeader(rep.Apply(v)), nil, ferr
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
func (e *Engine) rewriteLeaf(ctx context.Context, p *mimetree.Part, rep *replacer, out *Output, people []synth.Person, root bool, depth int) error {
	mt, _ := p.MediaType()
	if !root {
		// The message's own headers were filtered by keepHeaders above; a
		// part's headers have had no such pass, so drop the non-structural
		// ones here (custom part headers can carry personal data).
		dropPartHeaders(p, out)
	}
	if cid := p.Header.Get("Content-ID"); cid != "" {
		p.Header.Set("Content-ID", "<"+e.fakeID(strings.Trim(cid, "<> "))+">")
	}
	if name := filename(p); name != "" {
		inline := strings.HasPrefix(strings.ToLower(p.Header.Get("Content-Disposition")), "inline") || p.Header.Get("Content-ID") != ""
		e.renameAttachment(p, name, inline)
	}
	if err := e.rewritePartHeaders(ctx, p, rep); err != nil {
		return err
	}
	switch {
	case mt == "message/rfc822":
		raw, err := p.Decoded()
		if err != nil {
			return err
		}
		inner, err := mimetree.ParseAt(raw, p.Depth+1)
		if err != nil {
			return err
		}
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
		p.SetText(rep.Apply(text))
	default:
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

// partHeaders are the headers a MIME part needs to parse; every other header
// on a part is dropped, as unknown headers on a message are. Custom part
// headers (X-*, Comments and the like) can carry addresses and names no
// detector reads, so they must not reach the output.
var partHeaders = set([]string{
	"Content-Type", "Content-Transfer-Encoding", "Content-Disposition", "Content-ID",
	"Content-Language", "Content-Description", "Content-Location", "Content-Features",
	"Content-MD5", "MIME-Version",
})

// rewritePartHeaders rewrites the part headers that carry free text or a
// URL: a description often repeats a file name or what the part is about
// ("acme.com.au!1792800000.xml.gz" on a DMARC report), and Content-Location
// names where the part came from.
func (e *Engine) rewritePartHeaders(ctx context.Context, p *mimetree.Part, rep *replacer) error {
	if desc := p.Header.Get("Content-Description"); desc != "" {
		p.Header.Set("Content-Description", mimetree.EncodeHeader(rep.Apply(mimetree.DecodeHeader(desc))))
	}
	if loc := strings.Trim(strings.TrimSpace(p.Header.Get("Content-Location")), "<>"); loc != "" {
		if !strings.Contains(loc, "://") && !strings.HasPrefix(strings.ToLower(loc), "www.") {
			// A relative reference is a file name ("ann-lee-photo.png"):
			// renamed like an attachment, keeping the extension. The same
			// name gets the same fake, so references between parts still
			// match.
			fake := "part-" + e.key.Hex("location", loc, 6)
			if ext := strings.TrimPrefix(path.Ext(loc), "."); ext != "" && len(ext) <= 5 {
				fake += "." + strings.ToLower(ext)
			}
			p.Header.Set("Content-Location", fake)
			return nil
		}
		fake, err := e.fakeURL(ctx, loc)
		if err != nil {
			return err
		}
		p.Header.Set("Content-Location", fake)
	}
	return nil
}

// dropPartHeaders removes p's headers that carry no structure, counting them
// like dropped message headers.
func dropPartHeaders(p *mimetree.Part, out *Output) {
	kept := p.Header.Fields[:0:0]
	for _, f := range p.Header.Fields {
		if !partHeaders[strings.ToLower(f.Name)] {
			out.DroppedHeaders = append(out.DroppedHeaders, f.Name)
			continue
		}
		kept = append(kept, f)
	}
	p.Header.Fields = kept
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
