package anonymise

import (
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/martinsuchenak/exemplar/internal/synth"
)

// An XLSX is an Office package (ooxml.go) around xl/workbook.xml: sheets of
// cells, the strings cells share, comments, tables, pivot caches, charts and
// links to other workbooks. Text is rewritten where it sits, and so are the
// strings inside formulas. Sheet names are rewritten too, fitted to Excel's
// rules, and every formula, chart and pivot cache that refers to a sheet
// follows it. A column whose label names an entity ("Email", "Phone") marks
// the cells below it, as a CSV column does.

var (
	nsX     = namespaces("http://schemas.openxmlformats.org/spreadsheetml/2006/main", "http://purl.oclc.org/ooxml/spreadsheetml/main")
	nsXM    = namespaces("http://schemas.microsoft.com/office/excel/2006/main")
	nsX14   = namespaces("http://schemas.microsoft.com/office/spreadsheetml/2009/9/main")
	nsX15AC = namespaces("http://schemas.microsoft.com/office/spreadsheetml/2010/11/ac")
	// nsTC holds threaded comments and the people who wrote them.
	nsTC = namespaces("http://schemas.microsoft.com/office/spreadsheetml/2018/threadedcomments")
	// nsXV is Excel's in VML drawings: comment boxes and form controls.
	nsXV = namespaces("urn:schemas-microsoft-com:office:excel")
)

// xlsxFormat is Excel's. Parts kept as they are hold structure and formatting
// only.
var xlsxFormat = &ooxmlFormat{
	name: "XLSX", article: "an", main: "xl/workbook.xml", legacy: ".xls", ext: ".xlsx",
	rules: []partRule{
		{regexp.MustCompile(`^\[content_types\]\.xml$`), ruleContentTypes},
		{regexp.MustCompile(`(^|/)_rels/[^/]*\.rels$`), ruleRels},
		{regexp.MustCompile(`^xl/workbook\.xml$`), ruleWorkbook},
		{regexp.MustCompile(`^xl/(worksheets|chartsheets)/sheet\d*\.xml$`), ruleSheet},
		{regexp.MustCompile(`^xl/sharedstrings\.xml$`), ruleSharedStrings},
		{regexp.MustCompile(`^xl/comments\d*\.xml$`), ruleComments},
		{regexp.MustCompile(`^xl/threadedcomments/threadedcomment\d*\.xml$`), ruleThreaded},
		{regexp.MustCompile(`^xl/persons/person\d*\.xml$`), rulePersons},
		{regexp.MustCompile(`^xl/tables/table\d*\.xml$`), ruleTable},
		{regexp.MustCompile(`^xl/pivotcache/pivotcache(definition|records)\d*\.xml$`), rulePivotCache},
		{regexp.MustCompile(`^xl/pivottables/pivottable\d*\.xml$`), rulePivotTable},
		{regexp.MustCompile(`^xl/externallinks/externallink\d*\.xml$`), ruleExternalLink},
		{regexp.MustCompile(`^xl/connections\.xml$`), ruleConnections},
		{regexp.MustCompile(`^xl/querytables/querytable\d*\.xml$`), ruleQueryTable},
		{regexp.MustCompile(`^xl/(slicers/slicer|slicercaches/slicercache|timelines/timeline|timelinecaches/timelinecache)\d*\.xml$`), ruleCaptions},
		{regexp.MustCompile(`^xl/drawings/drawing\d*\.xml$`), ruleDrawing},
		{regexp.MustCompile(`^xl/drawings/vmldrawing(hf)?\d*\.vml$`), ruleVML},
		{regexp.MustCompile(`^xl/charts/chart\d*\.xml$`), ruleChart},
		{regexp.MustCompile(`^xl/richdata/[^/]+\.xml$`), ruleAllText},
		{regexp.MustCompile(`^docprops/core\.xml$`), ruleCore},
		{regexp.MustCompile(`^docprops/app\.xml$`), ruleApp},
		{regexp.MustCompile(`^docprops/custom\.xml$`), ruleCustom},
		{regexp.MustCompile(`^customxml/item\d*\.xml$`), ruleAllText},
		{regexp.MustCompile(`^xl/(styles|calcchain|metadata)\.xml$`), ruleKeep},
		{regexp.MustCompile(`^xl/theme/theme\d*\.xml$`), ruleKeep},
		{regexp.MustCompile(`^xl/charts/(colors|style)\d*\.xml$`), ruleKeep},
		{regexp.MustCompile(`^xl/ctrlprops/ctrlprop\d*\.xml$`), ruleKeep},
		{regexp.MustCompile(`^customxml/itemprops\d*\.xml$`), ruleKeep},
		{regexp.MustCompile(`^(xl/media/[^/]+|docprops/thumbnail\.[a-z0-9]+)$`), ruleMedia},
		{regexp.MustCompile(`^xl/embeddings/[^/]+$`), ruleEmbedding},
	},
	office: regexp.MustCompile(`^(\[content_types\]\.xml|docprops/(app|core|custom)\.xml|docprops/thumbnail\.[a-z0-9]+|` +
		`xl/(workbook|styles|sharedstrings|calcchain|metadata|connections)\.xml|xl/(worksheets|chartsheets)/sheet\d*\.xml|xl/comments\d*\.xml|` +
		`xl/threadedcomments/threadedcomment\d*\.xml|xl/persons/person\d*\.xml|xl/tables/table\d*\.xml|xl/pivotcache/pivotcache(definition|records)\d*\.xml|` +
		`xl/pivottables/pivottable\d*\.xml|xl/externallinks/externallink\d*\.xml|xl/querytables/querytable\d*\.xml|` +
		`xl/(slicers/slicer|slicercaches/slicercache|timelines/timeline|timelinecaches/timelinecache)\d*\.xml|xl/drawings/(drawing\d*\.xml|vmldrawing(hf)?\d*\.vml)|` +
		`xl/charts/(chart|colors|style)\d*\.xml|xl/theme/theme\d*\.xml|xl/ctrlprops/ctrlprop\d*\.xml|` +
		`xl/richdata/(rdrichvalue|rdrichvaluestructure|rdrichvaluetypes|richvaluerel|rdarray|rdsupportingpropertybag|rdsupportingpropertybagstructure)\.xml|` +
		`xl/media/(image\d*|thumbnail)\.[a-z0-9]+|customxml/(item|itemprops)\d*\.xml)$`),
	knownDrops: regexp.MustCompile(`^(xl/(vbaproject\.bin|vbadata\.xml|activex/activex\d+\.(xml|bin)|activex/_rels/activex\d+\.xml\.rels|` +
		`printersettings/printersettings\d+\.bin|customproperty\d+\.bin|webextensions/(webextension\d+|taskpanes)\.xml|webextensions/_rels/taskpanes\.xml\.rels|` +
		`model/item\.data|_rels/vbaproject\.bin\.rels|(macrosheets|dialogsheets)/(_rels/)?sheet\d+\.xml(\.rels)?|revisions/[a-z]+\d*\.xml|revisions/_rels/[a-z]+\d*\.xml\.rels)|` +
		`docprops/meta\.xml|customui/customui(14)?\.xml|customui/_rels/customui(14)?\.xml\.rels)$`),
	newState: func() any { return &xlsxBook{} },
	collect:  collectXLSX,
	finish:   func(pkg *ooxmlPackage) { workbook(pkg).markColumns(pkg) },
	edit: func(pkg *ooxmlPackage, newText []string, dropped map[string]bool, edits map[*ooxmlPart][]xmlEdit) {
		workbook(pkg).edit(pkg, newText, dropped, edits)
	},
}

