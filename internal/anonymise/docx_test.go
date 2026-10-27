package anonymise

import (
	"archive/zip"
	"bytes"
	"fmt"
	"io"
	"regexp"
	"strings"
	"testing"

	"github.com/martinsuchenak/exemplar/internal/synth"
	"github.com/martinsuchenak/exemplar/internal/textswap"
)

const (
	wNS   = `xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main"`
	rNS   = `xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships"`
	relNS = `xmlns="http://schemas.openxmlformats.org/package/2006/relationships"`
	xmlH  = `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>` + "\r\n"
)

// buildDOCX zips parts in order.
func buildDOCX(t *testing.T, parts [][2]string) []byte {
	t.Helper()
	var b bytes.Buffer
	zw := zip.NewWriter(&b)
	for _, p := range parts {
		w, err := zw.Create(p[0])
		if err != nil {
			t.Fatal(err)
		}
		_, _ = w.Write([]byte(p[1]))
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

// unzip returns every entry of a zip, in order.
func unzip(t *testing.T, data []byte) ([]string, map[string]string) {
	t.Helper()
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	parts := map[string]string{}
	for _, f := range zr.File {
		rc, _ := f.Open()
		b, _ := io.ReadAll(rc)
		_ = rc.Close()
		names = append(names, f.Name)
		parts[f.Name] = string(b)
	}
	return names, parts
}

// wordDocx is shaped like a document Word saves: the same secrets in every
// place Word can hide them.
func wordDocx(t *testing.T) []byte {
	styles := xmlH + `<w:styles ` + wNS + `><w:style w:type="paragraph" w:styleId="Normal"><w:name w:val="Normal"/></w:style></w:styles>`
	theme := xmlH + `<a:theme xmlns:a="http://schemas.openxmlformats.org/drawingml/2006/main" name="Office Theme"><a:themeElements/></a:theme>`
	return buildDOCX(t, [][2]string{
		{"[Content_Types].xml", xmlH + `<Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types">` +
			`<Default Extension="rels" ContentType="application/vnd.openxmlformats-package.relationships+xml"/>` +
			`<Default Extension="xml" ContentType="application/xml"/>` +
			`<Default Extension="jpeg" ContentType="image/jpeg"/>` +
			`<Default Extension="emf" ContentType="image/x-emf"/>` +
			`<Default Extension="bin" ContentType="application/vnd.ms-office.vbaProject"/>` +
			`<Override PartName="/word/document.xml" ContentType="application/vnd.openxmlformats-officedocument.wordprocessingml.document.main+xml"/>` +
			`<Override PartName="/word/embeddings/Jane_Doe_salary.xlsx" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.sheet"/>` +
			`<Override PartName="/word/vbaProject.bin" ContentType="application/vnd.ms-office.vbaProject"/>` +
			`<Override PartName="/word/webextensions/taskpanes.xml" ContentType="application/vnd.ms-office.webextensiontaskpanes+xml"/>` +
			`</Types>`},
		{"_rels/.rels", xmlH + `<Relationships ` + relNS + `>` +
			`<Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/officeDocument" Target="word/document.xml"/>` +
			`<Relationship Id="rId2" Type="http://schemas.openxmlformats.org/package/2006/relationships/metadata/core-properties" Target="docProps/core.xml"/>` +
			`<Relationship Id="rId3" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/extended-properties" Target="docProps/app.xml"/>` +
			`<Relationship Id="rId4" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/custom-properties" Target="docProps/custom.xml"/>` +
			`<Relationship Id="rId5" Type="http://schemas.openxmlformats.org/package/2006/relationships/metadata/thumbnail" Target="docProps/thumbnail.jpeg"/>` +
			`<Relationship Id="rId6" Type="http://schemas.microsoft.com/office/2011/relationships/webextensiontaskpanes" Target="word/webextensions/taskpanes.xml"/>` +
			`</Relationships>`},
		{"word/document.xml", xmlH + `<w:document ` + wNS + ` ` + rNS +
			` xmlns:wp="http://schemas.openxmlformats.org/drawingml/2006/wordprocessingDrawing" xmlns:a="http://schemas.openxmlformats.org/drawingml/2006/main"` +
			` xmlns:v="urn:schemas-microsoft-com:vml" xmlns:o="urn:schemas-microsoft-com:office:office"><w:body>` +
			// A name split across runs, with formatting and a spell-check mark between.
			`<w:p><w:r><w:t xml:space="preserve">Dear Sa</w:t></w:r><w:proofErr w:type="spellStart"/><w:r><w:rPr><w:b/></w:rPr><w:t>rah La</w:t></w:r><w:r><w:t>ne,</w:t></w:r></w:p>` +
			// Tracked changes, with their author.
			`<w:p><w:ins w:id="1" w:author="Priya Shah" w:initials="PS" w:date="2025-01-06T09:30:00Z"><w:r><w:t xml:space="preserve">call 0412 345 678 </w:t></w:r></w:ins>` +
			`<w:del w:id="2" w:author="Priya Shah" w:date="2025-01-06T09:31:00Z"><w:r><w:delText>Sarah Lane</w:delText></w:r></w:del>` +
			`<w:commentRangeStart w:id="0"/><w:r><w:t>see note</w:t></w:r><w:commentRangeEnd w:id="0"/><w:r><w:commentReference w:id="0"/></w:r></w:p>` +
			// A link to an address, and a field linking to a URL; a merge field stays.
			`<w:p><w:hyperlink r:id="rId12" w:tooltip="Mail Sarah Lane"><w:r><w:t>sarah.lane@acme-plumbing.com.au</w:t></w:r></w:hyperlink>` +
			`<w:r><w:fldChar w:fldCharType="begin"/></w:r><w:r><w:instrText xml:space="preserve"> HYPERLINK "https://acme-plumbing.com.au/invoices?id=991" </w:instrText></w:r>` +
			`<w:r><w:fldChar w:fldCharType="separate"/></w:r><w:r><w:t>the invoice</w:t></w:r><w:r><w:fldChar w:fldCharType="end"/></w:r>` +
			`<w:fldSimple w:instr=" MERGEFIELD FirstName "><w:r><w:t>«FirstName»</w:t></w:r></w:fldSimple></w:p>` +
			// A picture with alt text, a text box and a shape's text.
			`<w:p><w:r><w:drawing><wp:inline><wp:docPr id="1" name="Picture 1" descr="Photo of Sarah Lane" title="Sarah Lane"/>` +
			`<a:graphic><a:graphicData><a:blip r:embed="rId10"/><a:p><a:r><a:t>Acme Plumbing Pty Ltd</a:t></a:r></a:p></a:graphicData></a:graphic></wp:inline></w:drawing></w:r>` +
			`<w:r><w:pict><v:shape alt="Logo of Acme Plumbing Pty Ltd"><v:imagedata r:id="rId11" o:title="Sarah Lane signature"/>` +
			`<v:textbox><w:txbxContent><w:p><w:r><w:t>Priya Shah</w:t></w:r></w:p></w:txbxContent></v:textbox></v:shape></w:pict></w:r>` +
			`<w:r><w:object><o:OLEObject ProgID="Excel.Sheet.12" r:id="rId15"/></w:object></w:r></w:p>` +
			// A tab ends a run of text: no name is matched across it.
			`<w:p><w:r><w:t>Call</w:t><w:tab/><w:t>Sarah Lane</w:t></w:r></w:p>` +
			`</w:body></w:document>`},
		{"word/_rels/document.xml.rels", xmlH + `<Relationships ` + relNS + `>` +
			`<Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/styles" Target="styles.xml"/>` +
			`<Relationship Id="rId2" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/settings" Target="settings.xml"/>` +
			`<Relationship Id="rId3" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/comments" Target="comments.xml"/>` +
			`<Relationship Id="rId4" Type="http://schemas.microsoft.com/office/2011/relationships/people" Target="people.xml"/>` +
			`<Relationship Id="rId5" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/header" Target="header1.xml"/>` +
			`<Relationship Id="rId6" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/footnotes" Target="footnotes.xml"/>` +
			`<Relationship Id="rId7" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/theme" Target="theme/theme1.xml"/>` +
			`<Relationship Id="rId8" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/customXml" Target="../customXml/item1.xml"/>` +
			`<Relationship Id="rId9" Type="http://schemas.microsoft.com/office/2006/relationships/vbaProject" Target="vbaProject.bin"/>` +
			`<Relationship Id="rId10" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/image" Target="media/image1.jpeg"/>` +
			`<Relationship Id="rId11" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/image" Target="media/image2.emf"/>` +
			`<Relationship Id="rId12" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/hyperlink" Target="mailto:sarah.lane@acme-plumbing.com.au" TargetMode="External"/>` +
			`<Relationship Id="rId15" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/package" Target="embeddings/Jane_Doe_salary.xlsx"/>` +
			`</Relationships>`},
		{"word/styles.xml", styles},
		{"word/theme/theme1.xml", theme},
		{"word/settings.xml", xmlH + `<w:settings ` + wNS + ` ` + rNS + `><w:attachedTemplate r:id="rId1"/>` +
			`<w:mailMerge><w:mainDocumentType w:val="formLetters"/><w:connectString w:val="Provider=Microsoft.ACE.OLEDB.12.0;Data Source=C:\Users\jdoe\Acme Plumbing customers.xlsx"/>` +
			`<w:query w:val="SELECT * FROM jdoe_clients"/></w:mailMerge>` +
			`<w:docVars><w:docVar w:name="client" w:val="Acme Plumbing Pty Ltd"/></w:docVars></w:settings>`},
		{"word/_rels/settings.xml.rels", xmlH + `<Relationships ` + relNS + `>` +
			`<Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/attachedTemplate" Target="file:///C:\Users\jdoe\AppData\Roaming\Microsoft\Templates\Acme%20Quote.dotm" TargetMode="External"/>` +
			`</Relationships>`},
		{"word/comments.xml", xmlH + `<w:comments ` + wNS + `><w:comment w:id="0" w:author="Sarah Lane" w:initials="SL" w:date="2025-01-06T10:00:00Z">` +
			`<w:p><w:r><w:t>Check with Priya</w:t></w:r></w:p></w:comment></w:comments>`},
		{"word/people.xml", xmlH + `<w15:people xmlns:w15="http://schemas.microsoft.com/office/word/2012/wordml"><w15:person w15:author="Sarah Lane">` +
			`<w15:presenceInfo w15:providerId="AD" w15:userId="S::sarah.lane@acme-plumbing.com.au::4a5d6e7f-1234-4cde-9abc-0123456789ab"/></w15:person></w15:people>`},
		{"word/header1.xml", xmlH + `<w:hdr ` + wNS + `><w:p><w:r><w:t>Prepared for Sarah Lane</w:t></w:r></w:p></w:hdr>`},
		{"word/footnotes.xml", xmlH + `<w:footnotes ` + wNS + `><w:footnote w:id="1"><w:p><w:r><w:t>Priya, 0412 345 678</w:t></w:r></w:p></w:footnote></w:footnotes>`},
		{"word/media/image1.jpeg", "\xff\xd8\xff\xe0 real photo of Sarah"},
		{"word/media/image2.emf", "\x01\x00\x00\x00 EMF real signature"},
		{"word/embeddings/Jane_Doe_salary.xlsx", "PK real salary of Sarah Lane"},
		{"word/vbaProject.bin", "real macros by jdoe"},
		{"word/webextensions/taskpanes.xml", xmlH + `<wetp:taskpanes xmlns:wetp="http://schemas.microsoft.com/office/webextensions/taskpanes/2010/11">jdoe add-in</wetp:taskpanes>`},
		{"customXml/item1.xml", xmlH + `<p:properties xmlns:p="http://schemas.microsoft.com/office/2006/metadata/properties"><DisplayName>Sarah Lane</DisplayName><AccountId>12</AccountId></p:properties>`},
		{"customXml/itemProps1.xml", xmlH + `<ds:datastoreItem ds:itemID="{6B1B}" xmlns:ds="http://schemas.openxmlformats.org/officeDocument/2006/customXml"/>`},
		{"customXml/_rels/item1.xml.rels", xmlH + `<Relationships ` + relNS + `><Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/customXmlProps" Target="itemProps1.xml"/></Relationships>`},
		{"docProps/core.xml", xmlH + `<cp:coreProperties xmlns:cp="http://schemas.openxmlformats.org/package/2006/metadata/core-properties" xmlns:dc="http://purl.org/dc/elements/1.1/" xmlns:dcterms="http://purl.org/dc/terms/">` +
			`<dc:title>Quote for Sarah Lane</dc:title><dc:creator>Sarah Lane</dc:creator><cp:lastModifiedBy>Priya Shah</cp:lastModifiedBy>` +
			`<cp:revision>3</cp:revision><dcterms:created>2025-01-06T09:00:00Z</dcterms:created></cp:coreProperties>`},
		{"docProps/app.xml", xmlH + `<Properties xmlns="http://schemas.openxmlformats.org/officeDocument/2006/extended-properties" xmlns:vt="http://schemas.openxmlformats.org/officeDocument/2006/docPropsVTypes">` +
			`<Template>Acme Quote.dotm</Template><Application>Microsoft Office Word</Application><Company>Acme Plumbing Pty Ltd</Company><Manager>Priya Shah</Manager>` +
			`<HyperlinkBase>https://acme-plumbing.com.au/docs/</HyperlinkBase>` +
			`<TitlesOfParts><vt:vector size="1" baseType="lpstr"><vt:lpstr>Quote for Sarah Lane</vt:lpstr></vt:vector></TitlesOfParts></Properties>`},
		{"docProps/custom.xml", xmlH + `<Properties xmlns="http://schemas.openxmlformats.org/officeDocument/2006/custom-properties" xmlns:vt="http://schemas.openxmlformats.org/officeDocument/2006/docPropsVTypes">` +
			`<property fmtid="{D5CDD505}" pid="2" name="Client"><vt:lpwstr>Sarah Lane</vt:lpwstr></property></Properties>`},
		{"docProps/thumbnail.jpeg", "\xff\xd8\xff\xe0 real thumbnail of the first page"},
	})
}

var docxSecrets = []string{
	"Sarah", "Lane", "Priya", "Shah", "acme-plumbing", "Acme Plumbing", "0412 345 678", "jdoe", "invoices?id=991",
	"real photo", "real signature", "real salary", "real thumbnail", "real macros", "4a5d6e7f", "Jane_Doe",
	`w:initials="PS"`, `w:initials="SL"`,
}

func TestDOCXLosesEverySecret(t *testing.T) {
	e := newEngine(t, newMemRegistry())
	in := wordDocx(t)
	f := File{Name: "Quote for Sarah.docx", Data: in}
	if errs := e.Prepare(ctx, []File{f}); errs[0] != nil {
		t.Fatal(errs[0])
	}
	out, err := e.Anonymise(ctx, f)
	if err != nil {
		t.Fatal(err)
	}
	names, parts := unzip(t, out.Data)
	all := strings.ToLower(strings.Join(names, "\n"))
	for _, p := range parts {
		all += "\n" + strings.ToLower(p)
	}
	for _, s := range docxSecrets {
		if strings.Contains(all, strings.ToLower(s)) {
			t.Errorf("output still holds %q", s)
		}
	}
	if out.Format != FormatDOCX || !strings.HasPrefix(out.Name, "document-") || !strings.HasSuffix(out.Name, ".docx") {
		t.Errorf("format %s, name %s", out.Format, out.Name)
	}
	// Two images, the thumbnail and the workbook were replaced; the macros
	// and the add-in, which no rule knows, were dropped.
	if out.Attachments != 4 || strings.Join(out.DroppedParts, ",") != "word/vbaProject.bin,word/webextensions/taskpanes.xml" {
		t.Errorf("attachments %d, dropped %v", out.Attachments, out.DroppedParts)
	}
	if names[0] != "[Content_Types].xml" {
		t.Errorf("first entry = %s", names[0])
	}

	_, original := unzip(t, in)
	// Formatting and structure parts are kept byte for byte.
	for _, keep := range []string{"word/styles.xml", "word/theme/theme1.xml", "customXml/itemProps1.xml"} {
		if parts[keep] != original[keep] {
			t.Errorf("%s changed", keep)
		}
	}
	doc := parts["word/document.xml"]
	// The split name: the fake in the first run, the bold run emptied, the
	// comma kept in the last; formatting and the spell-check mark stay.
	if !regexp.MustCompile(`<w:t xml:space="preserve">Dear [A-Z][a-z]+ [A-Z][a-z]+</w:t></w:r><w:proofErr w:type="spellStart"/><w:r><w:rPr><w:b/></w:rPr><w:t></w:t></w:r><w:r><w:t>,</w:t></w:r>`).MatchString(doc) {
		t.Errorf("split runs:\n%s", doc)
	}
	// Code stays: the merge field, ids, the tab between words.
	for _, keep := range []string{`w:instr=" MERGEFIELD FirstName "`, `r:embed="rId10"`, `<w:t>Call</w:t><w:tab/><w:t>`, `w:date="2025-01-06T09:30:00Z"`, "«FirstName»"} {
		if !strings.Contains(doc, keep) {
			t.Errorf("document.xml lost %q", keep)
		}
	}
	// Initials follow the fake author.
	for _, part := range []string{doc, parts["word/comments.xml"]} {
		m := regexp.MustCompile(`w:author="([^"]+)" w:initials="([^"]+)"`).FindStringSubmatch(part)
		if m == nil || initials(m[1]) != m[2] {
			t.Errorf("author and initials = %v", m)
		}
	}
	// The EMF became a PNG, with its relationship and a content type.
	rels := parts["word/_rels/document.xml.rels"]
	if _, ok := parts["word/media/image2.png"]; !ok || !strings.Contains(rels, `Target="media/image2.png"`) ||
		!strings.Contains(parts["[Content_Types].xml"], `<Default Extension="png" ContentType="image/png"/>`) {
		t.Errorf("emf not replaced by a png:\n%s\n%s", rels, parts["[Content_Types].xml"])
	}
	if !bytes.HasPrefix([]byte(parts["word/media/image2.png"]), []byte("\x89PNG")) || !bytes.HasPrefix([]byte(parts["word/media/image1.jpeg"]), []byte("\xff\xd8")) {
		t.Error("replaced images are not images")
	}
	// The workbook was renamed, with its relationship and override.
	var emb string
	for _, n := range names {
		if strings.HasPrefix(n, "word/embeddings/embedding-") && strings.HasSuffix(n, ".xlsx") {
			emb = n
		}
	}
	if emb == "" || !strings.Contains(rels, `Target="embeddings/`+strings.TrimPrefix(emb, "word/embeddings/")+`"`) ||
		!strings.Contains(parts["[Content_Types].xml"], `PartName="/`+emb+`"`) {
		t.Errorf("embedding %q not renamed everywhere", emb)
	}
	// Dropped parts leave no relationship or override behind.
	for _, gone := range []string{"vbaProject.bin", "webextensions"} {
		if strings.Contains(rels+parts["_rels/.rels"]+parts["[Content_Types].xml"], gone) {
			t.Errorf("%s is still referred to", gone)
		}
	}
	// The attached template's path keeps only its extension.
	if !regexp.MustCompile(`Target="file:///anonymised/[0-9a-f]{10}\.dotm" TargetMode="External"`).MatchString(parts["word/_rels/settings.xml.rels"]) {
		t.Errorf("template path = %s", parts["word/_rels/settings.xml.rels"])
	}
	// Mail-merge sources are replaced whole; its settings stay.
	if st := parts["word/settings.xml"]; !strings.Contains(st, `<w:mainDocumentType w:val="formLetters"/>`) ||
		!regexp.MustCompile(`<w:connectString w:val="anonymised-[0-9a-f]{12}"/><w:query w:val="anonymised-[0-9a-f]{12}"/>`).MatchString(st) {
		t.Errorf("settings = %s", st)
	}
	// A dated property stays as it was.
	if !strings.Contains(parts["docProps/core.xml"], "<dcterms:created>2025-01-06T09:00:00Z</dcterms:created>") ||
		!strings.Contains(parts["docProps/app.xml"], "<Application>Microsoft Office Word</Application>") {
		t.Error("kept properties changed")
	}

	// The same file with the same key gives the same bytes.
	again, err := newEngine(t, newMemRegistry()).Anonymise(ctx, f)
	if err != nil || !bytes.Equal(again.Data, out.Data) {
		t.Errorf("a second run differs: %v", err)
	}
}

// A person named only in a DOCX's metadata is found in the batch's other
// files too.
func TestDOCXNamesCarryAcrossFiles(t *testing.T) {
	doc := buildDOCX(t, [][2]string{
		{"[Content_Types].xml", xmlH + `<Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types"><Default Extension="xml" ContentType="application/xml"/><Default Extension="rels" ContentType="application/vnd.openxmlformats-package.relationships+xml"/></Types>`},
		{"_rels/.rels", xmlH + `<Relationships ` + relNS + `><Relationship Id="rId1" Type="x" Target="word/document.xml"/><Relationship Id="rId2" Type="y" Target="docProps/core.xml"/></Relationships>`},
		{"word/document.xml", xmlH + `<w:document ` + wNS + `><w:body><w:p><w:r><w:t>Hello</w:t></w:r></w:p></w:body></w:document>`},
		{"docProps/core.xml", xmlH + `<cp:coreProperties xmlns:cp="http://schemas.openxmlformats.org/package/2006/metadata/core-properties" xmlns:dc="http://purl.org/dc/elements/1.1/"><dc:creator>Wendy Moss</dc:creator></cp:coreProperties>`},
	})
	notes := File{Name: "notes.txt", Data: []byte("Wendy Moss called\n")}
	e := newEngine(t, newMemRegistry())
	for _, err := range e.Prepare(ctx, []File{{Name: "a.docx", Data: doc}, notes}) {
		if err != nil {
			t.Fatal(err)
		}
	}
	out, err := e.Anonymise(ctx, notes)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(out.Data), "Wendy") {
		t.Errorf("notes = %q", out.Data)
	}
}

// A file the generator writes comes out with none of its people.
func TestGeneratedDOCXRoundTrip(t *testing.T) {
	g := synth.New(11)
	people := []synth.Person{g.Person(g.CompanyName()), g.Person(g.CompanyName())}
	file, err := g.File("docx", synth.FileInput{People: people})
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, p := range people {
		names = append(names, p.Name())
	}
	e, err := New(testKey(t), defaultProfile(t), wordDetector{names: names}, newMemRegistry())
	if err != nil {
		t.Fatal(err)
	}
	f := File{Name: "x.docx", Data: file.Data}
	e.Prepare(ctx, []File{f})
	out, err := e.Anonymise(ctx, f)
	if err != nil {
		t.Fatal(err)
	}
	_, parts := unzip(t, out.Data)
	for _, n := range names {
		if strings.Contains(parts["word/document.xml"], n) {
			t.Errorf("%s survived", n)
		}
	}
}

func TestDOCXErrors(t *testing.T) {
	minimal := func(doc string, extra ...[2]string) []byte {
		return buildDOCX(t, append([][2]string{
			{"[Content_Types].xml", xmlH + `<Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types"><Default Extension="xml" ContentType="application/xml"/><Default Extension="rels" ContentType="application/vnd.openxmlformats-package.relationships+xml"/><Default Extension="bin" ContentType="application/octet-stream"/></Types>`},
			{"_rels/.rels", xmlH + `<Relationships ` + relNS + `><Relationship Id="rId1" Type="x" Target="word/document.xml"/></Relationships>`},
			{"word/document.xml", doc},
		}, extra...))
	}
	many := make([][2]string, docxMaxParts)
	for i := range many {
		many[i] = [2]string{fmt.Sprintf("word/media/image%d.png", i), "x"}
	}
	for name, c := range map[string]struct {
		data []byte
		want string
	}{
		"not a zip":      {[]byte("hello"), "not a zip archive"},
		"old .doc":       {[]byte("\xd0\xcf\x11\xe0\xa1\xb1\x1a\xe1"), "password-protected or old .doc"},
		"no document":    {buildDOCX(t, [][2]string{{"[Content_Types].xml", "<Types/>"}}), "no word/document.xml"},
		"too many":       {minimal("<w:document "+wNS+"/>", many...), "over 2000 parts"},
		"bomb":           {minimal("<w:document "+wNS+"/>", [2]string{"word/media/big.png", strings.Repeat("\x00", docxMinExpand+1)}), "expands to more than"},
		"unsafe name":    {minimal("<w:document "+wNS+"/>", [2]string{"word/../../etc/x.xml", "x"}), "unsafe name"},
		"twice":          {minimal("<w:document "+wNS+"/>", [2]string{"WORD/document.xml", "x"}), "twice"},
		"broken xml":     {minimal("<w:document " + wNS + "><w:body>"), "word/document.xml"},
		"mismatched":     {minimal("<w:document " + wNS + "><w:body></w:document></w:body>"), "closes"},
		"dangling":       {minimal("<w:document " + wNS + " " + rNS + `><w:body><w:p r:id="rId9"/></w:body></w:document>`), "not a valid DOCX"},
		"dropped in use": {minimal("<w:document "+wNS+" "+rNS+`><w:body><w:p r:id="rId9"/></w:body></w:document>`, [2]string{"word/_rels/document.xml.rels", xmlH + `<Relationships ` + relNS + `><Relationship Id="rId9" Type="x" Target="activeX/activeX1.bin"/></Relationships>`}, [2]string{"word/activeX/activeX1.bin", "x"}), "uses relationship rId9, which is not there"},
	} {
		f := File{Name: "a.docx", Data: c.data}
		_, err := newEngine(t, newMemRegistry()).Anonymise(ctx, f)
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: err = %v, want %q", name, err, c.want)
		}
	}
}

