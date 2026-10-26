package synth

import (
	"bytes"
	"fmt"
	"strings"
)

// PDF layout on an A4 page, in points.
const (
	pdfWidth      = 595
	pdfHeight     = 842
	pdfMargin     = 56
	pdfLineHeight = 14
	pdfFontSize   = 10
	// pdfWrap is how many 10pt Helvetica characters fit the text width.
	pdfWrap = 92
)

// writePDF renders the document as a small, valid PDF 1.4: Helvetica text,
// as many A4 pages as it needs. It is hand-written to avoid a PDF library;
// fixtures need a real PDF, not a typeset one.
func writePDF(_ *Gen, d document) ([]byte, error) {
	var lines []string
	for _, l := range d.lines() {
		lines = append(lines, wrap(l, pdfWrap)...)
	}
	perPage := (pdfHeight - 2*pdfMargin) / pdfLineHeight
	var pages [][]string
	for len(lines) > 0 {
		n := min(perPage, len(lines))
		pages = append(pages, lines[:n])
		lines = lines[n:]
	}

	var objs []string
	add := func(s string) int {
		objs = append(objs, s)
		return len(objs)
	}
	catalog := add("") // filled in once the pages object number is known
	pagesObj := add("")
	font := add("<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica /Encoding /WinAnsiEncoding >>")
	var kids []string
	for i, page := range pages {
		var content strings.Builder
		y := pdfHeight - pdfMargin
		for j, l := range page {
			size := pdfFontSize
			if i == 0 && j == 0 {
				size = 14
			}
			fmt.Fprintf(&content, "BT /F1 %d Tf %d %d Td (%s) Tj ET\n", size, pdfMargin, y, pdfEscape(l))
			y -= pdfLineHeight
		}
		stream := add(fmt.Sprintf("<< /Length %d >>\nstream\n%sendstream", content.Len(), content.String()))
		pageObj := add(fmt.Sprintf("<< /Type /Page /Parent %d 0 R /MediaBox [0 0 %d %d] /Resources << /Font << /F1 %d 0 R >> >> /Contents %d 0 R >>",
			pagesObj, pdfWidth, pdfHeight, font, stream))
		kids = append(kids, fmt.Sprintf("%d 0 R", pageObj))
	}
	objs[catalog-1] = fmt.Sprintf("<< /Type /Catalog /Pages %d 0 R >>", pagesObj)
	objs[pagesObj-1] = fmt.Sprintf("<< /Type /Pages /Kids [%s] /Count %d >>", strings.Join(kids, " "), len(kids))
	info := add(fmt.Sprintf("<< /Title (%s) /Producer (exemplar) /CreationDate (D:%s) >>", pdfEscape(d.title), d.date.UTC().Format("20060102150405Z")))

	var b bytes.Buffer
	b.WriteString("%PDF-1.4\n%\xe2\xe3\xcf\xd3\n")
	offsets := make([]int, len(objs))
	for i, o := range objs {
		offsets[i] = b.Len()
		fmt.Fprintf(&b, "%d 0 obj\n%s\nendobj\n", i+1, o)
	}
	xref := b.Len()
	fmt.Fprintf(&b, "xref\n0 %d\n0000000000 65535 f \n", len(objs)+1)
	for _, off := range offsets {
		fmt.Fprintf(&b, "%010d 00000 n \n", off)
	}
	fmt.Fprintf(&b, "trailer\n<< /Size %d /Root %d 0 R /Info %d 0 R >>\nstartxref\n%d\n%%%%EOF\n", len(objs)+1, catalog, info, xref)
	return b.Bytes(), nil
}

// pdfEscape escapes a PDF literal string. Characters outside printable ASCII
// become '?', since the font uses a single-byte encoding.
func pdfEscape(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r == '(' || r == ')' || r == '\\':
			b.WriteRune('\\')
			b.WriteRune(r)
		case r < 32 || r > 126:
			b.WriteByte('?')
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

// wrap breaks s into lines of at most width characters at spaces.
func wrap(s string, width int) []string {
	if s == "" {
		return []string{""}
	}
	var out []string
	line := ""
	for _, w := range strings.Fields(s) {
		switch {
		case line == "":
			line = w
		case len(line)+1+len(w) <= width:
			line += " " + w
		default:
			out = append(out, line)
			line = w
		}
	}
	return append(out, line)
}