// xlsxBook is what an XLSX's parts say about each other, gathered while they
// are read.
type xlsxBook struct {
	// sheets are the workbook's sheet names, as written.
	sheets []string
	// renamed maps each sheet name that changes, lower-cased, to its new
	// name; built on first use, once the replacements are known.
	renamed map[string]string
	// shared is the unit of each shared string, by index; -1 for one with
	// no text.
	shared []int
	grids  []*xlsxGrid
	// threads are the threaded comments with mentions.
	threads []xlsxThread
	// readings are the strings with phonetic readings.
	readings []xlsxReading
	// pageSetups are the printer settings sheets point to, which are
	// dropped.
	pageSetups []xlsxRef
}

// xlsxGrid is the cells of one sheet, in the order they are written.
type xlsxGrid struct {
	part  *ooxmlPart
	cells []xlsxCell
}

// xlsxCell is a cell whose text could name an entity: a shared string, an
// inline string or formula result, or a number (a phone number typed as
// one).
type xlsxCell struct {
	row, col int
	shared   int // shared string index, or -1
	unit     int // unit of an inline string or formula result, or -1
	number   *xmlNode
}

// xlsxThread is a threaded comment whose text has mentions: they give
// offsets into it, so they are dropped when it changes.
type xlsxThread struct {
	part        *ooxmlPart
	unit        int    // the comment's text, or -1
	mentions    [2]int // byte range of the mentions element
	hasMentions bool
}

// xlsxReading is a string with phonetic readings (rPh), which spell out its
// text: they are dropped when it changes.
type xlsxReading struct {
	part  *ooxmlPart
	unit  int
	spans [][2]int // byte ranges of the rPh elements
}

// xlsxRef is a relationship attribute that may point to a dropped part.
type xlsxRef struct {
	part *ooxmlPart
	node *xmlNode
	attr int
}

// workbook returns what an XLSX's parts said about each other so far.
func workbook(pkg *ooxmlPackage) *xlsxBook { return pkg.state.(*xlsxBook) }

// collectXLSX finds a part's values by its rule. Parts Word and Excel share
// (document properties, drawings, charts) are read as Word's are, and
// Excel's references in drawings and charts on top.
func collectXLSX(pkg *ooxmlPackage, p *ooxmlPart) {
	x := workbook(pkg)
	switch p.rule {
	case ruleCore, ruleApp, ruleCustom, ruleAllText, ruleRels:
		collectMarkup(pkg, p)
	case ruleDrawing, ruleChart:
		collectMarkup(pkg, p)
		x.collectRefs(pkg, p)
	case ruleVML:
		x.collectVML(pkg, p)
	default:
		x.collect(pkg, p)
	}
}

// collectRefs finds the references in a drawing or chart: a chart's data
// ('Sheet 1'!$B$2:$B$9) and a shape's linked cell.
func (x *xlsxBook) collectRefs(pkg *ooxmlPackage, p *ooxmlPart) {
	for i := range p.nodes {
		n := &p.nodes[i]
		switch {
		case n.kind == xmlStart:
			if _, ai := n.attr(xmlName{local: "textlink"}); ai >= 0 {
				x.addFormula(pkg, p, &n.attrs[ai])
			}
		case n.kind == xmlText && p.rule == ruleChart && i > 0 && p.nodes[i-1].kind == xmlStart && is(p.nodes[i-1].name, nsC, "f"):
			x.addFormulaText(pkg, p, n)
		}
	}
}

