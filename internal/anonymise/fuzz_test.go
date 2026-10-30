package anonymise

import (
	"os"
	"testing"

	"github.com/martinsuchenak/exemplar/internal/textswap"
)

// The parsers read files from anyone with a key: whatever the bytes, they
// return an error or a result, never panic.

func FuzzParseRTF(f *testing.F) {
	f.Add([]byte(wordRTF))
	f.Add([]byte(`{\rtf1\uc2 a\u-10179??\u-8704??{\pict\bin3 a}b}x}`))
	f.Add([]byte(`{\rtf1{\*\atnid A}{\*\atnauthor B}{\sp{\sn wzDescription}{\sv C}}}`))
	f.Fuzz(func(t *testing.T, data []byte) {
		d, err := parseRTF(data)
		if err != nil {
			return
		}
		_ = d.texts()
		if _, err := splice(d.data, nil); err != nil {
			t.Fatal(err)
		}
		_ = rtfSkeleton(d.data, d.toks, d.drops)
		// The whole rewrite too: an error is fine, a panic is not.
		e, err := New(testKey(t), defaultProfile(t), wordDetector{names: []string{"Sarah Lane", "B"}}, newMemRegistry())
		if err != nil {
			t.Fatal(err)
		}
		_, _ = e.Anonymise(ctx, File{Name: "f.rtf", Data: data})
	})
}

func FuzzScanXML(f *testing.F) {
	f.Add([]byte(`<w:document xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main"><w:p><w:r><w:t a='1'>x &amp; y</w:t></w:r></w:p></w:document>`))
	f.Add([]byte(`<a b="c" d='e'/><!-- x --><?pi x?>`))
	f.Fuzz(func(t *testing.T, data []byte) {
		nodes, err := scanXML(data)
		if err != nil {
			return
		}
		for _, n := range nodes {
			if n.start < 0 || n.end > len(data) || n.start > n.end {
				t.Fatalf("node outside the data: %+v", n)
			}
			for _, a := range n.attrs {
				if a.start < n.start || a.end > n.end || a.start > a.end {
					t.Fatalf("attribute outside its tag: %+v", a)
				}
			}
		}
	})
}

// Formulas and headers are parsed by hand. Whatever the text, they never
// panic, and with nothing to replace they come back as they were.
func FuzzXLSXFormula(f *testing.F) {
	for _, s := range []string{`SUM('Sarah Lane'!A1:B2)&"x ""y"""`, `[1]Acme!A1+'[2]x''y'!B1`, `T[[#This Row],[a'#b]]+[@[c]]`, `IF(#REF!,"",Jo:Summary!$A$1)`, `'`, `"`, `[`, `[1]`, `x:`} {
		f.Add(s)
	}
	none, err := textswap.New(nil)
	if err != nil {
		f.Fatal(err)
	}
	some, err := textswap.New(map[string]string{"Sarah Lane": "Ann O'Hara", "x": "y"})
	if err != nil {
		f.Fatal(err)
	}
	f.Fuzz(func(t *testing.T, s string) {
		x := &xlsxBook{sheets: []string{"Sarah Lane", "Jo", "x"}}
		if got := x.formula(none, s); got != s {
			t.Fatalf("formula(%q) = %q with nothing to replace", s, got)
		}
		if got := rewriteHeaderFooter(none, s); got != s {
			t.Fatalf("rewriteHeaderFooter(%q) = %q with nothing to replace", s, got)
		}
		_ = (&xlsxBook{sheets: []string{"Sarah Lane", "x"}}).formula(some, s)
		_ = rewriteHeaderFooter(some, s)
		_ = formulaStrings(s)
		_ = headerFooterText(s)
		_, _, _ = parseCellRef(s)
	})
}

