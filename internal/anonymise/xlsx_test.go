package anonymise

import (
	"bytes"
	"regexp"
	"strings"
	"testing"

	"github.com/martinsuchenak/exemplar/internal/synth"
	"github.com/martinsuchenak/exemplar/internal/textswap"
)

const (
	xNS  = `xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main"`
	tcNS = `xmlns="http://schemas.microsoft.com/office/spreadsheetml/2018/threadedcomments"`
)

// excelBook is a workbook with personal data in every place Excel keeps it:
// a sheet named for a person, shared and inline strings, a name split across
// runs, columns whose labels name what they hold (one of them a phone number
// typed as a number), formulas, a header, comments old and threaded, a
// table, a pivot cache, a chart, a text box, a link to another workbook, a
// database connection and the file's properties.
func excelBook(t *testing.T) []byte {
	styles := xmlH + `<styleSheet ` + xNS + `><fonts count="1"><font><sz val="11"/></font></fonts></styleSheet>`
	return buildDOCX(t, [][2]string{
		{"[Content_Types].xml", xmlH + `<Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types">` +
			`<Default Extension="rels" ContentType="application/vnd.openxmlformats-package.relationships+xml"/>` +
			`<Default Extension="xml" ContentType="application/xml"/><Default Extension="png" ContentType="image/png"/>` +
			`<Default Extension="bin" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.printerSettings"/>` +
			`<Default Extension="vml" ContentType="application/vnd.openxmlformats-officedocument.vmlDrawing"/>` +
			`<Override PartName="/xl/workbook.xml" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.sheet.main+xml"/>` +
			`<Override PartName="/xl/vbaProject.bin" ContentType="application/vnd.ms-office.vbaProject"/></Types>`},
		{"_rels/.rels", xmlH + `<Relationships ` + relNS + `>` +
			`<Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/officeDocument" Target="xl/workbook.xml"/>` +
			`<Relationship Id="rId2" Type="http://schemas.openxmlformats.org/package/2006/relationships/metadata/core-properties" Target="docProps/core.xml"/>` +
			`<Relationship Id="rId3" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/extended-properties" Target="docProps/app.xml"/></Relationships>`},
		{"docProps/core.xml", xmlH + `<cp:coreProperties xmlns:cp="http://schemas.openxmlformats.org/package/2006/metadata/core-properties" xmlns:dc="http://purl.org/dc/elements/1.1/">` +
			`<dc:creator>Sarah Lane</dc:creator><cp:lastModifiedBy>Priya Shah</cp:lastModifiedBy></cp:coreProperties>`},
		{"docProps/app.xml", xmlH + `<Properties xmlns="http://schemas.openxmlformats.org/officeDocument/2006/extended-properties" xmlns:vt="http://schemas.openxmlformats.org/officeDocument/2006/docPropsVTypes">` +
			`<Application>Microsoft Excel</Application><Company>Acme Plumbing Pty Ltd</Company>` +
			`<TitlesOfParts><vt:vector size="2" baseType="lpstr"><vt:lpstr>Sarah Lane</vt:lpstr><vt:lpstr>Summary</vt:lpstr></vt:vector></TitlesOfParts></Properties>`},
		{"xl/workbook.xml", xmlH + `<workbook ` + xNS + ` ` + rNS + ` xmlns:mc="http://schemas.openxmlformats.org/markup-compatibility/2006" xmlns:x15ac="http://schemas.microsoft.com/office/spreadsheetml/2010/11/ac">` +
			`<fileSharing userName="Priya Shah"/>` +
			`<mc:AlternateContent><mc:Choice Requires="x15"><x15ac:absPath url="C:\Users\jdoe\Documents\"/></mc:Choice></mc:AlternateContent>` +
			`<sheets><sheet name="Sarah Lane" sheetId="1" r:id="rId1"/><sheet name="Summary" sheetId="2" r:id="rId2"/></sheets>` +
			`<definedNames><definedName name="_xlnm.Print_Area" localSheetId="0">'Sarah Lane'!$A$1:$D$5</definedName>` +
			`<definedName name="Owner" comment="Set by Sarah Lane">"Priya"</definedName></definedNames>` +
			`<pivotCaches><pivotCache cacheId="1" r:id="rId5"/></pivotCaches><externalReferences><externalReference r:id="rId6"/></externalReferences></workbook>`},
		{"xl/_rels/workbook.xml.rels", xmlH + `<Relationships ` + relNS + `>` +
			`<Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/worksheet" Target="worksheets/sheet1.xml"/>` +
			`<Relationship Id="rId2" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/worksheet" Target="worksheets/sheet2.xml"/>` +
			`<Relationship Id="rId3" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/sharedStrings" Target="sharedStrings.xml"/>` +
			`<Relationship Id="rId4" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/styles" Target="styles.xml"/>` +
			`<Relationship Id="rId5" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/pivotCacheDefinition" Target="pivotCache/pivotCacheDefinition1.xml"/>` +
			`<Relationship Id="rId6" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/externalLink" Target="externalLinks/externalLink1.xml"/>` +
			`<Relationship Id="rId7" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/connections" Target="connections.xml"/>` +
			`<Relationship Id="rId8" Type="http://schemas.microsoft.com/office/2006/relationships/vbaProject" Target="vbaProject.bin"/>` +
			`<Relationship Id="rId9" Type="http://schemas.microsoft.com/office/2017/10/relationships/person" Target="persons/person.xml"/></Relationships>`},
		{"xl/styles.xml", styles},
		{"xl/sharedStrings.xml", xmlH + `<sst ` + xNS + ` count="7" uniqueCount="7">` +
			// A name split across runs: Excel splits a string where its formatting changes.
			`<si><r><t xml:space="preserve">Customers of Sa</t></r><r><rPr><b/></rPr><t>rah Lane</t></r></si>` +
			`<si><t>Name</t></si><si><t>E-mail</t></si><si><t>Phone</t></si>` +
			// A name no detector would find: only its column says it is one.
			`<si><t>Wendy Moss</t><rPh sb="0" eb="5"><t>Wendy</t></rPh></si>` +
			`<si><t>Sarah Lane</t></si><si><t>Pending</t><rPh sb="0" eb="7"><t>Pending</t></rPh></si></sst>`},
		{"xl/worksheets/sheet1.xml", xmlH + `<worksheet ` + xNS + ` ` + rNS + ` xmlns:x14="http://schemas.microsoft.com/office/spreadsheetml/2009/9/main" xmlns:xm="http://schemas.microsoft.com/office/excel/2006/main"><sheetData>` +
			`<row r="1"><c r="A1" t="s"><v>0</v></c></row>` +
			// The header row, below a title: Name, E-mail, Phone, Notes.
			`<row r="3"><c r="A3" t="s"><v>1</v></c><c r="B3" t="s"><v>2</v></c><c r="C3" t="s"><v>3</v></c><c r="D3" t="inlineStr"><is><t>Notes</t></is></c></row>` +
			`<row r="4"><c r="A4" t="s"><v>4</v></c><c r="B4" t="inlineStr"><is><t>wendy@example.org</t></is></c><c r="C4"><v>61412345678</v></c>` +
			`<c r="D4" t="str"><f>"Call "&amp;A4</f><v>Call Wendy Moss</v></c></row>` +
			`<row r="5"><c r="A5" t="s"><v>5</v></c><c r="C5" t="s"><v>6</v></c><c r="D5"><f>IF(ISERROR(#REF!),0,1)</f><v>0</v></c></row>` +
			`</sheetData>` +
			`<hyperlinks><hyperlink ref="B4" r:id="rId7" display="Mail Sarah Lane" tooltip="Sarah Lane"/><hyperlink ref="A1" location="Summary!A1" display="Summary"/>` +
			`<hyperlink ref="A2" location="'Sarah Lane'!A4"/></hyperlinks>` +
			`<dataValidations count="1"><dataValidation type="list" sqref="A6" prompt="Ask Priya"><formula1>"Sarah Lane,Priya"</formula1></dataValidation></dataValidations>` +
			`<pageSetup paperSize="9" orientation="portrait" r:id="rId1"/>` +
			`<headerFooter><oddHeader>&amp;LSarah Lane&amp;RPage &amp;P</oddHeader></headerFooter>` +
			`<drawing r:id="rId5"/><legacyDrawing r:id="rId2"/><tableParts count="1"><tablePart r:id="rId4"/></tableParts>` +
			`<extLst><ext uri="{CCE6A557-97BC-4b89-ADB6-D9C93CAAB3DF}"><x14:dataValidations count="1"><x14:dataValidation type="list" promptTitle="Sarah Lane">` +
			`<x14:formula1><xm:f>'Sarah Lane'!$A$4:$A$5</xm:f></x14:formula1><xm:sqref>B6</xm:sqref></x14:dataValidation></x14:dataValidations></ext></extLst></worksheet>`},
		{"xl/worksheets/_rels/sheet1.xml.rels", xmlH + `<Relationships ` + relNS + `>` +
			`<Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/printerSettings" Target="../printerSettings/printerSettings1.bin"/>` +
			`<Relationship Id="rId2" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/vmlDrawing" Target="../drawings/vmlDrawing1.vml"/>` +
			`<Relationship Id="rId3" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/comments" Target="../comments1.xml"/>` +
			`<Relationship Id="rId4" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/table" Target="../tables/table1.xml"/>` +
			`<Relationship Id="rId5" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/drawing" Target="../drawings/drawing1.xml"/>` +
			`<Relationship Id="rId6" Type="http://schemas.microsoft.com/office/2017/10/relationships/threadedComment" Target="../threadedComments/threadedComment1.xml"/>` +
			`<Relationship Id="rId7" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/hyperlink" Target="mailto:sarah.lane@acme-plumbing.com.au" TargetMode="External"/></Relationships>`},
		{"xl/worksheets/sheet2.xml", xmlH + `<worksheet ` + xNS + ` ` + rNS + `><sheetData>` +
			`<row r="1"><c r="A1"><f>'Sarah Lane'!A4</f><v>0</v></c><c r="B1"><f>SUM('Sarah Lane'!C4:C5)</f><v>0</v></c></row>` +
			`<row r="2"><c r="A2"><f>COUNTA(Customers[Name])</f><v>2</v></c><c r="B2" t="str"><f>'[1]Sarah Lane'!A1</f><v>Sarah Lane</v></c></row>` +
			`</sheetData></worksheet>`},
		{"xl/worksheets/_rels/sheet2.xml.rels", xmlH + `<Relationships ` + relNS + `>` +
			`<Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/pivotTable" Target="../pivotTables/pivotTable1.xml"/></Relationships>`},
		{"xl/printerSettings/printerSettings1.bin", `real printer \\JDOE-PC\LaserJet`},
		{"xl/vbaProject.bin", "real macros"},
		{"xl/SarahLane-notes.bin", "real notes"},
		{"xl/comments1.xml", xmlH + `<comments ` + xNS + `><authors><author>Priya Shah</author></authors><commentList><comment ref="A4" authorId="0">` +
			`<text><r><rPr><b/></rPr><t>Priya Shah:</t></r><r><t xml:space="preserve">` + "\n" + `check with Sarah Lane</t></r></text></comment></commentList></comments>`},
		{"xl/threadedComments/threadedComment1.xml", xmlH + `<ThreadedComments ` + tcNS + `>` +
			`<threadedComment ref="A5" dT="2025-01-06T09:30:00.00" personId="{11111111-2222-3333-4444-555555555555}" id="{AAAAAAAA-2222-3333-4444-555555555555}">` +
			`<text>@Sarah Lane please call</text><mentions><mention mentionpersonId="{11111111-2222-3333-4444-555555555555}" mentionId="{BBBBBBBB-2222-3333-4444-555555555555}" startIndex="0" length="11"/></mentions></threadedComment>` +
			`<threadedComment ref="A6" dT="2025-01-06T09:31:00.00" personId="{11111111-2222-3333-4444-555555555555}" id="{CCCCCCCC-2222-3333-4444-555555555555}">` +
			`<text>@Pending done</text><mentions><mention mentionpersonId="{11111111-2222-3333-4444-555555555555}" mentionId="{DDDDDDDD-2222-3333-4444-555555555555}" startIndex="0" length="8"/></mentions></threadedComment></ThreadedComments>`},
		{"xl/persons/person.xml", xmlH + `<personList ` + tcNS + `><person displayName="Sarah Lane" id="{11111111-2222-3333-4444-555555555555}" userId="sarah.lane@acme-plumbing.com.au" providerId="AD"/></personList>`},
		{"xl/drawings/vmlDrawing1.vml", `<xml xmlns:v="urn:schemas-microsoft-com:vml" xmlns:o="urn:schemas-microsoft-com:office:office" xmlns:x="urn:schemas-microsoft-com:office:excel">` +
			`<v:shape id="_x0000_s1025" style='position:absolute'><v:textbox><div style='text-align:left'>Sarah Lane</div></v:textbox>` +
			`<x:ClientData ObjectType="Note"><x:Row>3</x:Row><x:Column>0</x:Column></x:ClientData></v:shape>` +
			// A check box linked to a cell, and a picture known by its file's name.
			`<v:shape id="_x0000_s1026" alt="Tick for Sarah Lane"><x:ClientData ObjectType="Checkbox"><x:FmlaLink>'Sarah Lane'!$E$4</x:FmlaLink></x:ClientData></v:shape>` +
			`<v:shape id="LH"><v:imagedata o:relid="rId1" o:title="Sarah Lane signature"/></v:shape></xml>`},
		{"xl/tables/table1.xml", xmlH + `<table ` + xNS + ` id="1" name="Customers" displayName="Customers" ref="A3:D5">` +
			`<autoFilter ref="A3:D5"><filterColumn colId="0"><filters><filter val="Sarah Lane"/></filters></filterColumn></autoFilter>` +
			`<tableColumns count="4"><tableColumn id="1" name="Name"/><tableColumn id="2" name="E-mail"/><tableColumn id="3" name="Phone"/>` +
			`<tableColumn id="4" name="Notes" totalsRowLabel="Total for Sarah Lane"><calculatedColumnFormula>"Call "&amp;[@Name]</calculatedColumnFormula></tableColumn></tableColumns></table>`},
		{"xl/drawings/drawing1.xml", xmlH + `<xdr:wsDr xmlns:xdr="http://schemas.openxmlformats.org/drawingml/2006/spreadsheetDrawing" xmlns:a="http://schemas.openxmlformats.org/drawingml/2006/main" ` + rNS + `>` +
			`<xdr:twoCellAnchor><xdr:sp macro="" textlink="'Sarah Lane'!$A$1"><xdr:nvSpPr><xdr:cNvPr id="2" name="TextBox 1" descr="Note for Sarah Lane"/></xdr:nvSpPr>` +
			`<xdr:txBody><a:p><a:r><a:t>Sa</a:t></a:r><a:r><a:t>rah Lane</a:t></a:r></a:p></xdr:txBody></xdr:sp></xdr:twoCellAnchor>` +
			`<xdr:twoCellAnchor><xdr:pic><xdr:nvPicPr><xdr:cNvPr id="3" name="Picture 2" descr="Sarah Lane"/></xdr:nvPicPr><xdr:blipFill><a:blip r:embed="rId1"/></xdr:blipFill></xdr:pic></xdr:twoCellAnchor>` +
			`<xdr:twoCellAnchor><xdr:graphicFrame><a:graphic><a:graphicData><c:chart xmlns:c="http://schemas.openxmlformats.org/drawingml/2006/chart" r:id="rId2"/></a:graphicData></a:graphic></xdr:graphicFrame></xdr:twoCellAnchor></xdr:wsDr>`},
		{"xl/drawings/_rels/drawing1.xml.rels", xmlH + `<Relationships ` + relNS + `>` +
			`<Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/image" Target="../media/image1.png"/>` +
			`<Relationship Id="rId2" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/chart" Target="../charts/chart1.xml"/></Relationships>`},
		{"xl/media/image1.png", "\x89PNG\r\n\x1a\nreal photo of Sarah"},
		{"xl/charts/chart1.xml", xmlH + `<c:chartSpace xmlns:c="http://schemas.openxmlformats.org/drawingml/2006/chart" xmlns:a="http://schemas.openxmlformats.org/drawingml/2006/main"><c:chart>` +
			`<c:title><c:tx><c:rich><a:p><a:r><a:t>Calls by Sarah Lane</a:t></a:r></a:p></c:rich></c:tx></c:title><c:plotArea><c:barChart><c:ser>` +
			`<c:cat><c:strRef><c:f>'Sarah Lane'!$A$4:$A$5</c:f><c:strCache><c:pt idx="0"><c:v>Wendy Moss</c:v></c:pt><c:pt idx="1"><c:v>Sarah Lane</c:v></c:pt></c:strCache></c:strRef></c:cat>` +
			`<c:val><c:numRef><c:f>'Sarah Lane'!$C$4:$C$5</c:f><c:numCache><c:pt idx="0"><c:v>3</c:v></c:pt></c:numCache></c:numRef></c:val></c:ser></c:barChart></c:plotArea></c:chart></c:chartSpace>`},
		{"xl/pivotCache/pivotCacheDefinition1.xml", xmlH + `<pivotCacheDefinition ` + xNS + ` ` + rNS + ` r:id="rId1" recordCount="2">` +
			`<cacheSource type="worksheet"><worksheetSource ref="A3:C5" sheet="Sarah Lane"/></cacheSource><cacheFields count="2">` +
			`<cacheField name="Name" numFmtId="0"><sharedItems><s v="Wendy Moss"/><s v="Sarah Lane"/></sharedItems></cacheField>` +
			`<cacheField name="Phone" numFmtId="0"><sharedItems containsNumber="1"><n v="61412345678"/></sharedItems></cacheField></cacheFields></pivotCacheDefinition>`},
		{"xl/pivotCache/_rels/pivotCacheDefinition1.xml.rels", xmlH + `<Relationships ` + relNS + `>` +
			`<Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/pivotCacheRecords" Target="pivotCacheRecords1.xml"/></Relationships>`},
		{"xl/pivotCache/pivotCacheRecords1.xml", xmlH + `<pivotCacheRecords ` + xNS + ` count="2"><r><s v="Wendy Moss"/><n v="61412345678"/></r><r><x v="1"/><n v="2025"/></r></pivotCacheRecords>`},
		{"xl/pivotTables/pivotTable1.xml", xmlH + `<pivotTableDefinition ` + xNS + ` name="PivotTable1" cacheId="1" dataCaption="Values"><location ref="A4:B6" firstHeaderRow="1" firstDataRow="1" firstDataCol="1"/>` +
			`<pivotFields count="2"><pivotField axis="axisRow"><items count="2"><item x="0" n="Wendy Moss (VIP)"/><item t="default"/></items></pivotField><pivotField dataField="1"/></pivotFields>` +
			`<dataFields count="1"><dataField name="Calls for Sarah Lane" fld="1"/></dataFields></pivotTableDefinition>`},
		{"xl/pivotTables/_rels/pivotTable1.xml.rels", xmlH + `<Relationships ` + relNS + `>` +
			`<Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/pivotCacheDefinition" Target="../pivotCache/pivotCacheDefinition1.xml"/></Relationships>`},
		{"xl/externalLinks/externalLink1.xml", xmlH + `<externalLink ` + xNS + ` ` + rNS + `><externalBook r:id="rId1"><sheetNames><sheetName val="Sarah Lane"/></sheetNames>` +
			`<sheetDataSet><sheetData sheetId="0"><row r="1"><cell r="A1" t="str"><v>Sarah Lane</v></cell><cell r="B1"><v>12</v></cell></row></sheetData></sheetDataSet></externalBook></externalLink>`},
		{"xl/externalLinks/_rels/externalLink1.xml.rels", xmlH + `<Relationships ` + relNS + `>` +
			`<Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/externalLinkPath" Target="file:///C:\Users\jdoe\Documents\Sarah%20Lane%20accounts.xlsx" TargetMode="External"/></Relationships>`},
		{"xl/connections.xml", xmlH + `<connections ` + xNS + `><connection id="1" name="Query - Sarah Lane" type="5" refreshedVersion="6">` +
			`<dbPr connection="Provider=Microsoft.ACE.OLEDB.12.0;Data Source=C:\Users\jdoe\clients.accdb" command="SELECT * FROM clients WHERE owner='Priya'"/></connection></connections>`},
	})
}