// Text is split at tabs, breaks and fields, so no name is matched across
// them, and deleted text is kept apart from the text around it.
func TestDOCXRunsAreSplitAtBreaks(t *testing.T) {
	doc := `<w:document ` + wNS + `><w:body><w:p><w:r><w:t>Sarah</w:t><w:tab/><w:t>Lane</w:t><w:br/><w:t>A</w:t></w:r>` +
		`<w:del><w:r><w:delText>B</w:delText></w:r></w:del><w:r><w:t>C</w:t><w:t>D</w:t></w:r></w:p></w:body></w:document>`
	pkg, err := openDOCX(buildDOCX(t, [][2]string{{"[Content_Types].xml", "<Types/>"}, {"word/document.xml", doc}}))
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for i := range pkg.units {
		got = append(got, pkg.units[i].text())
	}
	if strings.Join(got, "|") != "Sarah|Lane|A|B|CD" {
		t.Errorf("units = %q", got)
	}
}

func TestDistribute(t *testing.T) {
	pieces := func(vs ...string) []string { return vs }
	for _, c := range []struct {
		pieces  []string
		matches []textswap.Match
		want    []string
	}{
		{pieces("Dear Sa", "rah La", "ne,"), []textswap.Match{{Start: 5, End: 15, To: "Ann Lee"}}, []string{"Dear Ann Lee", "", ","}},
		{pieces("ab", "", "cd"), []textswap.Match{{Start: 0, End: 1, To: "X"}, {Start: 3, End: 4, To: "Y"}}, []string{"Xb", "", "cY"}},
		{pieces("", "abc"), []textswap.Match{{Start: 0, End: 3, To: "Z"}}, []string{"", "Z"}},
		{pieces("one"), nil, []string{"one"}},
	} {
		got := distribute(c.pieces, c.matches)
		if strings.Join(got, "|") != strings.Join(c.want, "|") {
			t.Errorf("distribute = %q, want %q", got, c.want)
		}
	}
}

