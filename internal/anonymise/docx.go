package anonymise

import (
	"context"
	"regexp"
	"strconv"
	"strings"
)

// A DOCX is an Office package (ooxml.go) around word/document.xml. Each part
// is read by a rule that knows what it can hold: body text, comments and
// tracked changes with their authors, document properties, relationships with
// links and file paths, images and embedded files.

var (
	nsW   = namespaces("http://schemas.openxmlformats.org/wordprocessingml/2006/main", "http://purl.oclc.org/ooxml/wordprocessingml/main")
	nsW15 = namespaces("http://schemas.microsoft.com/office/word/2012/wordml")
	nsO   = namespaces("urn:schemas-microsoft-com:office:office")
	nsM   = namespaces("http://schemas.openxmlformats.org/officeDocument/2006/math", "http://purl.oclc.org/ooxml/officeDocument/math")
)

// docxFormat is Word's. Parts kept as they are hold structure and formatting
// only.
var docxFormat = &ooxmlFormat{
	name: "DOCX", article: "a", main: "word/document.xml", legacy: ".doc", ext: ".docx",
	rules: []partRule{
		{regexp.MustCompile(`^\[content_types\]\.xml$`), ruleContentTypes},
		{regexp.MustCompile(`(^|/)_rels/[^/]*\.rels$`), ruleRels},
		{regexp.MustCompile(`^word/(glossary/)?(document|header\d*|footer\d*|footnotes|endnotes|comments)\.xml$`), ruleBody},
		{regexp.MustCompile(`^word/(glossary/)?settings\.xml$`), ruleSettings},
		{regexp.MustCompile(`^word/people\.xml$`), rulePeople},
		{regexp.MustCompile(`^docprops/core\.xml$`), ruleCore},
		{regexp.MustCompile(`^docprops/app\.xml$`), ruleApp},
		{regexp.MustCompile(`^docprops/custom\.xml$`), ruleCustom},
		{regexp.MustCompile(`^word/diagrams/(data|drawing)\d*\.xml$`), ruleDrawing},
		{regexp.MustCompile(`^word/charts/chart\d*\.xml$`), ruleChart},
		{regexp.MustCompile(`^customxml/item\d*\.xml$`), ruleAllText},
		{regexp.MustCompile(`^word/(glossary/)?(styles|styleswitheffects|numbering|fonttable|websettings|commentsextended|commentsids|commentsextensible)\.xml$`), ruleKeep},
		{regexp.MustCompile(`^word/theme/theme\d*\.xml$`), ruleKeep},
		{regexp.MustCompile(`^word/fonts/[^/]+$`), ruleKeep},
		{regexp.MustCompile(`^customxml/itemprops\d*\.xml$`), ruleKeep},
		{regexp.MustCompile(`^word/charts/(colors|style)\d*\.xml$`), ruleKeep},
		{regexp.MustCompile(`^word/diagrams/(layout|quickstyle|colors)\d*\.xml$`), ruleKeep},
		{regexp.MustCompile(`^(word/media/[^/]+|docprops/thumbnail\.[a-z0-9]+)$`), ruleMedia},
		{regexp.MustCompile(`^word/embeddings/[^/]+$`), ruleEmbedding},
	},
	office:     regexp.MustCompile(`^(\[content_types\]\.xml|docprops/(app|core|custom)\.xml|docprops/thumbnail\.[a-z0-9]+|word/(glossary/)?(document|header\d*|footer\d*|footnotes|endnotes|comments|settings|styles|styleswitheffects|numbering|fonttable|websettings|commentsextended|commentsids|commentsextensible)\.xml|word/people\.xml|word/theme/theme\d*\.xml|word/charts/(chart|colors|style)\d*\.xml|word/diagrams/(data|drawing|layout|quickstyle|colors)\d*\.xml|word/media/(image\d*|thumbnail)\.[a-z0-9]+|customxml/(item|itemprops)\d*\.xml)$`),
	knownDrops: regexp.MustCompile(`^(word/(vbaproject\.bin|vbadata\.xml|customizations\.xml|attachedtoolbars\.bin|keymapcustomizations\.xml|activex/activex\d+\.(xml|bin)|ink/ink\d+\.xml|printersettings/printersettings\d+\.bin|webextensions/(webextension\d+|taskpanes)\.xml|_rels/(vbaproject\.bin|customizations\.xml|attachedtoolbars\.bin)\.rels|activex/_rels/activex\d+\.xml\.rels|webextensions/_rels/taskpanes\.xml\.rels)|docprops/meta\.xml|customui/customui(14)?\.xml|customui/_rels/customui(14)?\.xml\.rels)$`),
	collect:    collectMarkup,
}

