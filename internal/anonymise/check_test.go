package anonymise

import (
	"encoding/base64"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"
)

// The check lists the output's values the original holds as they are:
// addresses, long numbers (not dates), names, then capitalised words not
// already in a listed name. A value inside a longer word does not count.
func TestKept(t *testing.T) {
	before := "Edda Pellow of Lumen Fabrication, 0412 345 678, edda@acme.example, " +
		"www.acme.example/x, due 30/10/2026, ref 7700012345. Laneway. Thanks. Sent 2026-10-30 14:38:29"
	after := "Ann Lee of Lumen Fabrication, 0499 000 111, ann@bolt.example, " +
		"www.acme.example/x, due 30/10/2026, ref 7700012345 and 7700012345. Lane. Thanks. Sent 2026-10-30 14:38:29"
	want := []Kept{
		{KeptURL, "www.acme.example/x", 1},
		{KeptNumber, "7700012345", 2},
		{KeptName, "Lumen Fabrication", 1},
		{KeptWord, "Thanks", 1},
		{KeptWord, "Sent", 1},
	}
	if got := kept(before, after); !reflect.DeepEqual(got, want) {
		t.Errorf("kept = %+v\nwant %+v", got, want)
	}
	if got := kept("Sarah", "Sarah's"); len(got) != 0 {
		t.Errorf("kept a value inside a word: %+v", got)
	}
	if got := kept("Call sarah@x.example", "Call sarah@x.example"); len(got) != 2 || got[0] != (Kept{KeptEmail, "sarah@x.example", 1}) {
		t.Errorf("kept = %+v", got)
	}
}

// The word index finds exactly what a scan of the whole text finds.
func TestWordIndex(t *testing.T) {
	text := "Mary O'Brien, 0412 345 678; +61 2 9123 4567 www.acme.example/x Sarah's (08) 9123 4567 ÖZTÜRK_X ü"
	x := newWordIndex(text)
	for _, v := range []string{
		"Mary O'Brien", "O'Brien", "Brien", "Mary", "Mar", "0412 345 678", "345 678", "345", "45 678",
		"+61 2 9123 4567", "61 2", "www.acme.example/x", "acme.example", "Sarah", "Sarah's", "(08) 9123 4567",
		"9123 4567", "ÖZTÜRK", "ÖZTÜRK_X", "ü", "---", "", "Mary O'Brien, 0412 345 678; +61 2 9123 4567 www.acme.example/x Sarah's (08) 9123 4567 ÖZTÜRK_X ü",
	} {
		if got, want := x.holds(v), holdsWord(text, v); got != want {
			t.Errorf("holds(%q) = %v, holdsWord %v", v, got, want)
		}
	}
	// A large file with many distinct values is quick: one pass to index.
	var b strings.Builder
	for i := range 50000 {
		fmt.Fprintf(&b, "Name%d Surname%d ref %08d\n", i, i, i)
	}
	big := b.String()
	start := time.Now()
	if got := kept(big, big); len(got) == 0 {
		t.Error("kept nothing")
	}
	if d := time.Since(start); d > 30*time.Second {
		t.Errorf("kept took %v", d)
	}
}

