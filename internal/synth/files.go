package synth

import (
	"archive/zip"
	"bytes"
	"encoding/csv"
	"fmt"
	"html"
	"image"
	"image/color"
	"image/gif"
	"image/jpeg"
	"image/png"
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf16"
)

// File is generated file content.
type File struct {
	// Title is the document's title, a good basis for a fixture name.
	Title string
	// Format is the file extension without a dot.
	Format string
	Data   []byte
}

// FileInput is what a file is about. People appear as authors, recipients and
// table rows, so files mention the same fake people as records and emails.
type FileInput struct {
	Profile Profile
	// People must not be empty; the first is the author.
	People []Person
}

type fileWriter func(g *Gen, doc document) ([]byte, error)

var fileWriters = map[string]fileWriter{
	"pdf":  writePDF,
	"docx": writeDOCX,
	"xlsx": writeXLSX,
	"csv":  writeCSV,
	"txt":  writeTXT,
	"rtf":  writeRTF,
	"html": writeHTML,
	"png":  writeImage("png"),
	"jpg":  writeImage("jpg"),
	"gif":  writeImage("gif"),
}

// FileFormats lists the formats File can produce, sorted.
func FileFormats() []string {
	out := make([]string, 0, len(fileWriters))
	for f := range fileWriters {
		out = append(out, f)
	}
	sort.Strings(out)
	return out
}

// IsFileFormat reports whether File can produce format.
func IsFileFormat(format string) bool {
	_, ok := fileWriters[format]
	return ok
}

// document is the content every file format renders in its own way.
type document struct {
	title      string
	reference  string
	date       time.Time
	author     Person
	recipient  Person
	paragraphs []string
	lineItems  [][3]string // item, quantity, amount
	people     []Person
}

// File generates a file of format about in.
func (g *Gen) File(format string, in FileInput) (File, error) {
	w, ok := fileWriters[format]
	if !ok {
		return File{}, fmt.Errorf("unknown file format %q", format)
	}
	if len(in.People) == 0 {
		return File{}, fmt.Errorf("a file needs at least one person")
	}
	if !in.Profile.Valid() {
		in.Profile = ProfileInsurance
	}
	doc := g.document(in)
	data, err := w(g, doc)
	if err != nil {
		return File{}, fmt.Errorf("generating %s: %w", format, err)
	}
	return File{Title: doc.title, Format: format, Data: data}, nil
}

func (g *Gen) document(in FileInput) document {
	recipient := in.People[0]
	if len(in.People) > 1 {
		recipient = in.People[1+g.Intn(len(in.People)-1)]
	}
	doc := document{
		title:      g.Title(in.Profile),
		reference:  g.Reference(),
		date:       g.Date(180),
		author:     in.People[0],
		recipient:  recipient,
		paragraphs: g.Paragraphs(in.Profile, g.Between(3, 7)),
		people:     in.People,
	}
	for i := 0; i < g.Between(3, 6); i++ {
		doc.lineItems = append(doc.lineItems, [3]string{g.Label(in.Profile), fmt.Sprint(g.Between(1, 12)), g.Money(40, 4000)})
	}
	return doc
}

// lines renders the document as plain text lines, shared by the text formats.
func (d document) lines() []string {
	out := []string{
		d.title,
		fmt.Sprintf("Reference %s | %s", d.reference, d.date.Format("2 January 2006")),
		fmt.Sprintf("To: %s, %s (%s)", d.recipient.Name(), d.recipient.JobTitle, d.recipient.Company),
		fmt.Sprintf("From: %s, %s (%s)", d.author.Name(), d.author.JobTitle, d.author.Company),
		"",
	}
	for _, p := range d.paragraphs {
		out = append(out, p, "")
	}
	out = append(out, "Item | Qty | Amount")
	for _, li := range d.lineItems {
		out = append(out, strings.Join(li[:], " | "))
	}
	out = append(out, "", "Regards,", d.author.Name(), d.author.Phone, d.author.Email)
	return out
}

func writeTXT(_ *Gen, d document) ([]byte, error) {
	return []byte(strings.Join(d.lines(), "\n") + "\n"), nil
}

