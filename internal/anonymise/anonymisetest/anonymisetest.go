// Package anonymisetest gives tests outside the anonymiser a stand-in for
// Presidio, Presidio clients that fail, and a few small real-looking emails.
package anonymisetest

import (
	"archive/zip"
	"bytes"
	"compress/zlib"
	"context"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
	"unicode/utf16"

	"github.com/martinsuchenak/exemplar/internal/anonymise"
)

// Key is a valid anonymisation secret for tests.
var Key = []byte("exemplar-test-anonymisation-key")

// FailingPresidios returns real Presidio clients that fail the ways a
// deployment can: nothing listening, an error status whose body echoes the
// request's URL, and an answer that is not JSON. Every error's detail names
// the service's address, which is what callers of a server must not see.
// Their servers stop when t ends.
func FailingPresidios(t testing.TB) map[string]anonymise.Detector {
	t.Helper()
	down := httptest.NewServer(http.NotFoundHandler())
	down.Close()
	failing := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "no model loaded for http://"+r.Host+r.URL.Path, http.StatusInternalServerError)
	}))
	t.Cleanup(failing.Close)
	garbage := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("{"))
	}))
	t.Cleanup(garbage.Close)
	return map[string]anonymise.Detector{
		"unreachable":  anonymise.NewPresidio(down.URL),
		"error status": anonymise.NewPresidio(failing.URL),
		"bad answer":   anonymise.NewPresidio(garbage.URL),
	}
}

// Detector finds the given names (and the names the engine passes) as PERSON,
// the way Presidio's NER would.
type Detector struct {
	Names []string
}

// Detect implements anonymise.Detector.
func (d Detector) Detect(_ context.Context, text string, extra []string, _ float64) ([]anonymise.Span, error) {
	var spans []anonymise.Span
	for _, n := range append(append([]string{}, d.Names...), extra...) {
		if n == "" {
			continue
		}
		for i := 0; ; {
			j := strings.Index(text[i:], n)
			if j < 0 {
				break
			}
			spans = append(spans, anonymise.Span{Start: i + j, End: i + j + len(n), Entity: anonymise.EntityPerson})
			i += j + len(n)
		}
	}
	return spans, nil
}

// Email builds a plain-text email from one named person to another; names are
// "First Last" and addresses are first.last@domain.
func Email(from, to, domain, subject, body string) []byte {
	addr := func(name string) string {
		return fmt.Sprintf("%s <%s@%s>", name, strings.ToLower(strings.ReplaceAll(name, " ", ".")), domain)
	}
	return []byte("From: " + addr(from) + "\r\nTo: " + addr(to) + "\r\nSubject: " + subject +
		"\r\nDate: Mon, 6 Jan 2025 09:30:00 +1100\r\nMessage-ID: <" + strings.ToLower(strings.ReplaceAll(subject, " ", "-")) + "@" + domain +
		">\r\nMIME-Version: 1.0\r\nContent-Type: text/plain; charset=utf-8\r\n\r\n" + body + "\r\n")
}