func TestScanXMLResolvesNamespacesAndSpans(t *testing.T) {
	// Another prefix for the same namespace is still Word's.
	data := []byte(`<x:document xmlns:x="http://schemas.openxmlformats.org/wordprocessingml/2006/main"><x:t x:a='it&apos;s' b="1">A &amp; B</x:t><!-- c --></x:document>`)
	nodes, err := scanXML(data)
	if err != nil {
		t.Fatal(err)
	}
	var tNode, text *xmlNode
	for i := range nodes {
		switch {
		case nodes[i].kind == xmlStart && is(nodes[i].name, nsW, "t"):
			tNode = &nodes[i]
		case nodes[i].kind == xmlText:
			text = &nodes[i]
		}
	}
	if tNode == nil || text == nil || text.text != "A & B" || string(data[text.start:text.end]) != "A &amp; B" {
		t.Fatalf("nodes = %+v", nodes)
	}
	a := tNode.attrs[0]
	if !is(a.name, nsW, "a") || a.value != "it's" || string(data[a.start:a.end]) != "it&apos;s" || tNode.attrs[1].name != (xmlName{local: "b"}) {
		t.Errorf("attr = %+v", a)
	}
	if nodes[tNode.match].kind != xmlEnd || nodes[nodes[tNode.match].match].start != tNode.start {
		t.Error("start and end nodes do not match")
	}
	out, err := splice(data, []xmlEdit{{start: a.start, end: a.end, with: attrEscaper.Replace(`"q"`)}, {start: text.start, end: text.end, with: textEscaper.Replace("C < D")}})
	if err != nil || !strings.Contains(string(out), `x:a='&quot;q&quot;'`) || !strings.Contains(string(out), ">C &lt; D</x:t>") {
		t.Errorf("splice = %s, %v", out, err)
	}
	if _, err := splice(data, []xmlEdit{{start: 5, end: 10}, {start: 8, end: 12}}); err == nil {
		t.Error("overlapping edits accepted")
	}
}