// FuzzXLSX fuzzes a workbook's sheet and shared strings inside a valid
// package. Fuzzing the zip itself mostly exercises decompression, and a
// mutated stream can expand to the 16 MiB a part may hold, which is slow
// under the fuzzer's instrumentation.
func FuzzXLSX(f *testing.F) {
	f.Add(`<worksheet `+xNS+`><sheetData><row><c t="inlineStr"><is><t>Name</t></is></c><c t="s"><v>1</v></c></row>`+
		`<row r="2"><c r="A2" t="s"><v>0</v></c><c><f>'Sarah Lane'!A1&amp;"Sarah Lane"</f><v>3</v></c><c t="str"><v>Sarah Lane</v></c></row></sheetData>`+
		`<headerFooter><oddHeader>&amp;LSarah Lane</oddHeader></headerFooter></worksheet>`,
		`<sst `+xNS+`><si><r><t>Sa</t></r><r><t>rah Lane</t></r><rPh><t>x</t></rPh></si><si><t>Phone</t></si></sst>`)
	// One engine for every input: building one per input is too slow to
	// fuzz, and what it learns from earlier inputs does no harm.
	e, err := New(testKey(f), defaultProfile(f), wordDetector{names: []string{"Sarah Lane"}}, newMemRegistry())
	if err != nil {
		f.Fatal(err)
	}
	f.Fuzz(func(t *testing.T, sheet, shared string) {
		data := minimalXLSX(t, `<workbook `+xNS+` `+rNS+`><sheets><sheet name="Sarah Lane" sheetId="1" r:id="rId1"/></sheets></workbook>`,
			sheet, [2]string{"xl/sharedStrings.xml", shared})
		out, err := e.Anonymise(ctx, File{Name: "f.xlsx", Data: data})
		if err == nil && out == nil {
			t.Fatal("no output and no error")
		}
	})
}

// FuzzPDF runs mangled PDFs through the whole PDF handler: it must fail
// cleanly or write a PDF that opens again. PDFium reads the input in its
// sandbox, so this mostly tests the Go around it. Each run starts PDFium,
// which the fuzzer's instrumentation makes slow: a few runs a minute, so
// run it for hours, or rely on FuzzPDFObjects for the Go.
func FuzzPDF(f *testing.F) {
	f.Add(onePagePDF("BT /F1 12 Tf 72 720 Td (Invoice for Ottoline Quaife) Tj ET"))
	f.Add(kitchenSink())
	for _, name := range []string{"chrome-invoice.pdf", "quartz-notes.pdf"} {
		if data, err := os.ReadFile("testdata/pdf/" + name); err == nil {
			f.Add(data)
		}
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) > 1<<20 {
			return
		}
		e, err := New(testKey(t), defaultProfile(t), wordDetector{names: []string{"Ottoline Quaife"}}, newMemRegistry())
		if err != nil {
			t.Fatal(err)
		}
		out, err := e.Anonymise(ctx, File{Name: "f.pdf", Data: data})
		if err != nil {
			return
		}
		d, err := readPDF(ctx, out.Data)
		if err != nil {
			t.Fatalf("the anonymised PDF does not open: %v", err)
		}
		d.s.close()
	})
}

// FuzzPDFObjects runs mangled PDFs through what pdfcpu does: reading the
// strings outside the drawing, and rewriting them, repairing forms,
// replacing images, dropping parts and adding links. pdfcpu only ever reads
// what PDFium wrote, but that is still a PDF of the sender's making.
func FuzzPDFObjects(f *testing.F) {
	f.Add(onePagePDF("BT /F1 12 Tf 72 720 Td (Invoice for Ottoline Quaife) Tj ET"))
	f.Add(kitchenSink())
	rep, err := textswap.New(map[string]string{"Ottoline Quaife": "Ann Lee"})
	if err != nil {
		f.Fatal(err)
	}
	links := [][]pdfLink{{{uri: "mailto:ottoline@example.org", page: -1}, {page: 0}, {page: 7}}}
	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) > 1<<20 {
			return
		}
		if _, _, err := pdfStrings(ctx, data); err != nil {
			return
		}
		_, _ = pdfFinish(ctx, data, rep, links, &Output{})
	})
}