// docxPara is a paragraph being read: its runs of text, split where a tab,
// break or field ends one.
type docxPara struct {
	segments [][]ooxmlPiece
	cur      []ooxmlPiece
	curKind  string
}

func (pp *docxPara) flush() {
	if len(pp.cur) > 0 {
		pp.segments = append(pp.segments, pp.cur)
	}
	pp.cur, pp.curKind = nil, ""
}

// collectMarkup finds the values of a part whose text is in paragraphs:
// Word's body, and the DrawingML text of shapes, charts and SmartArt both
// formats use. It also reads the parts both formats share (document
// properties, custom XML, relationships).
func collectMarkup(pkg *ooxmlPackage, p *ooxmlPart) {
	var stack []xmlName
	var paras []*docxPara
	mailMerge := 0
	for i := range p.nodes {
		n := &p.nodes[i]
		switch n.kind {
		case xmlStart:
			stack = append(stack, n.name)
			if is(n.name, nsW, "p") || is(n.name, nsA, "p") {
				paras = append(paras, &docxPara{})
			}
			if len(paras) > 0 && isTextBreak(n.name) {
				paras[len(paras)-1].flush()
			}
			if is(n.name, nsW, "mailMerge") {
				mailMerge++
			}
			collectAttrs(pkg, p, n, mailMerge > 0)
		case xmlEnd:
			if len(stack) > 0 {
				stack = stack[:len(stack)-1]
			}
			if (is(n.name, nsW, "p") || is(n.name, nsA, "p")) && len(paras) > 0 {
				pp := paras[len(paras)-1]
				paras = paras[:len(paras)-1]
				pp.flush()
				for _, seg := range pp.segments {
					pkg.add(ooxmlUnit{part: p, pieces: seg, initialsOf: -1})
				}
			}
			if is(n.name, nsW, "mailMerge") {
				mailMerge--
			}
		case xmlText:
			if len(stack) == 0 {
				continue
			}
			collectText(pkg, p, n, stack, paras)
		}
	}
}

// isTextBreak reports whether an element ends a run of text: no name is
// matched across a tab, a break or a field code.
func isTextBreak(n xmlName) bool {
	if nsW[n.space] {
		switch n.local {
		case "tab", "br", "cr", "fldChar", "sym", "ptab", "noBreakHyphen", "softHyphen":
			return true
		}
	}
	return is(n, nsA, "br")
}

func collectText(pkg *ooxmlPackage, p *ooxmlPart, n *xmlNode, stack []xmlName, paras []*docxPara) {
	parent := stack[len(stack)-1]
	switch p.rule {
	case ruleBody, ruleDrawing, ruleChart:
		kind := ""
		switch {
		case is(parent, nsW, "t"), is(parent, nsW, "delText"), is(parent, nsA, "t"):
			kind = parent.local
		case is(parent, nsW, "instrText"), is(parent, nsW, "delInstrText"), is(parent, nsM, "t"):
			// A field's instruction (HYPERLINK "mailto:…"), kept or
			// deleted, or the text of an equation.
			pkg.addText(p, n, "")
			return
		case p.rule == ruleChart && is(parent, nsC, "v"):
			if _, err := strconv.ParseFloat(strings.TrimSpace(n.text), 64); err != nil {
				pkg.addText(p, n, "") // a category or series name, not a number
			}
			return
		default:
			return
		}
		if len(paras) == 0 {
			pkg.addText(p, n, "")
			return
		}
		// Deleted and kept text are not joined: "Jane" deleted for "John"
		// is not "JaneJohn".
		pp := paras[len(paras)-1]
		if pp.curKind != "" && pp.curKind != kind {
			pp.flush()
		}
		pp.curKind = kind
		pp.cur = append(pp.cur, ooxmlPiece{start: n.start, end: n.end, value: n.text})
	case ruleCore:
		switch {
		case is(parent, nsDC, "creator"), is(parent, nsCP, "lastModifiedBy"):
			pkg.addText(p, n, EntityPerson)
		case is(parent, nsDC, "title"), is(parent, nsDC, "subject"), is(parent, nsCP, "keywords"),
			is(parent, nsDC, "description"), is(parent, nsCP, "category"), is(parent, nsCP, "contentStatus"):
			pkg.addText(p, n, "")
		}
	case ruleApp:
		switch {
		case is(parent, nsEP, "Company"):
			pkg.addText(p, n, EntityCompany)
		case is(parent, nsEP, "Manager"):
			pkg.addText(p, n, EntityPerson)
		case is(parent, nsEP, "Template"):
			pkg.addText(p, n, "")
		case is(parent, nsEP, "HyperlinkBase"):
			pkg.add(ooxmlUnit{part: p, fake: fakeTarget, initialsOf: -1, pieces: []ooxmlPiece{{start: n.start, end: n.end, value: n.text}}})
		case is(parent, nsVT, "lpstr") && len(stack) >= 3 && is(stack[len(stack)-3], nsEP, "TitlesOfParts"):
			pkg.addText(p, n, "")
		}
	case ruleCustom:
		if nsVT[parent.space] && strings.TrimSpace(n.text) != "" {
			pkg.addText(p, n, "")
		}
	case ruleAllText:
		if strings.TrimSpace(n.text) != "" {
			pkg.addText(p, n, "")
		}
	}
}