func writeHTML(_ *Gen, d document) ([]byte, error) {
	var b strings.Builder
	b.WriteString("<!DOCTYPE html>\n<html lang=\"en\"><head><meta charset=\"utf-8\"><title>")
	b.WriteString(html.EscapeString(d.title))
	b.WriteString("</title></head><body>\n<h1>" + html.EscapeString(d.title) + "</h1>\n")
	fmt.Fprintf(&b, "<p>Reference %s, %s</p>\n", html.EscapeString(d.reference), d.date.Format("2 January 2006"))
	for _, p := range d.paragraphs {
		b.WriteString("<p>" + html.EscapeString(p) + "</p>\n")
	}
	b.WriteString("<table>\n<tr><th>Item</th><th>Qty</th><th>Amount</th></tr>\n")
	for _, li := range d.lineItems {
		fmt.Fprintf(&b, "<tr><td>%s</td><td>%s</td><td>%s</td></tr>\n", html.EscapeString(li[0]), li[1], li[2])
	}
	fmt.Fprintf(&b, "</table>\n<p>Regards,<br>%s<br>%s</p>\n</body></html>\n", html.EscapeString(d.author.Name()), html.EscapeString(d.author.Email))
	return []byte(b.String()), nil
}

func writeRTF(_ *Gen, d document) ([]byte, error) {
	var b strings.Builder
	b.WriteString(`{\rtf1\ansi\deff0{\fonttbl{\f0 Helvetica;}}\f0\fs22`)
	fmt.Fprintf(&b, `{\b\fs32 %s}\par`, rtfEscape(d.title))
	for _, l := range d.lines()[1:] {
		b.WriteString(`\par ` + rtfEscape(l))
	}
	b.WriteString(`}`)
	return []byte(b.String()), nil
}

// tableRows is the people table used by the spreadsheet formats.
func (d document) tableRows(g *Gen) [][]string {
	rows := [][]string{{"ID", "First Name", "Last Name", "Email", "Phone", "Company", "Job Title", "Amount", "Date"}}
	for i, p := range d.people {
		rows = append(rows, []string{
			fmt.Sprint(i + 1), p.FirstName, p.LastName, p.Email, p.Phone, p.Company, p.JobTitle,
			g.Money(100, 20000), g.Date(365).Format("2006-01-02"),
		})
	}
	return rows
}

func writeCSV(g *Gen, d document) ([]byte, error) {
	var b bytes.Buffer
	w := csv.NewWriter(&b)
	if err := w.WriteAll(d.tableRows(g)); err != nil {
		return nil, err
	}
	return b.Bytes(), nil
}

// zipFiles writes a ZIP with fixed timestamps, so the bytes depend only on
// the content.
func zipFiles(files [][2]string) ([]byte, error) {
	var b bytes.Buffer
	zw := zip.NewWriter(&b)
	stamp := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for _, f := range files {
		w, err := zw.CreateHeader(&zip.FileHeader{Name: f[0], Method: zip.Deflate, Modified: stamp})
		if err != nil {
			return nil, err
		}
		if _, err := w.Write([]byte(f[1])); err != nil {
			return nil, err
		}
	}
	if err := zw.Close(); err != nil {
		return nil, err
	}
	return b.Bytes(), nil
}

const xmlHeader = `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>` + "\n"

func writeDOCX(_ *Gen, d document) ([]byte, error) {
	var body strings.Builder
	para := func(text, style string) {
		body.WriteString("<w:p>")
		if style != "" {
			body.WriteString(`<w:pPr><w:pStyle w:val="` + style + `"/></w:pPr>`)
		}
		body.WriteString(`<w:r><w:t xml:space="preserve">` + xmlEscape(text) + "</w:t></w:r></w:p>")
	}
	lines := d.lines()
	para(lines[0], "Title")
	for _, l := range lines[1:] {
		para(l, "")
	}
	return zipFiles([][2]string{
		{"[Content_Types].xml", xmlHeader + `<Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types">` +
			`<Default Extension="rels" ContentType="application/vnd.openxmlformats-package.relationships+xml"/>` +
			`<Default Extension="xml" ContentType="application/xml"/>` +
			`<Override PartName="/word/document.xml" ContentType="application/vnd.openxmlformats-officedocument.wordprocessingml.document.main+xml"/>` +
			`</Types>`},
		{"_rels/.rels", xmlHeader + `<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">` +
			`<Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/officeDocument" Target="word/document.xml"/>` +
			`</Relationships>`},
		{"word/document.xml", xmlHeader + `<w:document xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main"><w:body>` +
			body.String() + `</w:body></w:document>`},
	})
}

