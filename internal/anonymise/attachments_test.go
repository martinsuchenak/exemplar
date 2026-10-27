package anonymise

import (
	"archive/zip"
	"bytes"
	"encoding/base64"
	"io"
	"strings"
	"testing"

	"github.com/martinsuchenak/exemplar/internal/mimetree"
)

// attachmentMail carries every kind of attachment: ones the anonymiser reads
// (a CSV in Windows-1252, a DOCX with macros, an RTF, unnamed JSON) and
// ones it does not (a PNG, a PDF, an .eml sent as a file), plus a broken
// DOCX. Wendy Moss is named only in the CSV's Name column and the body.
func attachmentMail(t *testing.T) string {
	docx := buildDOCX(t, [][2]string{
		{"[Content_Types].xml", xmlH + `<Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types"><Default Extension="xml" ContentType="application/xml"/><Default Extension="rels" ContentType="application/vnd.openxmlformats-package.relationships+xml"/><Default Extension="bin" ContentType="application/octet-stream"/></Types>`},
		{"_rels/.rels", xmlH + `<Relationships ` + relNS + `><Relationship Id="rId1" Type="x" Target="word/document.xml"/></Relationships>`},
		{"word/document.xml", xmlH + `<w:document ` + wNS + `><w:body><w:p><w:r><w:t>Quote for Sarah Lane</w:t></w:r></w:p></w:body></w:document>`},
		{"word/vbaProject.bin", "macros"},
	})
	part := func(headers, body string) string {
		return "--B\r\n" + headers + "\r\n\r\n" + body + "\r\n"
	}
	b64 := func(s string) string { return base64.StdEncoding.EncodeToString([]byte(s)) }
	return "From: Sarah Lane <sarah.lane@acme-plumbing.com.au>\r\nTo: Tom Baker <tom.baker@harbour-freight.com.au>\r\n" +
		"Subject: Files\r\nMessage-ID: <files-1@acme-plumbing.com.au>\r\nMIME-Version: 1.0\r\nContent-Type: multipart/mixed; boundary=\"B\"\r\n\r\n" +
		part("Content-Type: text/plain; charset=utf-8", "Hi Tom, Wendy Moss asked for these. Sarah") +
		part("Content-Type: text/csv; charset=windows-1252; name=\"Wendy contacts.csv\"\r\nContent-Disposition: attachment; filename=\"Wendy contacts.csv\"",
			"Name,Email,Notes\r\nWendy Moss,wendy@acme-plumbing.com.au,Caf\xe9 owner\r\n") +
		part("Content-Type: application/vnd.openxmlformats-officedocument.wordprocessingml.document; name=\"quote.docx\"\r\nContent-Disposition: attachment; filename=\"quote.docx\"\r\nContent-Transfer-Encoding: base64",
			base64.StdEncoding.EncodeToString(docx)) +
		part("Content-Type: application/rtf; name=\"letter.rtf\"\r\nContent-Disposition: attachment; filename=\"letter.rtf\"\r\nContent-Transfer-Encoding: base64",
			b64(`{\rtf1\ansi\pard Dear Tom Baker, from Sarah Lane, 0412 345 678.\par}`)) +
		part("Content-Type: application/json\r\nContent-Disposition: attachment\r\nContent-Transfer-Encoding: base64",
			b64(`{"name": "Wendy Moss", "phone": "0412 345 678"}`)) +
		part("Content-Type: image/png; name=\"Sarah.png\"\r\nContent-Disposition: attachment; filename=\"Sarah.png\"\r\nContent-Transfer-Encoding: base64",
			b64("\x89PNG real photo")) +
		part("Content-Type: application/pdf; name=\"Sarah.pdf\"\r\nContent-Disposition: attachment; filename=\"Sarah.pdf\"\r\nContent-Transfer-Encoding: base64",
			b64("%PDF real invoice")) +
		part("Content-Type: application/octet-stream; name=\"old.eml\"\r\nContent-Disposition: attachment; filename=\"old.eml\"\r\nContent-Transfer-Encoding: base64",
			b64("From: Sarah Lane <sarah.lane@acme-plumbing.com.au>\r\n\r\nreal old mail")) +
		part("Content-Type: application/vnd.openxmlformats-officedocument.wordprocessingml.document; name=\"broken.docx\"\r\nContent-Disposition: attachment; filename=\"broken.docx\"\r\nContent-Transfer-Encoding: base64",
			b64("not a zip, from Sarah Lane")) +
		"--B--\r\n"
}