// collectVML finds the values in a VML drawing, the older kind Excel keeps
// comment boxes, form controls and pictures in printed headers in: its text,
// a picture's title (often its file's name) and alt text, and the cells a
// control is linked to.
func (x *xlsxBook) collectVML(pkg *ooxmlPackage, p *ooxmlPart) {
	var stack []xmlName
	for i := range p.nodes {
		n := &p.nodes[i]
		switch n.kind {
		case xmlStart:
			stack = append(stack, n.name)
			for j := range n.attrs {
				a := &n.attrs[j]
				if is(a.name, nsO, "title") || a.name.space == "" && (a.name.local == "alt" || a.name.local == "title") {
					pkg.addAttr(p, a, "")
				}
			}
		case xmlEnd:
			if len(stack) > 0 {
				stack = stack[:len(stack)-1]
			}
		case xmlText:
			switch {
			case strings.TrimSpace(n.text) == "" || len(stack) == 0:
			case nsXV[stack[len(stack)-1].space] && strings.HasPrefix(stack[len(stack)-1].local, "Fmla"):
				x.addFormulaText(pkg, p, n) // a control's linked cell or list
			default:
				pkg.addText(p, n, "")
			}
		}
	}
}

// xlsxRichText are the elements whose runs make one string: a shared
// string, an inline string, a comment.
func xlsxRichText(n xmlName, stack []xmlName) bool {
	switch {
	case is(n, nsX, "si"), is(n, nsX, "is"):
		return true
	case is(n, nsX, "text"):
		return len(stack) > 1 && is(stack[len(stack)-2], nsX, "comment")
	}
	return false
}

// cellTags are the elements of cells and strings, whose attributes say
// nothing personal.
var cellTags = map[string]bool{"c": true, "row": true, "v": true, "f": true, "is": true, "si": true, "t": true, "r": true, "rPr": true, "x": true, "col": true}

// minPhoneDigits is the fewest digits a value needs to be taken for a phone
// number from its column alone: shorter numbers (an extension, a year, a
// count) are too likely to be something else, and a fake replaces a value
// everywhere in the file.
const minPhoneDigits = 6

// headerFooters are the elements of a sheet's printed header and footer.
var headerFooters = map[string]bool{"oddHeader": true, "oddFooter": true, "evenHeader": true, "evenFooter": true, "firstHeader": true, "firstFooter": true}

// collect reads one of Excel's own parts.
func (x *xlsxBook) collect(pkg *ooxmlPackage, p *ooxmlPart) {
	var grid *xlsxGrid
	if p.rule == ruleSheet {
		grid = &xlsxGrid{part: p}
		x.grids = append(x.grids, grid)
	}
	var stack []xmlName
	var rich []ooxmlPiece // the runs of the string being read
	var readings [][2]int // its phonetic readings
	inRich, phonetic := false, 0
	row, col, cell, cellType := 0, 0, -1, ""
	field := ""       // the entity a pivot cache field's label names
	inThread := false // reading a threaded comment
	cur := xlsxThread{}
	for i := range p.nodes {
		n := &p.nodes[i]
		switch n.kind {
		case xmlStart:
			stack = append(stack, n.name)
			switch {
			case xlsxRichText(n.name, stack):
				inRich, rich, readings = true, nil, nil
			case is(n.name, nsX, "rPh"):
				phonetic++
				if inRich {
					readings = append(readings, [2]int{n.start, p.nodes[n.match].end})
				}
			case grid != nil && is(n.name, nsX, "row"):
				if r, err := strconv.Atoi(attrOf(n, "r")); err == nil && r > 0 {
					row = r
				} else {
					row++
				}
				col = 0
			case grid != nil && is(n.name, nsX, "c"):
				if c, r, ok := parseCellRef(attrOf(n, "r")); ok {
					col, row = c, r
				} else {
					col++
				}
				cellType = attrOf(n, "t")
				grid.cells = append(grid.cells, xlsxCell{row: row, col: col, shared: -1, unit: -1})
				cell = len(grid.cells) - 1
			case p.rule == rulePivotCache && is(n.name, nsX, "cacheField"):
				field = fieldHint(attrOf(n, "name"))
			case p.rule == ruleThreaded && is(n.name, nsTC, "threadedComment"):
				cur, inThread = xlsxThread{part: p, unit: -1}, true
			case is(n.name, nsTC, "mentions") && inThread:
				cur.mentions = [2]int{n.start, p.nodes[n.match].end}
				cur.hasMentions = true
			}
			x.collectAttrs(pkg, p, n, field)
		case xmlEnd:
			richEnd := xlsxRichText(n.name, stack)
			if len(stack) > 0 {
				stack = stack[:len(stack)-1]
			}
			switch {
			case richEnd:
				u := -1
				if len(rich) > 0 {
					u = pkg.add(ooxmlUnit{part: p, pieces: rich, initialsOf: -1})
					if len(readings) > 0 {
						x.readings = append(x.readings, xlsxReading{part: p, unit: u, spans: readings})
					}
				}
				switch {
				case is(n.name, nsX, "si"):
					x.shared = append(x.shared, u)
				case is(n.name, nsX, "is") && cell >= 0:
					grid.cells[cell].unit = u
				}
				inRich, rich = false, nil
			case is(n.name, nsX, "rPh"):
				phonetic--
			case grid != nil && is(n.name, nsX, "c"):
				cell = -1
			case is(n.name, nsX, "cacheField"):
				field = ""
			case is(n.name, nsTC, "threadedComment"):
				if cur.unit >= 0 && cur.hasMentions {
					x.threads = append(x.threads, cur)
				}
				inThread = false
			}
		case xmlText:
			if len(stack) == 0 {
				continue
			}
			parent := stack[len(stack)-1]
			switch {
			case is(parent, nsX, "t") && inRich && phonetic == 0:
				rich = append(rich, ooxmlPiece{start: n.start, end: n.end, value: n.text})
			case is(parent, nsX, "t"):
				pkg.addText(p, n, "") // a phonetic reading, or text outside a string
			case is(parent, nsX, "v") && cell >= 0:
				switch cellType {
				case "s":
					if idx, err := strconv.Atoi(strings.TrimSpace(n.text)); err == nil {
						grid.cells[cell].shared = idx
					}
				case "str":
					grid.cells[cell].unit = pkg.addText(p, n, "")
				case "", "n":
					grid.cells[cell].number = n
				}
			case p.rule == ruleExternalLink && is(parent, nsX, "v"):
				if _, err := strconv.ParseFloat(strings.TrimSpace(n.text), 64); err != nil {
					pkg.addText(p, n, "") // a cached value of the other workbook
				}
			case is(parent, nsX, "f"), is(parent, nsX, "formula"), is(parent, nsX, "formula1"), is(parent, nsX, "formula2"),
				is(parent, nsXM, "f"), is(parent, nsX, "definedName"),
				is(parent, nsX, "calculatedColumnFormula"), is(parent, nsX, "totalsRowFormula"):
				x.addFormulaText(pkg, p, n)
			case nsX[parent.space] && headerFooters[parent.local]:
				pkg.add(ooxmlUnit{part: p, initialsOf: -1, rewrite: rewriteHeaderFooter, search: headerFooterText,
					pieces: []ooxmlPiece{{start: n.start, end: n.end, value: n.text}}})
			case is(parent, nsX, "author") && p.rule == ruleComments:
				pkg.addText(p, n, EntityPerson)
			case is(parent, nsTC, "text") && inThread:
				cur.unit = pkg.addText(p, n, "")
			}
		}
	}
}

