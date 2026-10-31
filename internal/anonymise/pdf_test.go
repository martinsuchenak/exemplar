package anonymise

import (
	"bytes"
	"compress/zlib"
	"context"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"regexp"
	"slices"
	"strings"
	"testing"
	"unicode/utf16"

	"github.com/klippa-app/go-pdfium/requests"
	"github.com/klippa-app/go-pdfium/structs"
	"github.com/pdfcpu/pdfcpu/pkg/api"
	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/model"
)

// buildPDF writes a PDF of objs, numbered from 1, with trailer entries
// beyond /Size and /Root.
func buildPDF(trailer string, objs ...string) []byte {
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
	fmt.Fprintf(&b, "trailer\n<< /Size %d /Root 1 0 R %s >>\nstartxref\n%d\n%%%%EOF\n", len(objs)+1, trailer, xref)
	return []byte(b.String())
}

func pdfStreamObj(dict, data string) string {
	return fmt.Sprintf("<< %s /Length %d >>\nstream\n%s\nendstream", dict, len(data), data)
}

// onePagePDF is a PDF of one page drawing content with Helvetica as /F1
// and a 2×2 image as /Im1.
func onePagePDF(content string) []byte {
	return buildPDF("",
		"<< /Type /Catalog /Pages 2 0 R >>",
		"<< /Type /Pages /Kids [3 0 R] /Count 1 >>",
		"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] /Contents 4 0 R /Resources << /Font << /F1 5 0 R >> /XObject << /Im1 6 0 R >> >> >>",
		pdfStreamObj("", content),
		"<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>",
		pdfStreamObj("/Type /XObject /Subtype /Image /Width 2 /Height 2 /ColorSpace /DeviceRGB /BitsPerComponent 8", "SECRETPIXELS"),
	)
}

// kitchenSink is a PDF with a person's name in every place a PDF keeps
// text: split across text objects, in bold, serif, mono and coloured text,
// in a form, as invisible text, as alt text, in a filled form field, a
// comment, links, bookmarks, metadata, an embedded file, scripts, the
// structure tree and an application's private data. Its image's pixels
// spell SECRETPIXELS.
func kitchenSink() []byte {
	page1 := strings.Join([]string{
		"BT /F1 12 Tf 72 720 Td (Hello Ottoline ) Tj ET",
		"BT /F1 12 Tf 147.36 720 Td (Quaife, welcome.) Tj ET",
		"BT 0 0 1 rg /F2 14 Tf 72 690 Td (Peregrine Thistlewood) Tj ET",
		"BT /F3 10 Tf 72 670 Td (ID: Ottoline Quaife 0412 345 678) Tj ET",
		"BT /F4 10 Tf 72 650 Td (Phone 0412 345 678 for Ottoline Quaife) Tj ET",
		"/Span << /ActualText (Ottoline Quaife's photo) >> BDC q 50 0 0 50 300 500 cm /Im1 Do Q EMC",
		"q 1 0 0 1 72 600 cm /Fm1 Do Q",
		"BT /F1 8 Tf 3 Tr 72 580 Td (hidden note for Peregrine Thistlewood) Tj ET",
	}, "\n")
	return buildPDF("/Info 29 0 R",
		/* 1 */ "<< /Type /Catalog /Pages 2 0 R /Outlines 9 0 R /Names 13 0 R /OpenAction 14 0 R /AcroForm 15 0 R /StructTreeRoot 17 0 R /MarkInfo << /Marked true >> /Metadata 18 0 R >>",
		/* 2 */ "<< /Type /Pages /Kids [3 0 R 20 0 R] /Count 2 >>",
		/* 3 */ "<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] /Contents 4 0 R /Resources << /Font << /F1 5 0 R /F2 11 0 R /F3 12 0 R /F4 25 0 R >> /XObject << /Fm1 6 0 R /Im1 19 0 R >> >> /Annots [7 0 R 8 0 R 16 0 R 21 0 R 23 0 R] /Thumb 19 0 R >>",
		/* 4 */ pdfStreamObj("", page1),
		/* 5 */ "<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>",
		/* 6 */ pdfStreamObj("/Type /XObject /Subtype /Form /BBox [0 0 400 50] /Matrix [1 0 0 1 10 5] /Resources << /Font << /F1 5 0 R >> >>", "BT /F1 12 Tf 0 0 Td (Form: Ottoline Quaife) Tj ET"),
		/* 7 */ "<< /Type /Annot /Subtype /Link /Rect [72 715 300 735] /A << /S /URI /URI (mailto:ottoline.quaife@acme-plumbing.example) >> >>",
		/* 8 */ "<< /Type /Annot /Subtype /Text /Rect [400 700 420 720] /Contents (Call Ottoline Quaife back) /T (Peregrine Thistlewood) >>",
		/* 9 */ "<< /Type /Outlines /First 10 0 R /Last 10 0 R /Count 1 >>",
		/* 10 */ "<< /Title (Notes for Ottoline Quaife) /Parent 9 0 R /Dest [3 0 R /Fit] >>",
		/* 11 */ "<< /Type /Font /Subtype /Type1 /BaseFont /Times-Bold >>",
		/* 12 */ "<< /Type /Font /Subtype /Type1 /BaseFont /Courier >>",
		/* 13 */ "<< /EmbeddedFiles << /Names [(ottoline.txt) 22 0 R] >> /JavaScript << /Names [(init) 14 0 R] >> >>",
		/* 14 */ "<< /S /JavaScript /JS (var who = 'Peregrine Thistlewood';) >>",
		/* 15 */ "<< /Fields [23 0 R] >>",
		/* 16 */ "<< /Type /Annot /Subtype /Link /Rect [72 640 200 660] /Dest [20 0 R /XYZ 0 792 0] >>",
		/* 17 */ "<< /Type /StructTreeRoot /K 26 0 R >>",
		/* 18 */ pdfStreamObj("/Type /Metadata /Subtype /XML", "<x:xmpmeta><dc:creator>Ottoline Quaife</dc:creator></x:xmpmeta>"),
		/* 19 */ pdfStreamObj("/Type /XObject /Subtype /Image /Width 2 /Height 2 /ColorSpace /DeviceRGB /BitsPerComponent 8", "SECRETPIXELS"),
		/* 20 */ "<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] /Contents 28 0 R /Resources << /Font << /F1 5 0 R >> >> /PieceInfo << /Illustrator << /Private (Ottoline Quaife) >> >> >>",
		/* 21 */ "<< /Type /Annot /Subtype /Link /Rect [72 560 200 575] /A << /S /JavaScript /JS (app.alert('Ottoline Quaife')) >> >>",
		/* 22 */ "<< /Type /Filespec /F (ottoline.txt) /EF << /F 27 0 R >> >>",
		/* 23 */ "<< /Type /Annot /Subtype /Widget /FT /Tx /T (owner) /V (Ottoline Quaife) /Rect [300 700 500 720] /AP << /N 24 0 R >> /P 3 0 R /F 4 >>",
		/* 24 */ pdfStreamObj("/Type /XObject /Subtype /Form /BBox [0 0 200 20] /Resources << /Font << /F1 5 0 R >> >>", "BT /F1 10 Tf 2 5 Td (Ottoline Quaife) Tj ET"),
		/* 25 */ "<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica-Oblique >>",
		/* 26 */ "<< /Type /StructElem /S /Figure /Alt (Photo of Ottoline Quaife) /P 17 0 R >>",
		/* 27 */ pdfStreamObj("/Type /EmbeddedFile", "Ottoline Quaife tax file"),
		/* 28 */ pdfStreamObj("", "BT /F1 12 Tf 72 720 Td (Page two for Peregrine Thistlewood) Tj ET"),
		/* 29 */ "<< /Title (Ottoline Quaife contract) /Author (Peregrine Thistlewood) /Subject (For Ottoline Quaife) /Keywords (Ottoline Quaife, Peregrine Thistlewood) /Producer (handmade) >>",
	)
}