// XLSX builds a minimal workbook whose sheet holds text in one cell, below
// a Notes label, plus a part no anonymiser rule knows (xl/vbaProject.bin),
// which is dropped.
func XLSX(text string) []byte {
	var b bytes.Buffer
	zw := zip.NewWriter(&b)
	x := `xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main"`
	for _, p := range [][2]string{
		{"[Content_Types].xml", `<Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types">` +
			`<Default Extension="rels" ContentType="application/vnd.openxmlformats-package.relationships+xml"/>` +
			`<Default Extension="xml" ContentType="application/xml"/><Default Extension="bin" ContentType="application/octet-stream"/></Types>`},
		{"_rels/.rels", `<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">` +
			`<Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/officeDocument" Target="xl/workbook.xml"/></Relationships>`},
		{"xl/workbook.xml", `<workbook ` + x + ` xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships">` +
			`<sheets><sheet name="Sheet1" sheetId="1" r:id="rId1"/></sheets></workbook>`},
		{"xl/_rels/workbook.xml.rels", `<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">` +
			`<Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/worksheet" Target="worksheets/sheet1.xml"/></Relationships>`},
		{"xl/worksheets/sheet1.xml", `<worksheet ` + x + `><sheetData><row r="1"><c r="A1" t="inlineStr"><is><t>Notes</t></is></c></row>` +
			`<row r="2"><c r="A2" t="inlineStr"><is><t>` + strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;").Replace(text) + `</t></is></c></row></sheetData></worksheet>`},
		{"xl/vbaProject.bin", "macros"},
	} {
		w, _ := zw.Create(p[0])
		_, _ = w.Write([]byte(p[1]))
	}
	_ = zw.Close()
	return b.Bytes()
}

// DOCX builds a minimal Word document whose body is one paragraph of text,
// plus a part no anonymiser rule knows (word/vbaProject.bin), which is
// dropped.
func DOCX(text string) []byte {
	var b bytes.Buffer
	zw := zip.NewWriter(&b)
	for _, p := range [][2]string{
		{"[Content_Types].xml", `<Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types">` +
			`<Default Extension="rels" ContentType="application/vnd.openxmlformats-package.relationships+xml"/>` +
			`<Default Extension="xml" ContentType="application/xml"/><Default Extension="bin" ContentType="application/octet-stream"/></Types>`},
		{"_rels/.rels", `<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">` +
			`<Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/officeDocument" Target="word/document.xml"/></Relationships>`},
		{"word/document.xml", `<w:document xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main"><w:body><w:p><w:r><w:t>` +
			strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;").Replace(text) + `</w:t></w:r></w:p></w:body></w:document>`},
		{"word/vbaProject.bin", "macros"},
	} {
		w, _ := zw.Create(p[0])
		_, _ = w.Write([]byte(p[1]))
	}
	_ = zw.Close()
	return b.Bytes()
}

// PDF builds a one-page PDF whose page shows text in Helvetica, plus a
// script (a JavaScript name tree), which is dropped.
func PDF(text string) []byte {
	escape := strings.NewReplacer(`\`, `\\`, "(", `\(`, ")", `\)`)
	content := "BT /F1 12 Tf 72 720 Td (" + escape.Replace(text) + ") Tj ET"
	objs := []string{
		"<< /Type /Catalog /Pages 2 0 R /Names << /JavaScript << /Names [(init) 5 0 R] >> >> >>",
		"<< /Type /Pages /Kids [3 0 R] /Count 1 >>",
		"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] /Contents 4 0 R /Resources << /Font << /F1 6 0 R >> >> >>",
		fmt.Sprintf("<< /Length %d >>\nstream\n%s\nendstream", len(content), content),
		"<< /S /JavaScript /JS (app.alert(1)) >>",
		"<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>",
	}
	var b strings.Builder
	b.WriteString("%PDF-1.7\n")
	offsets := make([]int, len(objs))
	for i, o := range objs {
		offsets[i] = b.Len()
		fmt.Fprintf(&b, "%d 0 obj\n%s\nendobj\n", i+1, o)
	}
	xref := b.Len()
	fmt.Fprintf(&b, "xref\n0 %d\n0000000000 65535 f \n", len(objs)+1)
	for _, o := range offsets {
		fmt.Fprintf(&b, "%010d 00000 n \n", o)
	}
	fmt.Fprintf(&b, "trailer\n<< /Size %d /Root 1 0 R >>\nstartxref\n%d\n%%%%EOF\n", len(objs)+1, xref)
	return []byte(b.String())
}

var (
	pdfStream  = regexp.MustCompile(`(?s)stream\r?\n(.*?)endstream`)
	pdfStrings = regexp.MustCompile(`(?s)<([0-9A-Fa-f\s]+)>|\(((?:\\.|[^\\)])*)\)`)
)

// PDFText returns a PDF as text a secret can be searched for in: the file,
// every stream inflated, and every string in them decoded (hex strings, as
// PDFium writes text, and UTF-16 strings, as metadata often is).
func PDFText(data []byte) string {
	parts := []string{string(data)}
	for _, m := range pdfStream.FindAllSubmatch(data, -1) {
		if r, err := zlib.NewReader(bytes.NewReader(m[1])); err == nil {
			if b, err := io.ReadAll(r); err == nil {
				parts = append(parts, string(b))
			}
		}
	}
	var out strings.Builder
	for _, p := range parts {
		out.WriteString(p + "\n")
		for _, m := range pdfStrings.FindAllStringSubmatch(p, -1) {
			var raw []byte
			if m[1] != "" {
				raw, _ = hex.DecodeString(strings.Join(strings.Fields(m[1]), ""))
			} else {
				raw = []byte(strings.NewReplacer(`\(`, "(", `\)`, ")", `\\`, `\`).Replace(m[2]))
			}
			if bytes.HasPrefix(raw, []byte{0xfe, 0xff}) && len(raw)%2 == 0 {
				u := make([]uint16, 0, len(raw)/2-1)
				for i := 2; i < len(raw); i += 2 {
					u = append(u, uint16(raw[i])<<8|uint16(raw[i+1]))
				}
				out.WriteString(string(utf16.Decode(u)) + "\n")
				continue
			}
			for _, c := range raw {
				out.WriteRune(rune(c)) // Latin-1, as WinAnsi mostly is
			}
			out.WriteString("\n")
		}
	}
	return out.String()
}