// collectAttrs finds the values in one start tag of Excel's own parts.
// field is the entity a pivot cache field's label names, for its items.
func (x *xlsxBook) collectAttrs(pkg *ooxmlPackage, p *ooxmlPart, n *xmlNode, field string) {
	el := n.name.local
	if nsX[n.name.space] && cellTags[el] {
		return // nothing to read in them, and a sheet holds millions
	}
	for i := range n.attrs {
		a := &n.attrs[i]
		if a.name.space != "" {
			if nsR[a.name.space] && a.name.local == "id" && is(n.name, nsX, "pageSetup") {
				x.pageSetups = append(x.pageSetups, xlsxRef{part: p, node: n, attr: i})
			}
			continue
		}
		name := a.name.local
		switch {
		case p.rule == ruleCaptions:
			if name == "caption" {
				pkg.addAttr(p, a, "")
			}
		case p.rule == ruleWorkbook && is(n.name, nsX, "sheet") && name == "name":
			x.sheets = append(x.sheets, a.value)
			pkg.add(ooxmlUnit{part: p, initialsOf: -1, rewrite: x.sheetName,
				pieces: []ooxmlPiece{{start: a.start, end: a.end, value: a.value, attr: true}}})
		case is(n.name, nsX, "definedName") && (name == "comment" || name == "description" || name == "help" || name == "statusBar"):
			pkg.addAttr(p, a, "")
		case is(n.name, nsX, "definedName") && name == "refersTo":
			x.addFormula(pkg, p, a) // a name defined in another workbook
		case is(n.name, nsX, "fileSharing") && name == "userName":
			pkg.addAttr(p, a, EntityPerson)
		case is(n.name, nsX15AC, "absPath") && name == "url":
			pkg.addFake(p, a, fakeTarget) // where the file was saved: C:\Users\jdoe\…
		case (el == "webPublishItem" || el == "webPublishObject") && name == "destinationFile":
			pkg.addFake(p, a, fakeTarget)
		case (el == "webPublishItem" || el == "webPublishObject") && name == "title":
			pkg.addAttr(p, a, "")
		case is(n.name, nsX, "hyperlink") && (name == "display" || name == "tooltip"):
			pkg.addAttr(p, a, "")
		case is(n.name, nsX, "hyperlink") && name == "location":
			x.addFormula(pkg, p, a)
		case (is(n.name, nsX, "dataValidation") || is(n.name, nsX14, "dataValidation")) &&
			(name == "prompt" || name == "promptTitle" || name == "error" || name == "errorTitle"):
			pkg.addAttr(p, a, "")
		case (is(n.name, nsX, "filter") || is(n.name, nsX, "customFilter")) && name == "val":
			pkg.addAttr(p, a, "")
		case is(n.name, nsX, "scenario") && name == "user":
			pkg.addAttr(p, a, EntityPerson)
		case is(n.name, nsX, "scenario") && name == "comment", is(n.name, nsX, "inputCells") && name == "val":
			pkg.addAttr(p, a, "")
		case is(n.name, nsX, "protectedRange") && name == "securityDescriptor":
			pkg.addFake(p, a, fakeOpaque) // names the user's account (S-1-5-21-…)
		case is(n.name, nsX, "tableColumn") && (name == "name" || name == "totalsRowLabel"):
			pkg.addAttr(p, a, "")
		case is(n.name, nsX, "cacheField") && (name == "name" || name == "caption"):
			pkg.addAttr(p, a, "")
		case is(n.name, nsX, "cacheField") && name == "formula":
			x.addFormula(pkg, p, a)
		case is(n.name, nsX, "worksheetSource") && name == "sheet":
			pkg.add(ooxmlUnit{part: p, initialsOf: -1, rewrite: x.sheetName, search: noSearch,
				pieces: []ooxmlPiece{{start: a.start, end: a.end, value: a.value, attr: true}}})
		case is(n.name, nsX, "s") && name == "v" && p.rule == rulePivotCache:
			pkg.addAttr(p, a, field) // a value of the pivot's source data
		case is(n.name, nsX, "n") && name == "v" && p.rule == rulePivotCache && field == EntityPhone && isDigits(a.value):
			pkg.addAttr(p, a, EntityPhone)
		case is(n.name, nsX, "n") && name == "v" && p.rule == rulePivotCache && isDigits(a.value) && len(a.value) >= minPhoneDigits:
			// A number long enough to be a phone number: it follows the
			// fake its cell got. The records do not say which field a
			// value is of.
			pkg.add(ooxmlUnit{part: p, initialsOf: -1, rewrite: applyReplacements, search: noSearch,
				pieces: []ooxmlPiece{{start: a.start, end: a.end, value: a.value, attr: true}}})
		case p.rule == rulePivotTable && ((el == "item" && name == "n") || ((el == "dataField" || el == "pivotField") && name == "name")):
			pkg.addAttr(p, a, "")
		case is(n.name, nsX, "sheetName") && name == "val":
			pkg.add(ooxmlUnit{part: p, initialsOf: -1, rewrite: x.externalSheetName,
				pieces: []ooxmlPiece{{start: a.start, end: a.end, value: a.value, attr: true}}})
		case is(n.name, nsX, "connection") && (name == "name" || name == "description"):
			pkg.addAttr(p, a, "")
		case is(n.name, nsX, "dbPr") && (name == "connection" || name == "command"),
			is(n.name, nsX, "olapPr") && name == "connection":
			pkg.addFake(p, a, fakeOpaque) // a connection string or query, replaced whole
		case is(n.name, nsX, "textPr") && name == "sourceFile", is(n.name, nsX, "webPr") && name == "url":
			pkg.addFake(p, a, fakeTarget)
		case is(n.name, nsX, "parameter") && (name == "prompt" || name == "string"):
			pkg.addAttr(p, a, "")
		case is(n.name, nsX, "queryTableField") && name == "name":
			pkg.addAttr(p, a, "")
		case is(n.name, nsTC, "person") && name == "displayName":
			pkg.addAttr(p, a, EntityPerson)
		case is(n.name, nsTC, "person") && name == "userId":
			pkg.addFake(p, a, fakeUserID)
		}
	}
}