var (
	pdfStreamRe = regexp.MustCompile(`(?s)stream\r?\n(.*?)endstream`)
	pdfStringRe = regexp.MustCompile(`(?s)<([0-9A-Fa-f\s]+)>|\(((?:\\.|[^\\)])*)\)`)
)

// pdfDump returns a PDF as text a secret can be searched for in: the file,
// every stream inflated, and every string decoded.
func pdfDump(data []byte) string {
	parts := []string{string(data)}
	for _, m := range pdfStreamRe.FindAllSubmatch(data, -1) {
		if r, err := zlib.NewReader(bytes.NewReader(m[1])); err == nil {
			if b, err := io.ReadAll(r); err == nil {
				parts = append(parts, string(b))
			}
		}
	}
	var out strings.Builder
	for _, p := range parts {
		out.WriteString(p + "\n")
		for _, m := range pdfStringRe.FindAllStringSubmatch(p, -1) {
			var raw []byte
			if m[1] != "" {
				raw, _ = hex.DecodeString(strings.Join(strings.Fields(m[1]), ""))
			} else {
				raw = []byte(strings.NewReplacer(`\(`, "(", `\)`, ")", `\\`, `\`).Replace(m[2]))
			}
			if bytes.HasPrefix(raw, []byte{0xfe, 0xff}) && len(raw)%2 == 0 {
				u := make([]uint16, 0, len(raw)/2)
				for i := 2; i < len(raw); i += 2 {
					u = append(u, uint16(raw[i])<<8|uint16(raw[i+1]))
				}
				out.WriteString(string(utf16.Decode(u)) + "\n")
				continue
			}
			for _, c := range raw {
				out.WriteRune(rune(c))
			}
			out.WriteString("\n")
		}
	}
	return out.String()
}

func pdfEngine(t *testing.T, names ...string) *Engine {
	t.Helper()
	e, err := New(testKey(t), defaultProfile(t), wordDetector{names: names}, newMemRegistry())
	if err != nil {
		t.Fatal(err)
	}
	return e
}

func anonymisePDF(t *testing.T, e *Engine, data []byte) *Output {
	t.Helper()
	f := File{Name: "file.pdf", Data: data}
	if errs := e.Prepare(ctx, []File{f}); errs[0] != nil {
		t.Fatalf("preparing: %v", errs[0])
	}
	out, err := e.Anonymise(ctx, f)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// readBack reads an anonymised PDF as the anonymiser does.
func readBack(t *testing.T, data []byte) *pdfDoc {
	t.Helper()
	d, err := readPDF(ctx, data)
	if err != nil {
		t.Fatalf("reading the anonymised PDF: %v", err)
	}
	t.Cleanup(d.s.close)
	return d
}

func TestPDFLosesEverySecret(t *testing.T) {
	out := anonymisePDF(t, pdfEngine(t, "Ottoline Quaife", "Peregrine Thistlewood"), kitchenSink())
	dump := pdfDump(out.Data)
	for _, s := range []string{"Ottoline", "Quaife", "Peregrine", "Thistlewood", "ottoline", "quaife", "0412 345 678",
		"acme-plumbing", "SECRETPIXELS", "tax file", "app.alert", "who =", "Illustrator", "xmpmeta"} {
		if strings.Contains(dump, s) {
			t.Errorf("%q kept", s)
		}
	}
	// What says nothing about anyone stays, and the fakes are in place.
	d := readBack(t, out.Data)
	if len(d.pages) != 2 {
		t.Fatalf("%d pages", len(d.pages))
	}
	page1, page2 := string(d.pages[0].text), string(d.pages[1].text)
	for _, s := range []string{"Hello ", ", welcome.", "ID: ", "Phone ", "hidden note for ", "Form: "} {
		if !strings.Contains(page1, s) {
			t.Errorf("page 1 lost %q:\n%s", s, page1)
		}
	}
	if !strings.HasPrefix(page2, "Page two for ") || len(page2) <= len("Page two for ") {
		t.Errorf("page 2 = %q", page2)
	}
	// The colour and the invisibility of the text replaced are kept.
	if !strings.Contains(dump, "0 0 1 rg") || !strings.Contains(dump, "3 Tr") {
		t.Error("lost the text's colour or render mode")
	}
	// The links to the web and to page 2 are kept; the script's is not.
	links := d.pages[0].links
	if len(links) != 2 || links[0].uri == "" || !strings.HasPrefix(links[0].uri, "mailto:") || links[1].page != 1 {
		t.Errorf("links = %+v", links)
	}
	if len(d.strings) == 0 {
		t.Error("no strings read")
	}
	for _, want := range []string{"JavaScript", "XMP metadata", "application data", "embedded files", "page thumbnails", "structure tree"} {
		if !slices.Contains(out.DroppedParts, want) {
			t.Errorf("dropped parts %v lack %s", out.DroppedParts, want)
		}
	}
	// The bookmark, the metadata and the alt text were rewritten, not
	// dropped.
	for _, s := range []string{"Notes for ", " contract", "For ", "'s photo"} {
		if !strings.Contains(dump, s) {
			t.Errorf("%q gone", s)
		}
	}
}

// PDFs printed by Chrome and by macOS embed subsets of their fonts: a fake
// is drawn in the standard font nearest each.
func TestPDFFromRealPrinters(t *testing.T) {
	for _, c := range []struct {
		file  string
		fonts []string
		keep  []string
	}{
		{"chrome-invoice.pdf", []string{"/BaseFont/Times-Bold", "/BaseFont/Times-Roman"}, []string{"Invoice INV-2042", "Pipe repair"}},
		{"quartz-notes.pdf", []string{"/BaseFont/Courier"}, []string{"Meeting notes", "Attendees: "}},
	} {
		data, err := os.ReadFile("testdata/pdf/" + c.file)
		if err != nil {
			t.Fatal(err)
		}
		out := anonymisePDF(t, pdfEngine(t, "Sarah Lane", "Ravi Nair", "Zoë Müller-Lüdenscheidt"), data)
		dump := pdfDump(out.Data)
		for _, s := range []string{"Sarah Lane", "Ravi", "Nair", "Müller", "0412 345 678", "9123 4567", "lane-plumbing", "14 Smith Street"} {
			if strings.Contains(dump, s) {
				t.Errorf("%s: %q kept", c.file, s)
			}
		}
		for _, f := range c.fonts {
			if !strings.Contains(dump, f) {
				t.Errorf("%s: no %s", c.file, f)
			}
		}
		text := string(readBack(t, out.Data).pages[0].text)
		for _, s := range c.keep {
			if !strings.Contains(text, s) {
				t.Errorf("%s: lost %q:\n%s", c.file, s, text)
			}
		}
	}
}

func encryptPDF(t *testing.T, data []byte, user, owner string) []byte {
	t.Helper()
	var b bytes.Buffer
	conf := model.NewStatelessConfiguration()
	conf.UserPW, conf.OwnerPW, conf.EncryptUsingAES, conf.EncryptKeyLength = user, owner, true, 256
	if err := api.Encrypt(context.Background(), bytes.NewReader(data), &b, conf); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func TestPDFErrors(t *testing.T) {
	text := "BT /F1 12 Tf 72 720 Td (Invoice for Ottoline Quaife) Tj ET"
	scan := "q 612 0 0 792 0 0 cm /Im1 Do Q "
	long := strings.Repeat("Terms and conditions apply to every order. ", 3)
	pages := []string{"<< /Type /Catalog /Pages 2 0 R >>", ""}
	var kids []string
	for i := 0; i < pdfMaxPages+1; i++ {
		kids = append(kids, fmt.Sprintf("%d 0 R", i+3))
		pages = append(pages, "<< /Type /Page /Parent 2 0 R /MediaBox [0 0 10 10] >>")
	}
	pages[1] = fmt.Sprintf("<< /Type /Pages /Kids [%s] /Count %d >>", strings.Join(kids, " "), len(kids))
	for name, c := range map[string]struct {
		data []byte
		want string
	}{
		"not a PDF":                {[]byte("%PDF-1.7\nnothing here"), "not a PDF, or a damaged one"},
		"a password":               {encryptPDF(t, onePagePDF(text), "open", "owner"), "password-protected"},
		"a scan":                   {onePagePDF(scan), "it is a scan"},
		"a scan with its OCR text": {onePagePDF(scan + "BT /F1 10 Tf 3 Tr 72 720 Td (" + long + ") Tj ET"), "it is a scan"},
		"too many pages":           {buildPDF("", pages...), "at most 2000 are read"},
	} {
		_, err := pdfEngine(t, "Ottoline Quaife").Anonymise(ctx, File{Name: "x.pdf", Data: c.data})
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: err = %v, want %q", name, err, c.want)
		}
	}
	// A page with a big picture and text over it is not a scan; nor is a
	// PDF that is encrypted but opens without a password, which comes back
	// unencrypted.
	for name, data := range map[string][]byte{
		"a brochure":         onePagePDF(scan + "BT /F1 10 Tf 72 720 Td (" + long + ") Tj ET " + text),
		"permissions only":   encryptPDF(t, onePagePDF(text), "", "owner"),
		"no text whatsoever": onePagePDF("q 1 0 0 1 0 0 cm Q"),
	} {
		out, err := pdfEngine(t, "Ottoline Quaife").Anonymise(ctx, File{Name: "x.pdf", Data: data})
		if err != nil {
			t.Errorf("%s: %v", name, err)
			continue
		}
		if dump := pdfDump(out.Data); strings.Contains(dump, "Ottoline") || strings.Contains(dump, "/Encrypt") || strings.Contains(dump, "SECRETPIXELS") {
			t.Errorf("%s: kept a secret or its encryption", name)
		}
	}
	// An engine that has stopped says why.
	stopped, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := pdfEngine(t).Anonymise(stopped, File{Name: "x.pdf", Data: onePagePDF(text)}); err == nil {
		t.Error("a cancelled run went on")
	}
}

// go-pdfium passes FPDFFormObj_RemoveObject its arguments in the wrong
// order, which removeFromForm undoes. If go-pdfium fixes that, this fails,
// and removeFromForm must pass them straight.
func TestPDFiumRemovesFromForms(t *testing.T) {
	data := buildPDF("",
		"<< /Type /Catalog /Pages 2 0 R >>",
		"<< /Type /Pages /Kids [3 0 R] /Count 1 >>",
		"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] /Contents 4 0 R /Resources << /XObject << /Fm1 5 0 R >> >> >>",
		pdfStreamObj("", "/Fm1 Do"),
		pdfStreamObj("/Type /XObject /Subtype /Form /BBox [0 0 100 100] /Resources << /Font << /F1 6 0 R >> >>", "BT /F1 12 Tf (x) Tj ET"),
		"<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>",
	)
	s, err := openPDFium(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer s.close()
	d := &pdfDoc{s: s}
	doc, err := s.p.FPDF_LoadMemDocument64(&requests.FPDF_LoadMemDocument64{Data: &data})
	if err != nil {
		t.Fatal(err)
	}
	d.doc = doc.Document
	if err := d.withPage(0, func(ref requests.Page) error {
		obj, form, err := d.find(ref, []int{0, 0})
		if err != nil {
			return err
		}
		if err := s.removeFromForm(form, obj); err != nil {
			return err
		}
		n, err := d.countObjects(ref, form)
		if err == nil && n != 0 {
			err = fmt.Errorf("%d objects left in the form", n)
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
}

func TestPDFStandardFonts(t *testing.T) {
	for _, c := range []struct {
		font pdfFont
		want string
	}{
		{pdfFont{name: "BAAAAA+Georgia"}, "Times-Roman"},
		{pdfFont{name: "CAAAAA+Georgia-Bold"}, "Times-Bold"},
		{pdfFont{name: "TimesNewRomanPS-ItalicMT"}, "Times-Italic"},
		{pdfFont{name: "AAAAAB+Monaco"}, "Courier"},
		{pdfFont{name: "Consolas", weight: 700}, "Courier-Bold"},
		{pdfFont{name: "Arial-BoldItalicMT"}, "Helvetica-BoldOblique"},
		{pdfFont{name: "OpenSans-Regular"}, "Helvetica"},
		{pdfFont{name: "NotoSans-Regular", family: "Noto Sans"}, "Helvetica"},
		{pdfFont{name: "F1", flags: pdfFontSerif | pdfFontItalic}, "Times-Italic"},
		{pdfFont{name: "F2", flags: pdfFontFixedPitch | pdfFontForceBold}, "Courier-Bold"},
		{pdfFont{name: "F3", italicAngle: -12}, "Helvetica-Oblique"},
		{pdfFont{name: "ABCDEFG+Bodoni"}, "Times-Roman"}, // not a subset tag: still a serif name
	} {
		if got := c.font.standard(); got != c.want {
			t.Errorf("%+v: %s, want %s", c.font, got, c.want)
		}
	}
	for s, want := range map[string]bool{"Zoë Müller · €5 – “ok”": false, "李雷": true, "tab\there": true} {
		if _, bad := notWinAnsi(s); bad != want {
			t.Errorf("notWinAnsi(%q) = %v", s, bad)
		}
	}
}

// A character belongs to the smallest text object holding its centre; a
// space PDFium adds between two characters of one object belongs to it.
func TestPDFOwners(t *testing.T) {
	box := func(l, b, r, t float64) pdfRect { return pdfRect{l, b, r, t} }
	page := &pdfPage{
		text: []rune("ab cd"),
		objs: []pdfObj{
			{text: "ab cd", box: box(0, 0, 100, 10)},
			{text: "cd", box: box(30, 0, 50, 10)},
			{image: true, box: box(0, 0, 1000, 1000)},
		},
	}
	if _, err := page.own(ctx, []pdfRect{box(0, 0, 10, 10), box(10, 0, 20, 10), {}, box(30, 0, 40, 10), box(40, 0, 50, 10)}); err != nil {
		t.Fatal(err)
	}
	if want := []int{1, 1, 0, 2, 2}; !slices.Equal(page.owner, want) {
		t.Errorf("owners = %v, want %v", page.owner, want)
	}
	if acc := page.accounting(); acc[0] || !acc[1] || acc[2] {
		t.Error("the big object's text is not all its characters; the small one's is; an image accounts for nothing")
	}
	page.text = []rune("ab c")
	page.objs = page.objs[:1]
	page.objs[0].text = "ab c"
	if _, err := page.own(ctx, []pdfRect{box(0, 0, 10, 10), box(10, 0, 20, 10), {}, box(30, 0, 40, 10)}); err != nil {
		t.Fatal(err)
	}
	if want := []int{1, 1, 1, 1}; !slices.Equal(page.owner, want) {
		t.Errorf("owners = %v, want %v: an added space between one object's characters is its", page.owner, want)
	}
}

// A page of text objects that each cover it whole fills every cell of
// own's grid, so the grid's entries are capped while they are inserted:
// a small file of them must not multiply into many times its size.
func TestPDFGridBudget(t *testing.T) {
	big := pdfRect{0, 0, 612, 792}
	objs := make([]pdfObj, pdfMaxGrid/100+2)
	for k := range objs {
		objs[k] = pdfObj{text: "x", box: big}
	}
	page := &pdfPage{text: []rune("x"), objs: objs}
	_, err := page.own(ctx, []pdfRect{big})
	if err == nil || !strings.Contains(err.Error(), "grid cells") {
		t.Errorf("err = %v, want a grid budget error", err)
	}
	// A page of ordinary objects, each in one cell, is well inside it.
	page = &pdfPage{text: []rune("ab"), objs: []pdfObj{
		{text: "a", box: pdfRect{0, 0, 10, 10}},
		{text: "b", box: pdfRect{20, 0, 30, 10}},
	}}
	if _, err := page.own(ctx, []pdfRect{{0, 0, 10, 10}, {20, 0, 30, 10}}); err != nil {
		t.Errorf("ordinary page: %v", err)
	}
}

// plan stops at its deadline rather than walking every match's characters
// for every object the page's text does not account for, however long
// that walk would take.
func TestPDFPlanDeadline(t *testing.T) {
	rep := testReplacer(t, map[string]string{"Ann Lee": "Jo Bloggs"})
	text := []rune(strings.Repeat("Ann Lee ", 64)) // many matches
	objs := make([]pdfObj, 64)
	for k := range objs {
		objs[k] = pdfObj{text: "Ann Lee"} // unaccounted: none of it is page text
	}
	page := &pdfPage{text: text, objs: objs}
	page.owner = make([]int, len(text)) // no character owned
	cctx, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := page.plan(cctx, rep, nil); err == nil {
		t.Error("a cancelled context did not stop plan")
	}
}

// A form whose /Contents is an array of streams may decode to many times
// the file's size in total; the total is capped however many streams hold
// it, and across forms, as one stream alone always was.
func TestPDFFormContentBudget(t *testing.T) {
	stream := func(dict string, raw []byte, filtered bool) string {
		if filtered {
			var b bytes.Buffer
			w := zlib.NewWriter(&b)
			_, _ = w.Write(raw)
			_ = w.Close()
			raw = b.Bytes()
			dict += " /Filter /FlateDecode"
		}
		return fmt.Sprintf("<< %s /Length %d >>\nstream\n", dict, len(raw)) + string(raw) + "\nendstream"
	}
	one := make([]byte, pdfMaxFormContent/3+4096)
	// Three streams, each just under pdfMaxDecoded and Flate-packed, exceed
	// the per-form total once the third is appended.
	refs := "5 0 R 6 0 R 7 0 R"
	data := buildPDF("",
		"<< /Type /Catalog /Pages 2 0 R >>",
		"<< /Type /Pages /Kids [3 0 R] /Count 1 >>",
		"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] >>",
		pdfStreamObj("/Type /XObject /Subtype /Form /BBox [0 0 100 20] /Contents ["+refs+"]", ""),
		stream("", one, true),
		stream("", one, true),
		stream("", one, true),
	)
	fixed, err := pdfFinish(ctx, data, testReplacer(t, nil), nil, &Output{})
	if err == nil || !strings.Contains(err.Error(), "in total") {
		t.Errorf("err = %v, want a form content budget error", err)
	}
	_ = fixed
}

// A match across two text objects puts its fake in the first and takes the
// rest out of the second; an object the page's text does not account for
// is rewritten from its own text; a fake the standard fonts cannot draw
// fails the page.
func TestPDFPlan(t *testing.T) {
	rep := testReplacer(t, map[string]string{"Ottoline Quaife": "Ann Lee", "Wendy Moss": "李雷"})
	page := &pdfPage{
		text:  []rune("Hi Ottoline Quaife!\r\nWendy"),
		owner: []int{1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 2, 2, 2, 2, 2, 2, 2, 0, 0, 4, 4, 4, 4, 4},
		objs: []pdfObj{
			{text: "Hi Ottoline "}, {text: "Quaife!"}, {image: true}, {text: "Wendy"}, {text: "Ottoline Quaife hidden"},
		},
		marks: []pdfMark{{value: "photo of Ottoline Quaife"}, {value: "nothing"}},
	}
	pl, err := page.plan(ctx, rep, nil)
	if err != nil {
		t.Fatal(err)
	}
	want := map[int]string{0: "Hi Ann Lee", 1: "!", 4: "Ann Lee hidden"}
	if len(pl.text) != len(want) {
		t.Errorf("text = %v, want %v", pl.text, want)
	}
	for k, v := range want {
		if pl.text[k] != v {
			t.Errorf("object %d = %q, want %q", k, pl.text[k], v)
		}
	}
	if len(pl.marks) != 1 || pl.marks[0] != "photo of Ann Lee" {
		t.Errorf("marks = %v", pl.marks)
	}
	// An object drawn again as it is keeps the spaces between its words,
	// which its own text (as PDFium gives it) may lack.
	page.text = []rune("Hi Ottoline Quaife!\r\nWe dy")
	page.owner = []int{1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 2, 2, 2, 2, 2, 2, 2, 0, 0, 4, 4, 4, 4, 4} // the space, as own gives it
	page.objs[3].text = "Wedy"
	pl, err = page.plan(ctx, rep, map[int]bool{3: true})
	if err != nil || pl.text[3] != "We dy" {
		t.Errorf("redrawn as %q, %v", pl.text[3], err)
	}
	// A match whose first part is in an object the page's text does not
	// account for takes its fake there.
	odd := &pdfPage{
		text:  []rune("Ottoline Quaife!"),
		owner: []int{1, 1, 1, 1, 1, 1, 1, 1, 1, 2, 2, 2, 2, 2, 2, 2},
		objs:  []pdfObj{{text: "By Ottoline "}, {text: "Quaife!"}},
	}
	if pl, err := odd.plan(ctx, rep, nil); err != nil || pl.text[0] != "By Ann Lee" || pl.text[1] != "!" {
		t.Errorf("text = %v, %v", pl.text, err)
	}
	page.objs[3].text = "Wendy Moss"
	page.text = []rune("Hi Ottoline Quaife!\r\nWendy Moss")
	page.owner = append(page.owner, 4, 4, 4, 4, 4)
	if _, err := page.plan(ctx, rep, nil); err == nil || !strings.Contains(err.Error(), "U+674E") {
		t.Errorf("err = %v", err)
	}
}

func TestPDFMatrices(t *testing.T) {
	m := mulMatrix(pdfMatrix(2, 0, 0, 2, 0, 0), pdfMatrix(1, 0, 0, 1, 10, 20))
	if x, y := transformPoint(1, 1, m); x != 12 || y != 22 {
		t.Errorf("point = %v,%v", x, y)
	}
	r := transformRect(pdfRect{0, 0, 1, 2}, pdfMatrix(0, 1, -1, 0, 0, 0)) // a quarter turn
	if r != (pdfRect{-2, 0, 0, 1}) {
		t.Errorf("rect = %+v", r)
	}
	if a := intersect(pdfRect{0, 0, 10, 10}, pdfRect{5, 5, 20, 20}).area(); a != 25 {
		t.Errorf("area = %v", a)
	}
}

func pdfMatrix(a, b, c, d, e, f float32) structs.FPDF_FS_MATRIX {
	return structs.FPDF_FS_MATRIX{A: a, B: b, C: c, D: d, E: e, F: f}
}

// The same PDF anonymised with the same key comes out byte for byte the
// same, though PDFium and pdfcpu would each put something random in it.
func TestPDFIsDeterministic(t *testing.T) {
	a := anonymisePDF(t, pdfEngine(t, "Ottoline Quaife", "Peregrine Thistlewood"), kitchenSink())
	b := anonymisePDF(t, pdfEngine(t, "Ottoline Quaife", "Peregrine Thistlewood"), kitchenSink())
	if !bytes.Equal(a.Data, b.Data) {
		t.Error("two runs differ")
	}
}

// PDFium can write a trailer pointing to an information dictionary it left
// out (seen with a file updated incrementally, whose offsets were a little
// off). The output gets one.
func TestPDFMissingInfo(t *testing.T) {
	data := buildPDF("/Info 9 0 R",
		"<< /Type /Catalog /Pages 2 0 R >>",
		"<< /Type /Pages /Kids [3 0 R] /Count 1 >>",
		"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] >>",
	)
	fixed, err := pdfFinish(ctx, data, testReplacer(t, map[string]string{"x": "y"}), nil, &Output{})
	if err != nil {
		t.Fatal(err)
	}
	c, err := readPDFContext(ctx, fixed)
	if err != nil {
		t.Fatal(err)
	}
	if c.Info == nil {
		t.Fatal("no information dictionary")
	}
	if d, err := c.DereferenceDict(*c.Info); err != nil || d == nil {
		t.Errorf("the trailer points to a missing information dictionary: %v", err)
	}
}

// checkPDF finds what PDFium wrote back wrong: an object kept whose text
// comes back different is to be drawn again; new text not there, or text
// nobody wrote, fails the file.
func TestPDFCheck(t *testing.T) {
	data := onePagePDF("BT /F1 12 Tf 72 720 Td (Terms apply) Tj ET BT /F1 12 Tf 72 700 Td (Thanks) Tj ET")
	read := func() []pdfPage { return readBack(t, data).pages }
	none := []pdfPlan{{text: map[int]string{}}}
	if _, redo, err := checkPDF(ctx, data, read(), none); err != nil || len(redo) != 0 {
		t.Fatalf("redo %v, err %v", redo, err)
	}
	pages := read()
	pages[0].objs[1].text = "Thanks!" // as if PDFium wrote it back as "Thanks"
	if _, redo, err := checkPDF(ctx, data, pages, none); err != nil || !redo[0][1] || len(redo[0]) != 1 {
		t.Errorf("redo %v, err %v", redo, err)
	}
	if _, _, err := checkPDF(ctx, data, read(), []pdfPlan{{text: map[int]string{0: "Ann Lee"}}}); err == nil || !strings.Contains(err.Error(), "new text is not there") {
		t.Errorf("err = %v", err)
	}
	pages = read()
	pages[0].objs = pages[0].objs[:1]
	if _, _, err := checkPDF(ctx, data, pages, none); err == nil || !strings.Contains(err.Error(), "not written") {
		t.Errorf("err = %v", err)
	}
}

// An image drawn inline, in a page's content, is replaced on a page with no
// match too, though only that page is written again for it.
func TestPDFInlineImage(t *testing.T) {
	data := onePagePDF("BT /F1 12 Tf 72 720 Td (No names here) Tj ET q 20 0 0 20 72 600 cm BI /W 2 /H 2 /CS /RGB /BPC 8 ID SECRETPIXELS\nEI Q")
	out := anonymisePDF(t, pdfEngine(t, "Ottoline Quaife"), data)
	if dump := pdfDump(out.Data); strings.Contains(dump, "SECRETPIXELS") || !strings.Contains(dump, "No names here") {
		t.Error("kept the inline image's pixels, or lost the text")
	}
}

// formsPDF has a person's name in marked content inside a form (alt text
// on a drawing, no text near it), and in a filled field whose appearance
// draws the name in two text objects and alt text of its own. Flattened,
// the field is the page's last object, a form inside a form.
func formsPDF() []byte {
	return buildPDF("",
		"<< /Type /Catalog /Pages 2 0 R >>",
		"<< /Type /Pages /Kids [3 0 R] /Count 1 >>",
		"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] /Contents 4 0 R /Resources << /Font << /F1 5 0 R >> /XObject << /Fm1 6 0 R >> >> /Annots [7 0 R] >>",
		pdfStreamObj("", "BT /F1 12 Tf 72 720 Td (Notes) Tj ET q 1 0 0 1 72 600 cm /Fm1 Do Q"),
		"<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>",
		pdfStreamObj("/Type /XObject /Subtype /Form /BBox [0 0 100 20] /Resources << >>",
			"/Span << /ActualText (Ottoline Quaife's signature) >> BDC 0 0 50 10 re f EMC"),
		"<< /Type /Annot /Subtype /Widget /FT /Tx /T (owner) /Rect [300 700 500 720] /AP << /N 8 0 R >> /F 4 >>",
		pdfStreamObj("/Type /XObject /Subtype /Form /BBox [0 0 200 20] /Resources << /Font << /F1 5 0 R >> >>",
			"BT /F1 10 Tf 2 5 Td (Ottoline ) Tj ET BT /F1 10 Tf 39.24 5 Td (Quaife) Tj ET /Span << /Alt (Ottoline Quaife) >> BDC 0 0 1 1 re f EMC"),
	)
}

// Marked content in a form is written back (PDFium writes a form only when
// an object leaves it), and a field whose name ends up in one text object
// out of two is written whole.
func TestPDFForms(t *testing.T) {
	out := anonymisePDF(t, pdfEngine(t, "Ottoline Quaife"), formsPDF())
	dump := pdfDump(out.Data)
	for _, s := range []string{"Ottoline", "Quaife"} {
		if strings.Contains(dump, s) {
			t.Errorf("%q kept", s)
		}
	}
	if !strings.Contains(dump, "'s signature") || !strings.Contains(dump, "Notes") {
		t.Error("lost the rest of the alt text, or the page's text")
	}
}

// Strings a reader looks things up by are not rewritten, though they hold
// a value found elsewhere.
func TestPDFStructuralStrings(t *testing.T) {
	data := buildPDF("/Info 4 0 R",
		"<< /Type /Catalog /Pages 2 0 R /Lang (Adobe) >>",
		"<< /Type /Pages /Kids [3 0 R] /Count 1 >>",
		"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] /Resources << /Font << /F1 5 0 R >> >> >>",
		"<< /Title (Made with Adobe) >>",
		"<< /Type /Font /Subtype /Type0 /BaseFont /X /Encoding /Identity-H /DescendantFonts [6 0 R] >>",
		"<< /Type /Font /Subtype /CIDFontType2 /BaseFont /X /CIDSystemInfo << /Registry (Adobe) /Ordering (Identity) /Supplement 0 >> >>",
	)
	fixed, err := pdfFinish(ctx, data, testReplacer(t, map[string]string{"Adobe": "Acme", "Identity": "Ident"}), nil, &Output{})
	if err != nil {
		t.Fatal(err)
	}
	dump := pdfDump(fixed)
	if !strings.Contains(dump, "/Registry(Adobe)") && !strings.Contains(dump, "/Registry (Adobe)") ||
		!strings.Contains(dump, "Identity") || !strings.Contains(dump, "Made with Acme") {
		t.Errorf("registry, ordering or title wrong:\n%s", dump)
	}
}

// A scan is found though its page's box does not start at 0,0 and the page
// is turned.
func TestPDFScanOffsetPage(t *testing.T) {
	data := buildPDF("",
		"<< /Type /Catalog /Pages 2 0 R >>",
		"<< /Type /Pages /Kids [3 0 R] /Count 1 >>",
		"<< /Type /Page /Parent 2 0 R /MediaBox [400 400 1012 1192] /Rotate 90 /Contents 4 0 R /Resources << /XObject << /Im1 5 0 R >> >> >>",
		pdfStreamObj("", "q 612 0 0 792 400 400 cm /Im1 Do Q"),
		pdfStreamObj("/Type /XObject /Subtype /Image /Width 2 /Height 2 /ColorSpace /DeviceRGB /BitsPerComponent 8", "SECRETPIXELS"),
	)
	if _, err := pdfEngine(t).Anonymise(ctx, File{Name: "x.pdf", Data: data}); err == nil || !strings.Contains(err.Error(), "it is a scan") {
		t.Errorf("err = %v", err)
	}
}