// Every format is read as the anonymiser reads it: an email's headers,
// body and readable attachments too, not an image's bytes.
func TestReadText(t *testing.T) {
	email := "From: Sarah Lane <sarah@acme.example>\r\nTo: Tom <tom@bolt.example>\r\nSubject: Files\r\nMIME-Version: 1.0\r\n" +
		"Content-Type: multipart/mixed; boundary=\"B\"\r\n\r\n" +
		"--B\r\nContent-Type: text/plain\r\n\r\nHi Tom, from Sarah\r\n" +
		"--B\r\nContent-Type: text/csv; name=\"people.csv\"\r\nContent-Disposition: attachment; filename=\"people.csv\"\r\n\r\nName\r\nWendy Moss\r\n" +
		"--B\r\nContent-Type: image/png; name=\"x.png\"\r\nContent-Disposition: attachment; filename=\"x.png\"\r\nContent-Transfer-Encoding: base64\r\n\r\n" +
		base64.StdEncoding.EncodeToString([]byte("\x89PNG SECRETPIXELS")) + "\r\n--B--\r\n"
	for name, c := range map[string]struct {
		data string
		want []string
	}{
		"a.txt":  {"Hello Sarah Lane", []string{"Hello Sarah Lane"}},
		"a.csv":  {"Name\nSarah Lane\n", []string{"Sarah Lane"}},
		"a.rtf":  {`{\rtf1\ansi Dear Sarah Lane\par}`, []string{"Dear Sarah Lane"}},
		"a.pdf":  {string(onePagePDF("BT /F1 12 Tf 72 720 Td (Invoice for Sarah Lane) Tj ET")), []string{"Invoice for Sarah Lane"}},
		"a.xlsx": {string(minimalXLSX(t, `<workbook `+xNS+` `+rNS+`><sheets><sheet name="Sheet1" sheetId="1" r:id="rId1"/></sheets></workbook>`, `<worksheet `+xNS+`><sheetData><row r="1"><c r="A1" t="inlineStr"><is><t>Sarah Lane</t></is></c></row></sheetData></worksheet>`)), []string{"Sarah Lane"}},
		"a.eml":  {email, []string{"Sarah Lane <sarah@acme.example>", "Hi Tom, from Sarah", "Wendy Moss"}},
		"a.docx": {string(buildDOCX(t, [][2]string{
			{"[Content_Types].xml", xmlH + `<Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types"><Default Extension="xml" ContentType="application/xml"/><Default Extension="rels" ContentType="application/vnd.openxmlformats-package.relationships+xml"/></Types>`},
			{"_rels/.rels", xmlH + `<Relationships ` + relNS + `><Relationship Id="rId1" Type="x" Target="word/document.xml"/></Relationships>`},
			{"word/document.xml", xmlH + `<w:document ` + wNS + `><w:body><w:p><w:r><w:t>Quote for Sarah Lane</w:t></w:r></w:p></w:body></w:document>`},
		})), []string{"Quote for Sarah Lane"}},
	} {
		texts, err := ReadText(ctx, File{Name: name, Data: []byte(c.data)})
		if err != nil {
			t.Errorf("%s: %v", name, err)
			continue
		}
		all := strings.Join(texts, "\n")
		for _, w := range c.want {
			if !strings.Contains(all, w) {
				t.Errorf("%s: %q missing from %q", name, w, all)
			}
		}
		if strings.Contains(all, "SECRETPIXELS") {
			t.Errorf("%s: read an image's bytes as text", name)
		}
	}
	if _, err := ReadText(ctx, File{Name: "a.xls", Data: []byte("x")}); err == nil {
		t.Error("read an .xls")
	}
	if _, err := ReadText(ctx, File{Name: "a.pdf", Data: []byte("%PDF-1.7 broken")}); err == nil {
		t.Error("read a broken PDF")
	}
}

// A name the detector missed is listed; one it found is not.
func TestUnchanged(t *testing.T) {
	e := pdfEngine(t, "Sarah Lane")
	original := File{Name: "notes.txt", Data: []byte("Sarah Lane met Tom Baker.\n")}
	out, err := e.Anonymise(ctx, original)
	if err != nil {
		t.Fatal(err)
	}
	got, err := Unchanged(ctx, original, File{Name: out.Name, Data: out.Data})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0] != (Kept{KeptName, "Tom Baker", 1}) {
		t.Errorf("unchanged = %+v", got)
	}
	if _, err := Unchanged(ctx, File{Name: "a.xls"}, File{Name: "b.txt"}); err == nil || !strings.Contains(err.Error(), "reading the original") {
		t.Errorf("err = %v", err)
	}
}