// attrOf returns the value of an unprefixed attribute of n.
func attrOf(n *xmlNode, local string) string {
	v, _ := n.attr(xmlName{local: local})
	return v
}

// noSearch reads none of a value: its text is found where it is defined.
func noSearch(string) string { return "" }

// applyReplacements makes the replacements found elsewhere in the file.
func applyReplacements(rep *replacer, v string) (string, error) { return rep.Apply(v), nil }

// addFormula adds a formula held in an attribute.
func (x *xlsxBook) addFormula(pkg *ooxmlPackage, p *ooxmlPart, a *xmlAttr) {
	pkg.add(ooxmlUnit{part: p, initialsOf: -1, rewrite: x.formula, search: formulaStrings,
		pieces: []ooxmlPiece{{start: a.start, end: a.end, value: a.value, attr: true}}})
}

// addFormulaText adds a formula held in an element.
func (x *xlsxBook) addFormulaText(pkg *ooxmlPackage, p *ooxmlPart, n *xmlNode) {
	pkg.add(ooxmlUnit{part: p, initialsOf: -1, rewrite: x.formula, search: formulaStrings,
		pieces: []ooxmlPiece{{start: n.start, end: n.end, value: n.text}}})
}

// parseCellRef reads a cell reference ("B12") as its column and row, both
// from 1.
func parseCellRef(ref string) (col, row int, ok bool) {
	i := 0
	for i < len(ref) && ref[i] >= 'A' && ref[i] <= 'Z' {
		col = col*26 + int(ref[i]-'A'+1)
		i++
	}
	if i == 0 || i > 3 {
		return 0, 0, false
	}
	row, err := strconv.Atoi(ref[i:])
	if err != nil || row < 1 {
		return 0, 0, false
	}
	return col, row, true
}

// markColumns gives the cells below a column label the entity the label
// names. The header row is the first row of a sheet with such a label in
// it, and a label marks only its own block: the cells below it, down to the
// first blank row or another label. An empty cell is a value not given, so
// it does not end the block. So a second table further down, or a form with
// its labels down a column ("Name", "Email", "Phone"), is not taken for
// values.
func (x *xlsxBook) markColumns(pkg *ooxmlPackage) {
	text := func(c xlsxCell) string {
		switch {
		case c.shared >= 0 && c.shared < len(x.shared) && x.shared[c.shared] >= 0:
			return pkg.units[x.shared[c.shared]].text()
		case c.unit >= 0:
			return pkg.units[c.unit].text()
		}
		return ""
	}
	mark := func(u int, entity string) {
		if u >= 0 && pkg.units[u].entity == "" {
			pkg.units[u].entity = entity
		}
	}
	for _, g := range x.grids {
		header, hints := -1, map[int]string{}
		for _, c := range g.cells {
			if header >= 0 && c.row != header {
				break
			}
			if h := fieldHint(text(c)); h != "" {
				header, hints[c.col] = c.row, h
			}
		}
		// Every text below the label row is counted in its column (see
		// addColumnNames). Without a label a hint knows, the label row is
		// the sheet's first row with text.
		labels := header
		for i := 0; labels < 0 && i < len(g.cells); i++ {
			if text(g.cells[i]) != "" {
				labels = g.cells[i].row
			}
		}
		for _, c := range g.cells {
			u := c.unit
			if c.shared >= 0 && c.shared < len(x.shared) {
				u = x.shared[c.shared]
			}
			if c.row > labels && u >= 0 {
				pkg.units[u].columns = append(pkg.units[u].columns, g.part.name+"!"+strconv.Itoa(c.col))
			}
		}
		value := func(c xlsxCell) string {
			if c.number != nil {
				return strings.TrimSpace(c.number.text)
			}
			return strings.TrimSpace(text(c))
		}
		filled := map[int]bool{}
		for _, c := range g.cells {
			if c.row > header && value(c) != "" {
				filled[c.row] = true
			}
		}
		end := header + 1 // the first blank row, which ends every block
		for filled[end] {
			end++
		}
		ended := map[int]bool{}
		for _, c := range g.cells {
			h := hints[c.col]
			if h == "" || c.row <= header || c.row >= end || ended[c.col] {
				continue
			}
			v := value(c)
			if v == "" {
				continue
			}
			if fieldHint(v) != "" {
				ended[c.col] = true // another label ends the block
				continue
			}
			switch {
			case c.shared >= 0 && c.shared < len(x.shared):
				mark(x.shared[c.shared], h)
			case c.unit >= 0:
				mark(c.unit, h)
			case c.number != nil && h == EntityPhone && isDigits(v) && len(v) >= minPhoneDigits:
				// A phone number typed as a number: its digits are faked,
				// so it stays one.
				pkg.add(ooxmlUnit{part: g.part, entity: EntityPhone, initialsOf: -1,
					pieces: []ooxmlPiece{{start: c.number.start, end: c.number.end, value: c.number.text}}})
			}
		}
	}
}

func isDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// edit drops the mentions of comments and the phonetic readings of strings
// whose text changed, and the references to printer settings, which are
// dropped as parts.
func (x *xlsxBook) edit(pkg *ooxmlPackage, newText []string, dropped map[string]bool, edits map[*ooxmlPart][]xmlEdit) {
	changed := func(u int) bool { return u >= 0 && newText[u] != pkg.units[u].text() }
	for _, t := range x.threads {
		if changed(t.unit) {
			edits[t.part] = append(edits[t.part], xmlEdit{start: t.mentions[0], end: t.mentions[1]})
		}
	}
	gone := map[*ooxmlPart][][2]int{} // readings to drop, in byte order
	for _, r := range x.readings {
		if changed(r.unit) {
			gone[r.part] = append(gone[r.part], r.spans...)
		}
	}
	for part, spans := range gone {
		// A reading's own text may have been rewritten too: its edits go
		// with it. Readings are collected in byte order and never overlap.
		kept := edits[part][:0]
		for _, ed := range edits[part] {
			i := sort.Search(len(spans), func(i int) bool { return spans[i][1] >= ed.end })
			if i == len(spans) || ed.start < spans[i][0] {
				kept = append(kept, ed)
			}
		}
		for _, span := range spans {
			kept = append(kept, xmlEdit{start: span[0], end: span[1]})
		}
		edits[part] = kept
	}
	targets := map[*ooxmlPart]map[string]string{}
	for _, r := range x.pageSetups {
		if targets[r.part] == nil {
			targets[r.part] = pkg.relTargets(r.part.name)
		}
		a := r.node.attrs[r.attr]
		if !dropped[targets[r.part][a.value]] {
			continue
		}
		start, end := attrSpan(r.part.data, a)
		edits[r.part] = append(edits[r.part], xmlEdit{start: start, end: end})
	}
}

// attrSpan returns the bytes of a whole attribute, with the space before it:
// ` r:id="rId1"`.
func attrSpan(data []byte, a xmlAttr) (int, int) {
	space := func(b byte) bool { return b == ' ' || b == '\t' || b == '\n' || b == '\r' }
	s := a.start - 2 // before the opening quote
	for s >= 0 && space(data[s]) {
		s--
	}
	s-- // the '='
	for s >= 0 && space(data[s]) {
		s--
	}
	for s >= 0 && !space(data[s]) {
		s--
	}
	for s > 0 && space(data[s-1]) {
		s--
	}
	return s, a.end + 1
}

// Sheet names. A sheet's new name is its name with the replacements made,
// fitted to Excel's rules (synth.FitSheetName) and unique in the workbook.

// fitSheetName returns a sheet's name with the replacements made, fitted
// to Excel's rules but for uniqueness. A name with nothing to replace is
// kept as it is.
func fitSheetName(rep *replacer, old string) string {
	if s := rep.Apply(old); s != old {
		return synth.FitSheetName(s, synth.MaxSheetName)
	}
	return old
}

// renames returns the new name of every sheet whose name changes, by its
// old name lower-cased: Excel compares sheet names without case.
func (x *xlsxBook) renames(rep *replacer) map[string]string {
	if x.renamed != nil {
		return x.renamed
	}
	x.renamed = map[string]string{}
	used := map[string]bool{}
	for _, old := range x.sheets {
		if fitSheetName(rep, old) == old {
			used[strings.ToLower(old)] = true
		}
	}
	for _, old := range x.sheets {
		base := fitSheetName(rep, old)
		if base == old {
			continue
		}
		name := base
		for n := 2; used[strings.ToLower(name)]; n++ {
			suffix := fmt.Sprintf(" (%d)", n)
			name = synth.FitSheetName(base, synth.MaxSheetName-len(suffix)) + suffix
		}
		used[strings.ToLower(name)] = true
		x.renamed[strings.ToLower(old)] = name
	}
	return x.renamed
}

// sheetName returns a sheet's new name.
func (x *xlsxBook) sheetName(rep *replacer, old string) (string, error) {
	if n, ok := x.renames(rep)[strings.ToLower(old)]; ok {
		return n, nil
	}
	return old, nil
}

// externalSheetName returns the new name of a sheet in another workbook:
// fitted, but with no other names to be unique among.
func (x *xlsxBook) externalSheetName(rep *replacer, old string) (string, error) {
	return fitSheetName(rep, old), nil
}

// Formulas. Only their strings ("Dear " & A1) and the sheets and table
// columns they refer to change; functions, cells and names stay as written.

// xlsxMaxRefDepth caps how deeply a formula's structured references may
// nest. Excel's own references nest at most three deep; a deeper one is a
// file made to be read slowly, and fails instead.
const xlsxMaxRefDepth = 8