func TestDirectFakes(t *testing.T) {
	e := newEngine(t, newMemRegistry())
	for in, want := range map[string]*regexp.Regexp{
		"https://acme-plumbing.com.au/a?b=1":     regexp.MustCompile(`^https://[a-z-]+\.example/[0-9a-f]{10}$`),
		"mailto:sarah.lane@acme-plumbing.com.au": regexp.MustCompile(`^mailto:[a-z.]+@[a-z-]+\.example$`),
		`C:\Users\jdoe\Templates\Quote.dotx`:     regexp.MustCompile(`^file:///anonymised/[0-9a-f]{10}\.dotx$`),
		`\\server\share\jdoe file`:               regexp.MustCompile(`^file:///anonymised/[0-9a-f]{10}$`),
		"#_Toc123":                               regexp.MustCompile(`^#_Toc123$`),
	} {
		got, err := fakeTarget(ctx, e, in)
		if err != nil || !want.MatchString(got) {
			t.Errorf("fakeTarget(%q) = %q, %v", in, got, err)
		}
	}
	for in, want := range map[string]*regexp.Regexp{
		"S::sarah.lane@acme-plumbing.com.au::4a5d6e7f-1234": regexp.MustCompile(`^S::[a-z.]+@[a-z-]+\.example::[0-9a-f]{8}-1234$`),
		"0123456789abcdef": regexp.MustCompile(`^[0-9a-f]{16}$`),
		"jdoe":             regexp.MustCompile(`^anonymised-[0-9a-f]{12}$`),
	} {
		got, err := fakeUserID(ctx, e, in)
		if err != nil || !want.MatchString(got) || strings.Contains(got, "sarah") {
			t.Errorf("fakeUserID(%q) = %q, %v", in, got, err)
		}
	}
	if initials("Kenneth  huang") != "KH" || initials("") != "" {
		t.Error("initials")
	}
}