var xlsxSecrets = []string{
	"Sarah", "Lane", "Priya", "Shah", "Wendy", "Moss", "wendy@example.org", "acme-plumbing", "Acme Plumbing", "jdoe", "61412345678",
	"real photo", "real printer", "real macros", "real notes", "SarahLane", "printerSettings1.bin",
}

func TestXLSXLosesEverySecret(t *testing.T) {
	e := newEngine(t, newMemRegistry())
	in := excelBook(t)
	f := File{Name: "Sarah's customers.xlsx", Data: in}
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
	for _, s := range xlsxSecrets {
		if strings.Contains(all, strings.ToLower(s)) {
			t.Errorf("output still holds %q", s)
		}
	}
	if out.Format != FormatXLSX || !strings.HasPrefix(out.Name, "document-") || !strings.HasSuffix(out.Name, ".xlsx") {
		t.Errorf("format %s, name %s", out.Format, out.Name)
	}
	// The picture was replaced; the printer settings, the macros and the
	// unknown part were dropped, the last listed under a keyed name.
	if out.Attachments != 1 || len(out.DroppedParts) != 3 || strings.Join(out.DroppedParts[:2], ",") != "xl/printerSettings/printerSettings1.bin,xl/vbaProject.bin" ||
		!regexp.MustCompile(`^part-[0-9a-f]{6}\.bin$`).MatchString(out.DroppedParts[2]) {
		t.Errorf("attachments %d, dropped %v", out.Attachments, out.DroppedParts)
	}
	_, original := unzip(t, in)
	if parts["xl/styles.xml"] != original["xl/styles.xml"] {
		t.Error("styles changed")
	}

	// The sheet has a new name, and everything that refers to it follows.
	m := regexp.MustCompile(`<sheet name="([^"]+)" sheetId="1"`).FindStringSubmatch(parts["xl/workbook.xml"])
	if m == nil || m[1] == "Sarah Lane" {
		t.Fatalf("workbook = %s", parts["xl/workbook.xml"])
	}
	sheet := strings.NewReplacer("&apos;", "'", "&amp;", "&").Replace(m[1])
	ref := "'" + strings.ReplaceAll(sheet, "'", "''") + "'"
	inText, inAttr := textEscaper.Replace(ref), attrEscaper.Replace(ref)
	for part, want := range map[string]string{
		"xl/workbook.xml":                         `localSheetId="0">` + inText + `!$A$1:$D$5</definedName>`,
		"xl/worksheets/sheet2.xml":                `<f>` + inText + `!A4</f><v>0</v></c><c r="B1"><f>SUM(` + inText + `!C4:C5)</f>`,
		"xl/worksheets/sheet1.xml":                `location="` + inAttr + `!A4"`,
		"xl/charts/chart1.xml":                    `<c:f>` + inText + `!$A$4:$A$5</c:f>`,
		"xl/drawings/drawing1.xml":                `textlink="` + inAttr + `!$A$1"`,
		"xl/drawings/vmlDrawing1.vml":             `<x:FmlaLink>` + inText + `!$E$4</x:FmlaLink>`,
		"xl/pivotCache/pivotCacheDefinition1.xml": `sheet="` + attrEscaper.Replace(sheet) + `"`,
	} {
		if !strings.Contains(parts[part], want) {
			t.Errorf("%s does not hold %s:\n%s", part, want, parts[part])
		}
	}
	// What is not personal stays: labels, functions, other sheets, cells.
	s1, s2 := parts["xl/worksheets/sheet1.xml"], parts["xl/worksheets/sheet2.xml"]
	for _, keep := range []string{`<f>"Call "&amp;A4</f>`, `<f>IF(ISERROR(#REF!),0,1)</f>`, `location="Summary!A1"`, `&amp;RPage &amp;P</oddHeader>`, `<pageSetup paperSize="9" orientation="portrait"/>`} {
		if !strings.Contains(s1, keep) {
			t.Errorf("sheet1 lost %q:\n%s", keep, s1)
		}
	}
	if !strings.Contains(s2, `<f>COUNTA(Customers[Name])</f>`) || !strings.Contains(parts["xl/sharedStrings.xml"], `<si><t>Name</t></si><si><t>E-mail</t></si><si><t>Phone</t></si>`) ||
		!strings.Contains(parts["xl/tables/table1.xml"], `<tableColumn id="1" name="Name"/><tableColumn id="2" name="E-mail"/>`) {
		t.Errorf("labels changed:\n%s\n%s", s2, parts["xl/sharedStrings.xml"])
	}
	// The split name: the fake in the first run, the bold run emptied.
	if !regexp.MustCompile(`<t xml:space="preserve">Customers of [A-Z][a-z]+ [A-Z][a-z]+</t></r><r><rPr><b/></rPr><t></t></r>`).MatchString(parts["xl/sharedStrings.xml"]) {
		t.Errorf("split runs:\n%s", parts["xl/sharedStrings.xml"])
	}
	// The phone number typed as a number is still a number, as long, and
	// the pivot cache holds the same fake.
	pm := regexp.MustCompile(`<c r="C4"><v>(\d+)</v></c>`).FindStringSubmatch(s1)
	if pm == nil || len(pm[1]) != len("61412345678") || !strings.Contains(parts["xl/pivotCache/pivotCacheDefinition1.xml"], `<n v="`+pm[1]+`"/>`) ||
		!strings.Contains(parts["xl/pivotCache/pivotCacheRecords1.xml"], `<n v="`+pm[1]+`"/>`) || !strings.Contains(parts["xl/pivotCache/pivotCacheRecords1.xml"], `<n v="2025"/>`) {
		t.Errorf("phone number = %v:\n%s", pm, parts["xl/pivotCache/pivotCacheRecords1.xml"])
	}
	// A phonetic reading spells out its string, so it goes when the string
	// changes, and stays when it does not.
	if ss := parts["xl/sharedStrings.xml"]; strings.Count(ss, "<rPh") != 1 || !strings.Contains(ss, `<si><t>Pending</t><rPh sb="0" eb="7"><t>Pending</t></rPh></si>`) {
		t.Errorf("readings:\n%s", ss)
	}
	// A mention gives offsets into its comment's text, so it goes when the
	// text changes, and stays when it does not.
	tc := parts["xl/threadedComments/threadedComment1.xml"]
	if strings.Count(tc, "<mentions>") != 1 || !strings.Contains(tc, `<text>@Pending done</text><mentions>`) {
		t.Errorf("threaded comments:\n%s", tc)
	}
	// The printer settings' relationship went with them.
	if strings.Contains(parts["xl/worksheets/_rels/sheet1.xml.rels"], "printerSettings") {
		t.Errorf("printer settings still referred to")
	}

	// The same file with the same key gives the same bytes.
	again, err := newEngine(t, newMemRegistry()).Anonymise(ctx, f)
	if err != nil || !bytes.Equal(again.Data, out.Data) {
		t.Errorf("a second run differs: %v", err)
	}
}