// formula returns a formula with the replacements made in its strings, and
// its references following the sheets and table columns they name.
func (x *xlsxBook) formula(rep *replacer, f string) (string, error) {
	var b strings.Builder
	for i := 0; i < len(f); {
		c := f[i]
		switch {
		case c == '"':
			j := quotedEnd(f, i, '"')
			lit := strings.ReplaceAll(f[i+1:max(i+1, j-1)], `""`, `"`)
			if v := x.literal(rep, lit); v != lit && j > i+1 && f[j-1] == '"' {
				b.WriteString(`"` + strings.ReplaceAll(v, `"`, `""`) + `"`)
			} else {
				b.WriteString(f[i:j])
			}
			i = j
		case c == '\'':
			j := quotedEnd(f, i, '\'')
			if j < len(f) && f[j] == '!' && f[j-1] == '\'' {
				b.WriteString(x.quotedRef(rep, f[i:j]))
			} else {
				b.WriteString(f[i:j])
			}
			i = j
		case c == '[':
			if m := externalRefRe.FindStringSubmatchIndex(f[i:]); m != nil {
				// [1]Sheet1!A1: a sheet of another workbook.
				name := f[i+m[2] : i+m[3]]
				if n := x.mapSheets(rep, name, true); n != name {
					b.WriteString("'" + f[i:i+m[2]] + strings.ReplaceAll(n, "'", "''") + "'")
				} else {
					b.WriteString(f[i : i+m[3]])
				}
				i += m[3]
				continue
			}
			j := bracketEnd(f, i)
			w, err := structuredRef(rep, f[i:j], 1)
			if err != nil {
				return "", err
			}
			b.WriteString(w)
			i = j
		case isNameByte(c):
			j := i
			for j < len(f) && isNameByte(f[j]) {
				j++
			}
			// Sheet1!A1, or Sheet1:Sheet3!A1 across sheets.
			end := j
			if end < len(f) && f[end] == ':' {
				k := end + 1
				for k < len(f) && isNameByte(f[k]) {
					k++
				}
				if k > end+1 && k < len(f) && f[k] == '!' {
					end = k
				}
			}
			if end < len(f) && f[end] == '!' {
				name := f[i:end]
				if n := x.mapSheets(rep, name, false); n != name {
					b.WriteString("'" + strings.ReplaceAll(n, "'", "''") + "'")
				} else {
					b.WriteString(name)
				}
				i = end
				continue
			}
			b.WriteString(f[i:j])
			i = j
		default:
			b.WriteByte(c)
			i++
		}
	}
	return b.String(), nil
}

// literal returns a formula's string with the replacements made. A
// reference to one of the workbook's sheets in it (INDIRECT("'Sales
// 2025'!A1")) follows the sheet's new name, as a reference outside a string
// does; the rest of the string is text.
func (x *xlsxBook) literal(rep *replacer, s string) string {
	if !strings.Contains(s, "!") {
		return rep.Apply(s)
	}
	var b strings.Builder
	last := 0 // where the text not yet written starts
	ref := func(start, end int, with string) {
		b.WriteString(rep.Apply(s[last:start]))
		b.WriteString(with)
		last = end
	}
	for i := 0; i < len(s); {
		switch {
		case s[i] == '\'':
			j := quotedEnd(s, i, '\'')
			if j < len(s) && s[j] == '!' && s[j-1] == '\'' && x.isSheet(strings.ReplaceAll(s[i+1:j-1], "''", "'")) {
				ref(i, j, x.quotedRef(rep, s[i:j]))
				i = j
				continue
			}
			i++
		case isNameByte(s[i]):
			j := i
			for j < len(s) && isNameByte(s[j]) {
				j++
			}
			if j < len(s) && s[j] == '!' && x.isSheet(s[i:j]) {
				if n := x.mapSheets(rep, s[i:j], false); n != s[i:j] {
					ref(i, j, "'"+strings.ReplaceAll(n, "'", "''")+"'")
				}
			}
			i = j
		default:
			i++
		}
	}
	b.WriteString(rep.Apply(s[last:]))
	return b.String()
}

// isSheet reports whether name is one of the workbook's sheets.
func (x *xlsxBook) isSheet(name string) bool {
	for _, s := range x.sheets {
		if strings.EqualFold(s, name) {
			return true
		}
	}
	return false
}

// externalRefRe matches a sheet of another workbook written without quotes:
// [1]Sheet1! (the name is group 1).
var externalRefRe = regexp.MustCompile(`^\[\d+\]([^\s'"!\[\](),;:+\-*/^&=<>{}]+(?::[^\s'"!\[\](),;:+\-*/^&=<>{}]+)?)!`)

// isNameByte reports whether b can be part of a name in a formula: a sheet,
// a function, a cell, a defined name. Bytes of characters past ASCII count,
// as sheet names may hold any letter.
func isNameByte(b byte) bool {
	return b >= 0x80 || b == '_' || b == '.' || b == '\\' ||
		b >= 'a' && b <= 'z' || b >= 'A' && b <= 'Z' || b >= '0' && b <= '9'
}

// quotedEnd returns the index after the quote that closes the string opening
// at i, where a doubled quote stands for one; len(s) when it is not closed.
func quotedEnd(s string, i int, q byte) int {
	for j := i + 1; j < len(s); j++ {
		if s[j] != q {
			continue
		}
		if j+1 < len(s) && s[j+1] == q {
			j++
			continue
		}
		return j + 1
	}
	return len(s)
}

// bracketEnd returns the index after the bracket that closes the one at i,
// where ' escapes the character after it; len(s) when it is not closed.
func bracketEnd(s string, i int) int {
	depth := 0
	for j := i; j < len(s); j++ {
		switch s[j] {
		case '\'':
			j++
		case '[':
			depth++
		case ']':
			if depth--; depth == 0 {
				return j + 1
			}
		}
	}
	return len(s)
}