func TestPartRulesAndPaths(t *testing.T) {
	for name, want := range map[string]docxRule{
		"[Content_Types].xml": ruleContentTypes, "_rels/.rels": ruleRels, "word/_rels/document.xml.rels": ruleRels,
		"word/document.xml": ruleBody, "word/header2.xml": ruleBody, "word/glossary/document.xml": ruleBody,
		"word/settings.xml": ruleSettings, "docProps/core.xml": ruleCore, "word/styles.xml": ruleKeep,
		"word/media/image1.png": ruleMedia, "docProps/thumbnail.wmf": ruleMedia, "word/embeddings/oleObject1.bin": ruleEmbedding,
		"word/charts/chart1.xml": ruleChart, "word/diagrams/data1.xml": ruleDrawing, "customXml/item2.xml": ruleAllText,
		"word/vbaProject.bin": ruleDrop, "word/ink/ink1.xml": ruleDrop,
	} {
		if got := ruleFor(name); got != want {
			t.Errorf("ruleFor(%s) = %d, want %d", name, got, want)
		}
	}
	for rels, src := range map[string]string{"_rels/.rels": "", "word/_rels/document.xml.rels": "word/document.xml", "customXml/_rels/item1.xml.rels": "customXml/item1.xml"} {
		if relsSource(rels) != src || (src != "" && relsFor(src) != rels) {
			t.Errorf("relsSource(%s) = %q", rels, relsSource(rels))
		}
	}
	for _, c := range [][3]string{
		{"word/document.xml", "media/a.png", "word/media/a.png"},
		{"word/document.xml", "../customXml/item1.xml", "customXml/item1.xml"},
		{"word/document.xml", "/word/x.xml", "word/x.xml"},
		{"", "word/document.xml", "word/document.xml"},
	} {
		if got := resolveTarget(c[0], c[1]); got != c[2] {
			t.Errorf("resolveTarget(%q, %q) = %q", c[0], c[1], got)
		}
	}
}