// A person named only in a workbook is found in the batch's other files too,
// even when only the column says it is a name.
func TestXLSXNamesCarryAcrossFiles(t *testing.T) {
	notes := File{Name: "notes.txt", Data: []byte("Wendy Moss called\n")}
	e := newEngine(t, newMemRegistry())
	for _, err := range e.Prepare(ctx, []File{{Name: "a.xlsx", Data: excelBook(t)}, notes}) {
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

// A workbook the generator writes comes out with none of its people.
func TestGeneratedXLSXRoundTrip(t *testing.T) {
	g := synth.New(12)
	people := []synth.Person{g.Person(g.CompanyName()), g.Person(g.CompanyName())}
	file, err := g.File("xlsx", synth.FileInput{People: people})
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
	f := File{Name: "x.xlsx", Data: file.Data}
	e.Prepare(ctx, []File{f})
	out, err := e.Anonymise(ctx, f)
	if err != nil {
		t.Fatal(err)
	}
	// First and last names are in columns of their own, which only their
	// labels say are names.
	_, parts := unzip(t, out.Data)
	for _, p := range people {
		for _, s := range []string{p.FirstName, p.LastName, p.Email, p.Phone, p.Company} {
			if strings.Contains(parts["xl/worksheets/sheet1.xml"], "<t>"+xmlEscapeTest(s)+"</t>") {
				t.Errorf("%s survived", s)
			}
		}
	}
}

func xmlEscapeTest(s string) string { return textEscaper.Replace(s) }

// minimalXLSX is the smallest XLSX around a workbook and a sheet, plus any
// extra parts.
func minimalXLSX(t testing.TB, workbook, sheet string, extra ...[2]string) []byte {
	return buildDOCX(t, append([][2]string{
		{"[Content_Types].xml", xmlH + `<Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types"><Default Extension="xml" ContentType="application/xml"/><Default Extension="rels" ContentType="application/vnd.openxmlformats-package.relationships+xml"/><Default Extension="bin" ContentType="application/octet-stream"/></Types>`},
		{"_rels/.rels", xmlH + `<Relationships ` + relNS + `><Relationship Id="rId1" Type="x" Target="xl/workbook.xml"/></Relationships>`},
		{"xl/workbook.xml", workbook},
		{"xl/_rels/workbook.xml.rels", xmlH + `<Relationships ` + relNS + `><Relationship Id="rId1" Type="x" Target="worksheets/sheet1.xml"/></Relationships>`},
		{"xl/worksheets/sheet1.xml", sheet},
	}, extra...))
}

func TestXLSXErrors(t *testing.T) {
	book := `<workbook ` + xNS + ` ` + rNS + `><sheets><sheet name="Sheet1" sheetId="1" r:id="rId1"/></sheets></workbook>`
	for name, c := range map[string]struct {
		data []byte
		want string
	}{
		"not a zip":  {[]byte("hello"), "not an XLSX: not a zip archive"},
		"old .xls":   {[]byte("\xd0\xcf\x11\xe0\xa1\xb1\x1a\xe1"), "password-protected or old .xls file; save it as an unprotected .xlsx"},
		"a docx":     {wordDocx(t), "not an XLSX: it has no xl/workbook.xml"},
		"broken xml": {minimalXLSX(t, book, `<worksheet `+xNS+`><sheetData>`), "xl/worksheets/sheet1.xml: an element is never closed"},
		"twice":      {minimalXLSX(t, book, `<worksheet `+xNS+`/>`, [2]string{"XL/workbook.xml", ""}), "holds XL/workbook.xml twice"},
		"dangling":   {minimalXLSX(t, book, `<worksheet `+xNS+` `+rNS+`><drawing r:id="rId9"/></worksheet>`), "not a valid XLSX"},
		// A newer chart type (chartEx) is not read, so it is dropped, and
		// the drawing that shows it cannot do without it.
		"newer chart": {minimalXLSX(t, book, `<worksheet `+xNS+` `+rNS+`><drawing r:id="rId1"/></worksheet>`,
			[2]string{"xl/worksheets/_rels/sheet1.xml.rels", xmlH + `<Relationships ` + relNS + `><Relationship Id="rId1" Type="x" Target="../drawings/drawing1.xml"/></Relationships>`},
			[2]string{"xl/drawings/drawing1.xml", `<xdr:wsDr xmlns:xdr="http://schemas.openxmlformats.org/drawingml/2006/spreadsheetDrawing" ` + rNS + `><cx:chart xmlns:cx="http://schemas.microsoft.com/office/drawing/2014/chartex" r:id="rId1"/></xdr:wsDr>`},
			[2]string{"xl/drawings/_rels/drawing1.xml.rels", xmlH + `<Relationships ` + relNS + `><Relationship Id="rId1" Type="x" Target="../charts/chartEx1.xml"/></Relationships>`},
			[2]string{"xl/charts/chartEx1.xml", `<cx:chartSpace xmlns:cx="http://schemas.microsoft.com/office/drawing/2014/chartex"><cx:pt>Sarah Lane</cx:pt></cx:chartSpace>`}),
			"xl/drawings/drawing1.xml uses a relationship that is not there"},
		// A macro sheet is dropped, and the workbook cannot do without it.
		"macro sheet": {buildDOCX(t, [][2]string{
			{"[Content_Types].xml", xmlH + `<Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types"><Default Extension="xml" ContentType="application/xml"/><Default Extension="rels" ContentType="application/vnd.openxmlformats-package.relationships+xml"/></Types>`},
			{"xl/workbook.xml", `<workbook ` + xNS + ` ` + rNS + `><sheets><sheet name="Sheet1" sheetId="1" r:id="rId1"/><sheet name="Macro1" sheetId="2" r:id="rId2"/></sheets></workbook>`},
			{"xl/_rels/workbook.xml.rels", xmlH + `<Relationships ` + relNS + `><Relationship Id="rId1" Type="x" Target="worksheets/sheet1.xml"/><Relationship Id="rId2" Type="x" Target="macrosheets/sheet1.xml"/></Relationships>`},
			{"xl/worksheets/sheet1.xml", `<worksheet ` + xNS + `/>`},
			{"xl/macrosheets/sheet1.xml", `<worksheet ` + xNS + `/>`},
		}), "xl/workbook.xml uses a relationship that is not there"},
	} {
		_, err := newEngine(t, newMemRegistry()).Anonymise(ctx, File{Name: "a.xlsx", Data: c.data})
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: err = %v, want %q", name, err, c.want)
		}
	}
	// Errors name only the parts Office names.
	for name, want := range map[string]string{
		"xl/worksheets/sheet12.xml":               "xl/worksheets/sheet12.xml",
		"xl/worksheets/_rels/sheet1.xml.rels":     "xl/worksheets/_rels/sheet1.xml.rels",
		"xl/sharedStrings.xml":                    "xl/sharedStrings.xml",
		"xl/printerSettings/printerSettings1.bin": "xl/printerSettings/printerSettings1.bin",
		"xl/PriyaShah.xml":                        "a part named by the sender",
		"xl/media/PriyaShah.png":                  "a part named by the sender",
	} {
		if got := xlsxFormat.partLabel(name); got != want {
			t.Errorf("partLabel(%q) = %q, want %q", name, got, want)
		}
	}
}

func TestXLSXPartRules(t *testing.T) {
	for name, want := range map[string]ooxmlRule{
		"xl/workbook.xml": ruleWorkbook, "xl/worksheets/sheet3.xml": ruleSheet, "xl/chartsheets/sheet1.xml": ruleSheet,
		"xl/sharedStrings.xml": ruleSharedStrings, "xl/comments2.xml": ruleComments, "xl/threadedComments/threadedComment1.xml": ruleThreaded,
		"xl/persons/person.xml": rulePersons, "xl/tables/table4.xml": ruleTable, "xl/pivotCache/pivotCacheRecords1.xml": rulePivotCache,
		"xl/pivotTables/pivotTable1.xml": rulePivotTable, "xl/externalLinks/externalLink1.xml": ruleExternalLink,
		"xl/connections.xml": ruleConnections, "xl/queryTables/queryTable1.xml": ruleQueryTable, "xl/slicers/slicer1.xml": ruleCaptions,
		"xl/drawings/drawing1.xml": ruleDrawing, "xl/drawings/vmlDrawing1.vml": ruleVML, "xl/drawings/vmlDrawingHF1.vml": ruleVML, "xl/charts/chart1.xml": ruleChart,
		"xl/styles.xml": ruleKeep, "xl/calcChain.xml": ruleKeep, "xl/media/image1.png": ruleMedia, "xl/embeddings/oleObject1.bin": ruleEmbedding,
		"docProps/core.xml": ruleCore, "xl/vbaProject.bin": ruleDrop, "xl/model/item.data": ruleDrop, "xl/printerSettings/printerSettings1.bin": ruleDrop,
		"word/document.xml": ruleDrop,
	} {
		if got := xlsxFormat.ruleFor(name); got != want {
			t.Errorf("ruleFor(%s) = %d, want %d", name, got, want)
		}
	}
}

// testReplacer makes a replacer from pairs.
func testReplacer(t *testing.T, pairs map[string]string) *replacer {
	t.Helper()
	rep, err := textswap.New(pairs)
	if err != nil {
		t.Fatal(err)
	}
	return rep
}

func TestXLSXFormulas(t *testing.T) {
	rep := testReplacer(t, map[string]string{"Sarah Lane": "Ann O'Hara", "Jo": "Kim", "Acme": "Bolt"})
	x := &xlsxBook{sheets: []string{"Sarah Lane", "Jo", "Summary"}}
	for in, want := range map[string]string{
		// Strings in formulas; a quote in a fake is doubled.
		`"Dear "&"Sarah Lane"`: `"Dear "&"Ann O'Hara"`,
		`"say ""Jo"" twice"`:   `"say ""Kim"" twice"`,
		// Sheets, quoted or not, and ranges of them; an apostrophe doubled.
		`'Sarah Lane'!A1+Jo!B2`:                 `'Ann O''Hara'!A1+'Kim'!B2`,
		`SUM('Jo:Sarah Lane'!A1)`:               `SUM('Kim:Ann O''Hara'!A1)`,
		`SUM(Jo:Summary!A1)`:                    `SUM('Kim:Summary'!A1)`,
		`Summary!A1&Jo2!A1`:                     `Summary!A1&Jo2!A1`,
		`[1]Acme!A1+'[2]Sarah Lane'!B1`:         `'[1]Bolt'!A1+'[2]Ann O''Hara'!B1`,
		`Jo`:                                    `Jo`, // a name, not a sheet
		`IF(#REF!,"",Summary!$A$1)`:             `IF(#REF!,"",Summary!$A$1)`,
		`Customers[Sarah Lane]`:                 `Customers[Ann O''Hara]`,
		`Customers[[#This Row],[Sarah Lane]]`:   `Customers[[#This Row],[Ann O''Hara]]`,
		`[@Jo]+[@[Sarah Lane]]+Customers[#All]`: `[@Kim]+[@[Ann O''Hara]]+Customers[#All]`,
		`[1]!Owner`:                             `[1]!Owner`,
		`"unclosed`:                             `"unclosed`,
		`'unclosed`:                             `'unclosed`,
		`AcmeTotal+Acme.Rate`:                   `AcmeTotal+Acme.Rate`,
		// A sheet named in a string follows the sheet; the rest is text.
		`INDIRECT("'Sarah Lane'!A1")&INDIRECT("Jo!B2")`: `INDIRECT("'Ann O''Hara'!A1")&INDIRECT("'Kim'!B2")`,
		`"Hi Sarah Lane! See Summary!A1, not Jo2!A1"`:   `"Hi Ann O'Hara! See Summary!A1, not Jo2!A1"`,
		`"Lane!"`: `"Lane!"`,
	} {
		if got := x.formula(rep, in); got != want {
			t.Errorf("formula(%s) = %s, want %s", in, got, want)
		}
	}
	if got := formulaStrings(`"Dear "&'Sarah Lane'!A1&"Jo ""Kim"""&T[Acme]`); got != "Dear \n\nJo \"Kim\"" {
		t.Errorf("formulaStrings = %q", got)
	}
}

func TestXLSXSheetNames(t *testing.T) {
	long := strings.Repeat("x", 40)
	rep := testReplacer(t, map[string]string{
		"Sarah": "Ann", "Lane": "Lee", "Jo": "Ann Lee", "Wendy": long, "Priya": "a/b:c*", "Pat": "'", "Emoji": "😀" + strings.Repeat("y", 30),
	})
	x := &xlsxBook{sheets: []string{"Sarah Lane", "Jo", "Ann Lee (2)", "Wendy", "Priya", "Pat", "Emoji", "Summary"}}
	for old, want := range map[string]string{
		"Sarah Lane": "Ann Lee",
		// Taken, and so is the next, so the third.
		"Jo":         "Ann Lee (3)",
		"Wendy":      long[:31],
		"Priya":      "a b c",
		"Pat":        "Sheet",
		"Emoji":      "😀" + strings.Repeat("y", 29),
		"Summary":    "Summary",
		"SARAH LANE": "Ann Lee", // Excel compares sheet names without case
	} {
		if got := x.sheetName(rep, old); got != want {
			t.Errorf("sheetName(%s) = %q, want %q", old, got, want)
		}
	}
	if got := x.externalSheetName(rep, "Jo"); got != "Ann Lee" {
		t.Errorf("externalSheetName = %q", got)
	}
}

func TestXLSXHeaderFooter(t *testing.T) {
	rep := testReplacer(t, map[string]string{"Sarah Lane": "Smith & Co", "Bold": "Thin"})
	for in, want := range map[string]string{
		`&LSarah Lane&RPage &P of &N`:         `&LSmith && Co&RPage &P of &N`,
		`&"Arial,Bold"&12Sarah Lane&K00FF00x`: `&"Arial,Bold"&12Smith && Co&K00FF00x`,
		`&&Sarah Lane&`:                       `&&Smith && Co&`,
		`Sarah Lane`:                          `Smith && Co`,
	} {
		if got := rewriteHeaderFooter(rep, in); got != want {
			t.Errorf("rewriteHeaderFooter(%s) = %s, want %s", in, got, want)
		}
	}
	if got := headerFooterText(`&LSarah Lane&C&"Arial,Bold"Jo&RPage &P`); got != "Sarah Lane\n\nJo\n\nPage " {
		t.Errorf("headerFooterText = %q", got)
	}
}

func TestXLSXHelpers(t *testing.T) {
	for ref, want := range map[string][3]int{"A1": {1, 1, 1}, "Z9": {26, 9, 1}, "AA10": {27, 10, 1}, "XFD1048576": {16384, 1048576, 1}, "": {}, "1A": {}, "A0": {}, "ABCD1": {}, "a1": {}} {
		c, r, ok := parseCellRef(ref)
		if ok != (want[2] == 1) || (ok && (c != want[0] || r != want[1])) {
			t.Errorf("parseCellRef(%q) = %d, %d, %v", ref, c, r, ok)
		}
	}
	data := []byte(`<pageSetup paperSize="9"  r:id = "rId1" orientation="portrait"/>`)
	nodes, err := scanXML(data)
	if err != nil {
		t.Fatal(err)
	}
	s, e := attrSpan(data, nodes[0].attrs[1])
	if got := string(data[:s]) + string(data[e:]); got != `<pageSetup paperSize="9" orientation="portrait"/>` {
		t.Errorf("without the attribute: %s", got)
	}
}

// A column label marks the cells right below it, from the first row that
// has a label, down to a gap, an empty cell or another label. Cells above it
// and in other columns are only searched, and so are placeholders and
// numbers too short to be phone numbers.
func TestXLSXColumnLabels(t *testing.T) {
	book := `<workbook ` + xNS + ` ` + rNS + `><sheets><sheet name="Sheet1" sheetId="1" r:id="rId1"/></sheets></workbook>`
	is := func(v string) string { return `<c t="inlineStr"><is><t>` + v + `</t></is></c>` }
	sheet := `<worksheet ` + xNS + `><sheetData>` +
		`<row>` + is("Kim Doyle") + `</row>` + // above the labels: no row number, so row 1
		`<row>` + is("Contact name") + is("Notes") + is("Mobile") + `</row>` +
		`<row>` + is("Lee Ford") + is("Ravi Nair") + `<c><v>0412345678</v></c></row>` +
		`<row>` + is("Total") + is("x") + `<c><v>42</v></c></row>` + // a placeholder, and a number too short
		`<row>` + is("Ana Diaz") + is("x") + `<c><v>2025</v></c></row>` +
		`<row r="9"><c r="A9" t="inlineStr"><is><t>Max Tan</t></is></c></row>` + // after a gap: another block
		`</sheetData></worksheet>`
	// A form: its labels run down a column, each beside its value.
	form := `<worksheet ` + xNS + `><sheetData>` +
		`<row r="1">` + is("Name") + is("Uma Roy") + `</row>` +
		`<row r="2">` + is("Email") + is("uma@example.org") + `</row>` +
		`<row r="3">` + is("Company") + is("Roy Builders") + `</row>` +
		`</sheetData></worksheet>`
	// A sparse table, as exports write them: a value not given is an empty
	// cell or no cell at all, and neither ends the block.
	sparse := `<worksheet ` + xNS + `><sheetData>` +
		`<row r="1">` + is("ID") + is("Mobile Phone") + is("Work Email") + `</row>` +
		`<row r="2"><c r="A2"><v>1</v></c><c r="B2" s="1"/><c r="C2" t="inlineStr"><is><t></t></is></c></row>` +
		`<row r="3"><c r="A3"><v>2</v></c><c r="B3" t="inlineStr"><is><t>+639171234567</t></is></c></row>` +
		`<row r="4"><c r="A4"><v>3</v></c><c r="C4" t="inlineStr"><is><t>kai@example.org</t></is></c></row>` +
		`<row r="5"><c r="A5"><v>4</v></c><c r="B5"><v>447700900123</v></c></row>` +
		`<row r="7"><c r="B7" t="inlineStr"><is><t>+421901234567</t></is></c></row>` + // after a blank row
		`</sheetData></worksheet>`
	for name, c := range map[string]struct {
		sheet string
		want  map[string]string
	}{
		"table": {sheet, map[string]string{"Kim Doyle": "", "Contact name": "", "Lee Ford": EntityPerson, "Ravi Nair": "", "0412345678": EntityPhone,
			"Total": "", "Ana Diaz": EntityPerson, "Max Tan": ""}},
		"form": {form, map[string]string{"Name": "", "Email": "", "Company": "", "Uma Roy": "", "Roy Builders": ""}},
		"sparse": {sparse, map[string]string{"+639171234567": EntityPhone, "kai@example.org": EntityEmail,
			"447700900123": EntityPhone, "+421901234567": ""}},
	} {
		pkg, err := xlsxFormat.open(minimalXLSX(t, book, c.sheet))
		if err != nil {
			t.Fatal(err)
		}
		got := map[string]string{}
		for _, d := range pkg.texts() {
			if labelled(d) {
				got[d.value] = d.entity
			} else if _, ok := got[d.value]; !ok {
				got[d.value] = ""
			}
		}
		for v, want := range c.want {
			if e, ok := got[v]; !ok || e != want {
				t.Errorf("%s: %q: entity %q (found %v), want %q", name, v, e, ok, want)
			}
		}
		for _, short := range []string{"42", "2025"} {
			if _, ok := got[short]; ok {
				t.Errorf("%s: %s was taken for a phone number", name, short)
			}
		}
	}
}

// Only a value its label can vouch for is taken for what the label says.
func TestLabelled(t *testing.T) {
	for _, c := range []struct {
		value, entity string
		want          bool
	}{
		{"Wendy Moss", EntityPerson, true},
		{"Wendy Moss", "", false},
		{"  ", EntityPerson, false},
		{"Total", EntityPerson, false},
		{"N/A", EntityEmail, false},
		{"tbd", EntityCompany, false},
		{"0412 345 678", EntityPhone, true},
		{"ext. 42", EntityPhone, false},
		{"2025", EntityPhone, false},
	} {
		if got := labelled(docText{value: c.value, entity: c.entity}); got != c.want {
			t.Errorf("labelled(%q, %q) = %v", c.value, c.entity, got)
		}
	}
}

// A workbook attached to an email is rewritten in place when the profile
// says so, found by its media type when its name has no extension.
func TestXLSXAttachmentFormat(t *testing.T) {
	if f := attachmentFormats["application/vnd.openxmlformats-officedocument.spreadsheetml.sheet"]; f != FormatXLSX {
		t.Errorf("attachment format = %s", f)
	}
	if NameFor(FormatXLSX) != "input.xlsx" {
		t.Errorf("NameFor = %s", NameFor(FormatXLSX))
	}
}