func collectAttrs(pkg *ooxmlPackage, p *ooxmlPart, n *xmlNode, inMailMerge bool) {
	switch p.rule {
	case ruleBody, ruleDrawing, ruleChart:
		author := -1
		for i := range n.attrs {
			a := &n.attrs[i]
			switch {
			case is(a.name, nsW, "author"):
				author = pkg.addAttr(p, a, EntityPerson)
			case is(a.name, nsW, "instr"), is(a.name, nsW, "tooltip"), is(a.name, nsO, "title"),
				a.name.space == "" && (a.name.local == "descr" || a.name.local == "title" || a.name.local == "alt"):
				pkg.addAttr(p, a, "")
			case a.name.space == "" && a.name.local == "name" && (n.name.local == "cNvPr" || n.name.local == "docPr"):
				// A picture or shape's name: Word keeps an inserted
				// picture's file name here ("Jane Doe passport.jpg").
				if fileExt(a.value) != "" {
					pkg.addFake(p, a, fakePictureName)
				} else {
					pkg.addAttr(p, a, "")
				}
			case nsW[n.name.space] && is(a.name, nsW, "val") && docxFormValues[n.name.local],
				is(n.name, nsW, "listItem") && (is(a.name, nsW, "displayText") || is(a.name, nsW, "value")):
				// A form field's default, help and status text and
				// drop-down entries, and a content control's list items.
				pkg.addAttr(p, a, "")
			}
		}
		// Initials follow the fake author, so they never spell the real one.
		if _, i := n.attr(xmlName{space: n.name.space, local: "initials"}); i >= 0 && author >= 0 && nsW[n.name.space] {
			pkg.add(ooxmlUnit{part: p, initialsOf: author, pieces: []ooxmlPiece{{start: n.attrs[i].start, end: n.attrs[i].end, value: n.attrs[i].value, attr: true}}})
		}
	case ruleSettings:
		_, vi := n.attr(xmlName{space: n.name.space, local: "val"})
		switch {
		case vi < 0 || !nsW[n.name.space]:
		case n.name.local == "docVar":
			pkg.addAttr(p, &n.attrs[vi], "")
		case inMailMerge && mailMergeSources[n.name.local]:
			// Where a mail merge reads from: a connection string with a
			// file path, a query naming tables. Settings, not content, so
			// replaced whole.
			pkg.addFake(p, &n.attrs[vi], fakeOpaque)
		}
	case rulePeople:
		for i := range n.attrs {
			a := &n.attrs[i]
			switch {
			case is(n.name, nsW15, "person") && is(a.name, nsW15, "author"):
				pkg.addAttr(p, a, EntityPerson)
			case is(n.name, nsW15, "presenceInfo") && is(a.name, nsW15, "userId"):
				pkg.addFake(p, a, fakeUserID)
			}
		}
	case ruleRels:
		if !is(n.name, nsPR, "Relationship") {
			return
		}
		if mode, _ := n.attr(xmlName{local: "TargetMode"}); mode != "External" {
			return
		}
		if _, i := n.attr(xmlName{local: "Target"}); i >= 0 {
			pkg.addFake(p, &n.attrs[i], fakeTarget)
		}
	}
}

// docxFormValues are the elements of a legacy form field (w:ffData) whose
// w:val is text.
var docxFormValues = map[string]bool{"default": true, "helpText": true, "statusText": true, "listEntry": true}

// fakePictureName replaces a picture's file name by a keyed one with its
// extension.
func fakePictureName(_ context.Context, e *Engine, v string) (string, error) {
	return "picture-" + e.key.Hex("file", v, 6) + fileExt(v), nil
}

// mailMergeSources are the mail-merge settings that say where its data is.
var mailMergeSources = map[string]bool{"connectString": true, "query": true, "dataSource": true, "headerSource": true, "addressFieldName": true}