func anonymiseWithAttachments(t *testing.T, mode string) (*Output, map[string][]byte) {
	t.Helper()
	profile := *defaultProfile(t)
	profile.Attachments = mode
	e, err := New(testKey(t), &profile, wordDetector{names: []string{"Sarah Lane", "Tom Baker"}}, newMemRegistry())
	if err != nil {
		t.Fatal(err)
	}
	out := run(t, e, attachmentMail(t))[0]
	root, err := mimetree.Parse(out.Data)
	if err != nil {
		t.Fatal(err)
	}
	parts := map[string][]byte{}
	root.Walk(func(p *mimetree.Part) {
		if p.IsMultipart() {
			return
		}
		name := filename(p)
		if name == "" {
			name, _ = p.MediaType() // the body is text/plain
		}
		if mt, _ := p.MediaType(); strings.HasPrefix(mt, "text/") {
			text, _ := p.Text()
			parts[name] = []byte(text)
		} else {
			parts[name], _ = p.Decoded()
		}
	})
	return out, parts
}

func TestAttachmentsAreReplacedByDefault(t *testing.T) {
	out, parts := anonymiseWithAttachments(t, AttachmentsReplace)
	if out.AttachmentsRewritten != 0 || out.Attachments != 8 || len(out.DroppedParts) != 0 {
		t.Errorf("rewritten %d, replaced %d, dropped %v", out.AttachmentsRewritten, out.Attachments, out.DroppedParts)
	}
	for name, data := range parts {
		if name != "text/plain" && (strings.Contains(string(data), "Café owner") || strings.Contains(string(data), "Quote for") || strings.Contains(string(data), "Dear ")) {
			t.Errorf("%s kept its content under replace", name)
		}
	}
}

func TestAttachmentsAreRewrittenInPlace(t *testing.T) {
	out, parts := anonymiseWithAttachments(t, AttachmentsAnonymise)
	// The CSV, DOCX, RTF and JSON are rewritten; the PNG, PDF, the .eml
	// sent as a file and the broken DOCX are replaced.
	if out.AttachmentsRewritten != 4 || out.Attachments != 4 {
		t.Errorf("rewritten %d, replaced %d", out.AttachmentsRewritten, out.Attachments)
	}
	all := ""
	for name, data := range parts {
		all += "\n" + name + "\n" + string(data)
	}
	var docx []byte
	for name, data := range parts {
		if strings.HasSuffix(name, ".docx") && bytes.HasPrefix(data, []byte("PK")) {
			docx = data
		}
		if strings.HasSuffix(name, ".csv") {
			// Converted from Windows-1252, rewritten, kept as a CSV.
			if !strings.HasPrefix(string(data), "Name,Email,Notes\r\n") || !strings.Contains(string(data), "Café owner") {
				t.Errorf("csv = %q", data)
			}
		}
	}
	if docx == nil {
		t.Fatal("no rewritten DOCX")
	}
	zr, err := zip.NewReader(bytes.NewReader(docx), int64(len(docx)))
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range zr.File {
		rc, _ := f.Open()
		b, _ := io.ReadAll(rc)
		_ = rc.Close()
		all += "\n" + f.Name + "\n" + string(b)
	}
	for _, s := range []string{"Sarah", "Lane", "Wendy", "Moss", "Tom", "Baker", "acme-plumbing", "0412 345 678", "real photo", "real invoice", "real old mail", "macros"} {
		if strings.Contains(all, s) {
			t.Errorf("output still holds %q", s)
		}
	}
	// A name found only in an attachment is found in the body too, and the
	// attachment's replacements are in the email's report.
	if body := string(parts["text/plain"]); !strings.Contains(body, "asked for these") || out.Replacements["Wendy Moss"] == "" {
		t.Errorf("body = %q, Wendy -> %q", body, out.Replacements["Wendy Moss"])
	}
	if !strings.Contains(string(parts["application/json"]), `"phone": "0`) {
		t.Errorf("unnamed JSON = %q", parts["application/json"])
	}
	// What the DOCX dropped is listed under the attachment's fake name.
	if len(out.DroppedParts) != 1 || !strings.HasPrefix(out.DroppedParts[0], "attachment-") || !strings.HasSuffix(out.DroppedParts[0], ".docx: word/vbaProject.bin") {
		t.Errorf("dropped = %v", out.DroppedParts)
	}
	// The same email gives the same bytes again.
	again, _ := anonymiseWithAttachments(t, AttachmentsAnonymise)
	if !bytes.Equal(again.Data, out.Data) {
		t.Error("two runs differ")
	}
}

func TestAttachmentsSetting(t *testing.T) {
	for in, want := range map[string]string{"": AttachmentsReplace, `attachments = "replace"`: AttachmentsReplace, `attachments = "anonymise"`: AttachmentsAnonymise} {
		p, err := parseProfile([]byte("name = \"x\"\n" + in))
		if err != nil || p.Attachments != want {
			t.Errorf("%q: %v, %v", in, p, err)
		}
	}
	if _, err := parseProfile([]byte("name = \"x\"\nattachments = \"keep\"")); err == nil || !strings.Contains(err.Error(), `not "keep"`) {
		t.Errorf("bad setting err = %v", err)
	}
	if p := defaultProfile(t); p.Attachments != AttachmentsReplace {
		t.Errorf("the default profile %s attachments", p.Attachments)
	}
}
