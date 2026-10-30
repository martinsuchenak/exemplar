// Package anonymisetest gives tests outside the anonymiser a stand-in for
// Presidio, Presidio clients that fail, and a few small real-looking emails.
package anonymisetest

import (
	"archive/zip"
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

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