// quotedRef rewrites a quoted sheet reference ('Sales 2025'!, '[1]Their
// sheet'!, 'Jan:Mar'!), given with its quotes but not the '!'.
func (x *xlsxBook) quotedRef(rep *replacer, q string) string {
	name := strings.ReplaceAll(q[1:len(q)-1], "''", "'")
	prefix := ""
	if strings.HasPrefix(name, "[") {
		if k := strings.IndexByte(name, ']'); k > 0 {
			prefix, name = name[:k+1], name[k+1:]
		}
	}
	n := x.mapSheets(rep, name, prefix != "")
	if n == name {
		return q
	}
	return "'" + strings.ReplaceAll(prefix+n, "'", "''") + "'"
}

// mapSheets returns the new name of one sheet, or of a range of them
// (Jan:Mar). A sheet of this workbook gets the name the workbook gives it;
// one of another workbook (external) is only fitted.
func (x *xlsxBook) mapSheets(rep *replacer, names string, external bool) string {
	parts := strings.Split(names, ":")
	for i, p := range parts {
		if external {
			parts[i] = fitSheetName(rep, p)
		} else if n, ok := x.renames(rep)[strings.ToLower(p)]; ok {
			parts[i] = n
		}
	}
	return strings.Join(parts, ":")
}

// structuredRef rewrites a reference to a table's columns ([Name],
// [@Email], [[#This Row],[Full name]]), given with its brackets: a column
// name follows the header cell and the table column it names. Special items
// (#Headers) and another workbook's index ([1]) stay.
func structuredRef(rep *replacer, s string, depth int) (string, error) {
	if depth > xlsxMaxRefDepth {
		return "", fmt.Errorf("structured references nest more than %d deep", xlsxMaxRefDepth)
	}
	if len(s) < 2 || s[len(s)-1] != ']' {
		return s, nil
	}
	inner := s[1 : len(s)-1]
	if strings.Contains(inner, "[") {
		var b strings.Builder
		b.WriteByte('[')
		for i := 0; i < len(inner); {
			switch inner[i] {
			case '[':
				j := bracketEnd(inner, i)
				w, err := structuredRef(rep, inner[i:j], depth+1)
				if err != nil {
					return "", err
				}
				b.WriteString(w)
				i = j
			case '\'':
				b.WriteString(inner[i:min(i+2, len(inner))])
				i += 2
			default:
				b.WriteByte(inner[i])
				i++
			}
		}
		b.WriteByte(']')
		return b.String(), nil
	}
	at := ""
	if strings.HasPrefix(inner, "@") {
		at, inner = "@", inner[1:]
	}
	if inner == "" || strings.HasPrefix(inner, "#") || isDigits(inner) {
		return s, nil
	}
	name := unescapeColumn(inner)
	n := rep.Apply(name)
	if n == name {
		return s, nil
	}
	return "[" + at + escapeColumn(n) + "]", nil
}

// columnEscaper and its reverse write the characters a table column name
// escapes with ' in a reference.
var columnEscaper = strings.NewReplacer("'", "''", "[", "'[", "]", "']", "#", "'#")

func escapeColumn(s string) string { return columnEscaper.Replace(s) }

func unescapeColumn(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '\'' && i+1 < len(s) {
			i++
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

// formulaStrings returns the strings in a formula, for detection: its
// functions and references are not text.
func formulaStrings(f string) string {
	var out []string
	for i := 0; i < len(f); {
		switch f[i] {
		case '"':
			j := quotedEnd(f, i, '"')
			out = append(out, strings.ReplaceAll(f[i+1:max(i+1, j-1)], `""`, `"`))
			i = j
		case '\'':
			i = quotedEnd(f, i, '\'')
		case '[':
			i = bracketEnd(f, i)
		default:
			i++
		}
	}
	return strings.Join(out, "\n\n")
}

// Headers and footers. A printed header or footer is text with codes in it:
// &L, &C and &R start its sections, &P is the page number, &"Arial,Bold" a
// font, &12 a size, &K00FF00 a colour, && an ampersand. Names are matched in
// the text between codes, and a fake with an ampersand in it is written as &&.

// headerFooterSegments splits a header or footer into text and codes.
func headerFooterSegments(v string) (segs []string, code []bool) {
	text := 0
	flush := func(i int) {
		if i > text {
			segs, code = append(segs, v[text:i]), append(code, false)
		}
	}
	for i := 0; i < len(v); {
		if v[i] != '&' {
			i++
			continue
		}
		flush(i)
		j := i + 1
		switch {
		case j >= len(v):
		case v[j] == '"':
			j = quotedEnd(v, j, '"')
		case v[j] >= '0' && v[j] <= '9':
			for j < len(v) && v[j] >= '0' && v[j] <= '9' {
				j++
			}
		case v[j] == 'K':
			j = min(len(v), j+7)
		default:
			j++
		}
		segs, code = append(segs, v[i:j]), append(code, true)
		i, text = j, j
	}
	flush(len(v))
	return segs, code
}

func headerFooterText(v string) string {
	segs, code := headerFooterSegments(v)
	var out []string
	for i, s := range segs {
		if !code[i] {
			out = append(out, s)
		}
	}
	return strings.Join(out, "\n\n")
}

func rewriteHeaderFooter(rep *replacer, v string) (string, error) {
	segs, code := headerFooterSegments(v)
	var b strings.Builder
	for i, s := range segs {
		if code[i] {
			b.WriteString(s)
		} else {
			b.WriteString(strings.ReplaceAll(rep.Apply(s), "&", "&&"))
		}
	}
	return b.String(), nil
}