func writeXLSX(g *Gen, d document) ([]byte, error) {
	var sheet strings.Builder
	sheet.WriteString(xmlHeader + `<worksheet xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main"><sheetData>`)
	for r, row := range d.tableRows(g) {
		fmt.Fprintf(&sheet, `<row r="%d">`, r+1)
		for c, v := range row {
			fmt.Fprintf(&sheet, `<c r="%s%d" t="inlineStr"><is><t>%s</t></is></c>`, columnName(c), r+1, xmlEscape(v))
		}
		sheet.WriteString("</row>")
	}
	sheet.WriteString("</sheetData></worksheet>")
	return zipFiles([][2]string{
		{"[Content_Types].xml", xmlHeader + `<Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types">` +
			`<Default Extension="rels" ContentType="application/vnd.openxmlformats-package.relationships+xml"/>` +
			`<Default Extension="xml" ContentType="application/xml"/>` +
			`<Override PartName="/xl/workbook.xml" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.sheet.main+xml"/>` +
			`<Override PartName="/xl/worksheets/sheet1.xml" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.worksheet+xml"/>` +
			`</Types>`},
		{"_rels/.rels", xmlHeader + `<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">` +
			`<Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/officeDocument" Target="xl/workbook.xml"/>` +
			`</Relationships>`},
		{"xl/workbook.xml", xmlHeader + `<workbook xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main" ` +
			`xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships">` +
			`<sheets><sheet name="` + xmlEscape(FitSheetName(d.title, MaxSheetName)) + `" sheetId="1" r:id="rId1"/></sheets></workbook>`},
		{"xl/_rels/workbook.xml.rels", xmlHeader + `<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">` +
			`<Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/worksheet" Target="worksheets/sheet1.xml"/>` +
			`</Relationships>`},
		{"xl/worksheets/sheet1.xml", sheet.String()},
	})
}

// columnName turns a zero-based column index into A, B, ... Z, AA.
func columnName(i int) string {
	name := ""
	for i++; i > 0; i = (i - 1) / 26 {
		name = string(rune('A'+(i-1)%26)) + name
	}
	return name
}

// MaxSheetName is the most characters an Excel sheet name may hold,
// counted in UTF-16 units as Excel counts them.
const MaxSheetName = 31

// FitSheetName fits a title to Excel's sheet name rules: at most max UTF-16
// units, none of []:*?/\ or control characters, no apostrophe or space at
// either end, and not empty. The anonymiser renames sheets with it too.
func FitSheetName(title string, max int) string {
	s := strings.Map(func(r rune) rune {
		if strings.ContainsRune(`[]:*?/\`, r) || unicode.IsControl(r) {
			return ' '
		}
		return r
	}, title)
	s = strings.Trim(truncateUTF16(strings.Trim(s, " '"), max), " '")
	if s == "" {
		return "Sheet"
	}
	return s
}

// truncateUTF16 cuts s to at most n UTF-16 units without splitting a
// character.
func truncateUTF16(s string, n int) string {
	units := 0
	for i, r := range s {
		if units += utf16.RuneLen(r); units > n {
			return s[:i]
		}
	}
	return s
}

func writeImage(format string) fileWriter {
	return func(g *Gen, d document) ([]byte, error) {
		w, h := g.Between(160, 480), g.Between(120, 360)
		img := image.NewRGBA(image.Rect(0, 0, w, h))
		bg := color.RGBA{uint8(g.Between(120, 255)), uint8(g.Between(120, 255)), uint8(g.Between(120, 255)), 255}
		fill(img, img.Bounds(), bg)
		for i := 0; i < g.Between(3, 7); i++ {
			x, y := g.Intn(w-20), g.Intn(h-20)
			r := image.Rect(x, y, x+g.Between(20, 120), y+g.Between(20, 90)).Intersect(img.Bounds())
			fill(img, r, color.RGBA{uint8(g.Intn(256)), uint8(g.Intn(256)), uint8(g.Intn(256)), 255})
		}
		var b bytes.Buffer
		var err error
		switch format {
		case "png":
			err = png.Encode(&b, img)
		case "jpg":
			err = jpeg.Encode(&b, img, &jpeg.Options{Quality: 80})
		default:
			err = gif.Encode(&b, img, &gif.Options{NumColors: 64})
		}
		return b.Bytes(), err
	}
}

func fill(img *image.RGBA, r image.Rectangle, c color.RGBA) {
	for y := r.Min.Y; y < r.Max.Y; y++ {
		for x := r.Min.X; x < r.Max.X; x++ {
			img.SetRGBA(x, y, c)
		}
	}
}

func xmlEscape(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch r {
		case '&':
			b.WriteString("&amp;")
		case '<':
			b.WriteString("&lt;")
		case '>':
			b.WriteString("&gt;")
		case '"':
			b.WriteString("&quot;")
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

func rtfEscape(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r == '\\' || r == '{' || r == '}':
			b.WriteRune('\\')
			b.WriteRune(r)
		case r > 127:
			fmt.Fprintf(&b, `\u%d?`, int16(r))
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}
