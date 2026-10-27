package anonymise

import (
	"archive/zip"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"path"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/martinsuchenak/exemplar/internal/textswap"
)

// A DOCX is a zip of XML parts. Each part is read by a rule that knows what
// it can hold: body text, comments and tracked changes with their authors,
// document properties, relationships with links and file paths, images and
// embedded files. Text is rewritten in place (xmlscan.go), images and
// embedded files are replaced, and a part no rule knows is dropped, since
// it could hold anything. The output must open as a DOCX again: every part
// well-formed, every relationship and content type in place.

var (
	nsW   = namespaces("http://schemas.openxmlformats.org/wordprocessingml/2006/main", "http://purl.oclc.org/ooxml/wordprocessingml/main")
	nsA   = namespaces("http://schemas.openxmlformats.org/drawingml/2006/main", "http://purl.oclc.org/ooxml/drawingml/main")
	nsR   = namespaces("http://schemas.openxmlformats.org/officeDocument/2006/relationships", "http://purl.oclc.org/ooxml/officeDocument/relationships")
	nsC   = namespaces("http://schemas.openxmlformats.org/drawingml/2006/chart", "http://purl.oclc.org/ooxml/drawingml/chart")
	nsW15 = namespaces("http://schemas.microsoft.com/office/word/2012/wordml")
	nsDC  = namespaces("http://purl.org/dc/elements/1.1/")
	nsCP  = namespaces("http://schemas.openxmlformats.org/package/2006/metadata/core-properties")
	nsEP  = namespaces("http://schemas.openxmlformats.org/officeDocument/2006/extended-properties", "http://purl.oclc.org/ooxml/officeDocument/extendedProperties")
	nsVT  = namespaces("http://schemas.openxmlformats.org/officeDocument/2006/docPropsVTypes", "http://purl.oclc.org/ooxml/officeDocument/docPropsVTypes")
	nsO   = namespaces("urn:schemas-microsoft-com:office:office")
	nsPR  = namespaces("http://schemas.openxmlformats.org/package/2006/relationships")
	nsCT  = namespaces("http://schemas.openxmlformats.org/package/2006/content-types")
)

// namespaces returns a set of namespace URIs. Unlike set, it keeps case:
// URIs are compared exactly.
func namespaces(uris ...string) map[string]bool {
	m := make(map[string]bool, len(uris))
	for _, u := range uris {
		m[u] = true
	}
	return m
}

// is reports whether n is local in one of the namespaces ns.
func is(n xmlName, ns map[string]bool, local string) bool {
	return n.local == local && ns[n.space]
}

const (
	// docxMaxParts caps the entries of one DOCX.
	docxMaxParts = 2000
	// A DOCX may expand to docxRatio times its size, and to at least
	// docxMinExpand bytes: XML compresses well, a zip bomb far better.
	docxRatio     = 100
	docxMinExpand = 16 << 20
)

type docxRule int

const (
	ruleDrop docxRule = iota
	ruleKeep
	ruleContentTypes
	ruleRels
	ruleBody
	ruleSettings
	rulePeople
	ruleCore
	ruleApp
	ruleCustom
	ruleDrawing
	ruleChart
	ruleAllText
	ruleMedia
	ruleEmbedding
)

// docxRules says how each part is treated, by its lower-cased name. Parts
// kept as they are hold structure and formatting only.
var docxRules = []struct {
	re   *regexp.Regexp
	rule docxRule
}{
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
}

func ruleFor(name string) docxRule {
	lower := strings.ToLower(name)
	for _, r := range docxRules {
		if r.re.MatchString(lower) {
			return r.rule
		}
	}
	return ruleDrop
}

// docxPart is one entry of the package.
type docxPart struct {
	name  string
	data  []byte
	rule  docxRule
	nodes []xmlNode // for XML parts that are read
}

// docxPackage is a DOCX read for anonymising.
type docxPackage struct {
	parts  []*docxPart
	byName map[string]*docxPart
	units  []docxUnit
}

// docxUnit is one value to search and rewrite: a run of text across one or
// more w:t elements, or an attribute value.
type docxUnit struct {
	part   *docxPart
	pieces []docxPiece
	// entity, when set, is what the whole value is (an author is a person).
	entity string
	// fake, when set, makes the new value directly instead of detection.
	fake func(ctx context.Context, e *Engine, v string) (string, error)
	// initialsOf is the unit holding the author these are the initials
	// of, or -1.
	initialsOf int
}

// docxPiece is one text node or attribute value of a unit.
type docxPiece struct {
	start, end int
	value      string
	attr       bool
}

func (u *docxUnit) text() string {
	var b strings.Builder
	for _, p := range u.pieces {
		b.WriteString(p.value)
	}
	return b.String()
}

// docxHandler reads and rewrites DOCX files.
type docxHandler struct{}

func (docxHandler) prepare(ctx context.Context, e *Engine, file File) error {
	pkg, err := openDOCX(file.Data)
	if err != nil {
		return err
	}
	return e.learnNames(ctx, pkg.texts())
}

func (docxHandler) anonymise(ctx context.Context, e *Engine, file File) (*Output, error) {
	pkg, err := openDOCX(file.Data)
	if err != nil {
		return nil, err
	}
	pairs, f, rep, err := e.docReplacements(ctx, pkg.texts())
	if err != nil {
		return nil, err
	}
	out := &Output{Replacements: pairs, Entities: f.entities}
	if out.Data, err = pkg.rewrite(ctx, e, rep, out); err != nil {
		return nil, err
	}
	if err := checkDOCX(out.Data); err != nil {
		return nil, fmt.Errorf("anonymised output is not a valid DOCX: %w", err)
	}
	return out, nil
}

// readDOCX reads every entry of a DOCX, within the limits.
func readDOCX(data []byte) ([]*docxPart, error) {
	if bytes.HasPrefix(data, []byte{0xd0, 0xcf, 0x11, 0xe0}) {
		return nil, errors.New("not a DOCX: this looks like a password-protected or old .doc file; save it as an unprotected .docx first")
	}
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return nil, errors.New("not a DOCX: not a zip archive")
	}
	if len(zr.File) > docxMaxParts {
		return nil, fmt.Errorf("the DOCX has over %d parts", docxMaxParts)
	}
	budget := int64(max(len(data)*docxRatio, docxMinExpand))
	var parts []*docxPart
	seen := map[string]bool{}
	for _, f := range zr.File {
		if strings.HasSuffix(f.Name, "/") {
			continue
		}
		if f.Name == "" || strings.HasPrefix(f.Name, "/") || strings.Contains(f.Name, `\`) || path.Clean(f.Name) != f.Name {
			return nil, fmt.Errorf("the DOCX has a part with an unsafe name")
		}
		if seen[strings.ToLower(f.Name)] {
			return nil, fmt.Errorf("the DOCX holds %s twice", f.Name)
		}
		seen[strings.ToLower(f.Name)] = true
		rc, err := f.Open()
		if err != nil {
			return nil, fmt.Errorf("reading %s: %w", f.Name, err)
		}
		b, err := io.ReadAll(io.LimitReader(rc, budget+1))
		_ = rc.Close()
		if err != nil {
			return nil, fmt.Errorf("reading %s: %w", f.Name, err)
		}
		if budget -= int64(len(b)); budget < 0 {
			return nil, fmt.Errorf("the DOCX expands to more than %d times its size", docxRatio)
		}
		parts = append(parts, &docxPart{name: f.Name, data: b, rule: ruleFor(f.Name)})
	}
	if !seen["word/document.xml"] || !seen["[content_types].xml"] {
		return nil, errors.New("not a DOCX: it has no word/document.xml")
	}
	return parts, nil
}

// openDOCX reads a DOCX and finds every value to search.
func openDOCX(data []byte) (*docxPackage, error) {
	parts, err := readDOCX(data)
	if err != nil {
		return nil, err
	}
	pkg := &docxPackage{parts: parts, byName: map[string]*docxPart{}}
	for _, p := range parts {
		pkg.byName[p.name] = p
		switch p.rule {
		case ruleKeep, ruleMedia, ruleEmbedding, ruleDrop:
			continue
		}
		if p.nodes, err = scanXML(p.data); err != nil {
			return nil, fmt.Errorf("%s: %w", p.name, err)
		}
		pkg.collect(p)
	}
	return pkg, nil
}

// texts returns the values to detect in; directly faked values are not
// searched.
func (pkg *docxPackage) texts() []docText {
	var out []docText
	for i := range pkg.units {
		u := &pkg.units[i]
		if u.fake != nil || u.initialsOf >= 0 {
			continue
		}
		if v := u.text(); strings.TrimSpace(v) != "" {
			out = append(out, docText{value: v, entity: u.entity})
		}
	}
	return out
}

func (pkg *docxPackage) add(u docxUnit) int {
	pkg.units = append(pkg.units, u)
	return len(pkg.units) - 1
}

func (pkg *docxPackage) addText(p *docxPart, n *xmlNode, entity string) {
	pkg.add(docxUnit{part: p, entity: entity, initialsOf: -1, pieces: []docxPiece{{start: n.start, end: n.end, value: n.text}}})
}

func (pkg *docxPackage) addAttr(p *docxPart, a *xmlAttr, entity string) int {
	return pkg.add(docxUnit{part: p, entity: entity, initialsOf: -1, pieces: []docxPiece{{start: a.start, end: a.end, value: a.value, attr: true}}})
}

func (pkg *docxPackage) addFake(p *docxPart, a *xmlAttr, fake func(context.Context, *Engine, string) (string, error)) {
	pkg.add(docxUnit{part: p, fake: fake, initialsOf: -1, pieces: []docxPiece{{start: a.start, end: a.end, value: a.value, attr: true}}})
}

// docxPara is a paragraph being read: its runs of text, split where a tab,
// break or field ends one.
type docxPara struct {
	segments [][]docxPiece
	cur      []docxPiece
	curKind  string
}

func (pp *docxPara) flush() {
	if len(pp.cur) > 0 {
		pp.segments = append(pp.segments, pp.cur)
	}
	pp.cur, pp.curKind = nil, ""
}

// collect finds a part's values by its rule.
func (pkg *docxPackage) collect(p *docxPart) {
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
			pkg.collectAttrs(p, n, mailMerge > 0)
		case xmlEnd:
			if len(stack) > 0 {
				stack = stack[:len(stack)-1]
			}
			if (is(n.name, nsW, "p") || is(n.name, nsA, "p")) && len(paras) > 0 {
				pp := paras[len(paras)-1]
				paras = paras[:len(paras)-1]
				pp.flush()
				for _, seg := range pp.segments {
					pkg.add(docxUnit{part: p, pieces: seg, initialsOf: -1})
				}
			}
			if is(n.name, nsW, "mailMerge") {
				mailMerge--
			}
		case xmlText:
			if len(stack) == 0 {
				continue
			}
			pkg.collectText(p, n, stack, paras)
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

func (pkg *docxPackage) collectText(p *docxPart, n *xmlNode, stack []xmlName, paras []*docxPara) {
	parent := stack[len(stack)-1]
	switch p.rule {
	case ruleBody, ruleDrawing, ruleChart:
		kind := ""
		switch {
		case is(parent, nsW, "t"), is(parent, nsW, "delText"), is(parent, nsA, "t"):
			kind = parent.local
		case is(parent, nsW, "instrText"):
			pkg.addText(p, n, "") // a field's instruction: HYPERLINK "mailto:…"
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
		pp.cur = append(pp.cur, docxPiece{start: n.start, end: n.end, value: n.text})
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
			pkg.add(docxUnit{part: p, fake: fakeTarget, initialsOf: -1, pieces: []docxPiece{{start: n.start, end: n.end, value: n.text}}})
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

func (pkg *docxPackage) collectAttrs(p *docxPart, n *xmlNode, inMailMerge bool) {
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
			}
		}
		// Initials follow the fake author, so they never spell the real one.
		if _, i := n.attr(xmlName{space: n.name.space, local: "initials"}); i >= 0 && author >= 0 && nsW[n.name.space] {
			pkg.add(docxUnit{part: p, initialsOf: author, pieces: []docxPiece{{start: n.attrs[i].start, end: n.attrs[i].end, value: n.attrs[i].value, attr: true}}})
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

// fakeTarget replaces a link or file path outside the document: a URL or
// address like any other, a path (a template on C:\Users\jdoe\…, a linked
// file) by a keyed one that keeps its extension.
func fakeTarget(ctx context.Context, e *Engine, v string) (string, error) {
	lower := strings.ToLower(strings.TrimSpace(v))
	for _, prefix := range []string{"http://", "https://", "ftp://", "mailto:", "www."} {
		if strings.HasPrefix(lower, prefix) {
			return e.fakeURL(ctx, strings.TrimSpace(v))
		}
	}
	if lower == "" || strings.HasPrefix(lower, "#") {
		return v, nil // empty, or a place in the document
	}
	ext := strings.ToLower(path.Ext(strings.ReplaceAll(v, `\`, "/")))
	if len(ext) > 6 || strings.ContainsAny(ext, " ?#&=") {
		ext = ""
	}
	return "file:///anonymised/" + e.key.Hex("path", v, 10) + ext, nil
}

// mailMergeSources are the mail-merge settings that say where its data is.
var mailMergeSources = map[string]bool{"connectString": true, "query": true, "dataSource": true, "headerSource": true, "addressFieldName": true}

// fakeOpaque replaces a value whole with a keyed token.
func fakeOpaque(_ context.Context, e *Engine, v string) (string, error) {
	if strings.TrimSpace(v) == "" {
		return v, nil
	}
	return "anonymised-" + e.key.Hex("opaque", v, 12), nil
}

var hexRunRe = regexp.MustCompile(`[0-9A-Fa-f]{8,}`)

// fakeUserID replaces a reviewer's account id in people.xml: the addresses
// in it, and its hex runs (GUIDs, Live IDs) by keyed ones of the same
// length. One that is neither is replaced whole.
func fakeUserID(ctx context.Context, e *Engine, v string) (string, error) {
	var ferr error
	out := emailRe.ReplaceAllStringFunc(v, func(a string) string {
		fake, err := e.fakeEmail(ctx, a)
		if err != nil {
			ferr = err
		}
		return fake
	})
	out = hexRunRe.ReplaceAllStringFunc(out, func(h string) string {
		return e.key.Hex("userid", h, min(len(h), 64))
	})
	if ferr != nil {
		return "", ferr
	}
	if out == v && strings.TrimSpace(v) != "" {
		out = "anonymised-" + e.key.Hex("userid", v, 12)
	}
	return out, nil
}

// initials returns the initials of a name: "Kenneth Huang" gives "KH".
func initials(name string) string {
	var b strings.Builder
	for _, w := range strings.Fields(name) {
		r, _ := utf8.DecodeRuneInString(w)
		b.WriteRune(unicode.ToUpper(r))
	}
	return b.String()
}

// distribute writes the matches into the pieces a value is split across:
// each fake into the piece where its match starts, and the matched text
// taken out of the pieces after it. The pieces keep everything else.
func distribute(pieces []docxPiece, matches []textswap.Match) []string {
	offs := make([]int, len(pieces)+1)
	for i, p := range pieces {
		offs[i+1] = offs[i] + len(p.value)
	}
	text := strings.Builder{}
	for _, p := range pieces {
		text.WriteString(p.value)
	}
	s := text.String()
	out := make([]strings.Builder, len(pieces))
	copyRange := func(from, to int) {
		for i := range pieces {
			a, b := max(from, offs[i]), min(to, offs[i+1])
			if a < b {
				out[i].WriteString(s[a:b])
			}
		}
	}
	pos := 0
	for _, m := range matches {
		copyRange(pos, m.Start)
		i := 0
		for i < len(pieces)-1 && offs[i+1] <= m.Start {
			i++
		}
		out[i].WriteString(m.To)
		pos = m.End
	}
	copyRange(pos, len(s))
	values := make([]string, len(pieces))
	for i := range out {
		values[i] = out[i].String()
	}
	return values
}

// docxRename is a binary part given a new name, and with it, for an image
// whose format changed, a new content type.
type docxRename struct {
	to          string
	contentType string // empty: the part keeps its type
}

// rewrite returns the anonymised package and counts what it replaced and
// dropped in out.
func (pkg *docxPackage) rewrite(ctx context.Context, e *Engine, rep *replacer, out *Output) ([]byte, error) {
	edits := map[*docxPart][]xmlEdit{}
	newText := make([]string, len(pkg.units))
	for i := range pkg.units {
		u := &pkg.units[i]
		old := u.text()
		var values []string
		switch {
		case u.initialsOf >= 0:
			if newText[u.initialsOf] == pkg.units[u.initialsOf].text() {
				continue // the author did not change
			}
			values = []string{initials(newText[u.initialsOf])}
		case u.fake != nil:
			v, err := u.fake(ctx, e, old)
			if err != nil {
				return nil, err
			}
			values = []string{v}
		default:
			values = distribute(u.pieces, rep.Matches(old))
		}
		var b strings.Builder
		for j, p := range u.pieces {
			b.WriteString(values[j])
			if values[j] == p.value {
				continue
			}
			esc := textEscaper
			if p.attr {
				esc = attrEscaper
			}
			edits[u.part] = append(edits[u.part], xmlEdit{start: p.start, end: p.end, with: esc.Replace(values[j])})
		}
		newText[i] = b.String()
	}

	// Images and embedded files are replaced; a part no rule knows is
	// dropped, with its relationships and content type.
	people := e.fillerPeople()
	renames := map[string]docxRename{}
	dropped := map[string]bool{}
	for _, p := range pkg.parts {
		switch p.rule {
		case ruleDrop:
			dropped[p.name] = true
		case ruleMedia:
			out.Attachments++
			ext := strings.ToLower(path.Ext(p.name))
			switch ext {
			case ".png", ".jpg", ".jpeg", ".gif":
				p.data = e.placeholder(mediaTypeOf(ext), path.Base(p.name), len(p.data), people)
			case ".svg":
				p.data = []byte(`<svg xmlns="http://www.w3.org/2000/svg" width="64" height="64"><rect width="64" height="64" fill="#d0d0d0"/></svg>`)
			default:
				// EMF, WMF, BMP, TIFF…: a PNG in its place.
				to := pkg.freeName(strings.TrimSuffix(p.name, path.Ext(p.name)), ".png", renames)
				renames[p.name] = docxRename{to: to, contentType: "image/png"}
				p.data = e.placeholder("image/png", path.Base(to), len(p.data), people)
			}
		case ruleEmbedding:
			out.Attachments++
			ext := strings.ToLower(path.Ext(p.name))
			to := pkg.freeName(path.Dir(p.name)+"/embedding-"+e.key.Hex("file", path.Base(p.name), 6), ext, renames)
			renames[p.name] = docxRename{to: to}
			p.data = e.placeholder(mediaTypeOf(ext), path.Base(to), len(p.data), people)
		}
	}
	// A part's relationships go with it.
	for _, p := range pkg.parts {
		if p.rule == ruleRels && dropped[relsSource(p.name)] {
			dropped[p.name] = true
		}
	}

	for _, p := range pkg.parts {
		switch p.rule {
		case ruleRels:
			edits[p] = append(edits[p], relsEdits(p, renames, dropped)...)
		case ruleContentTypes:
			edits[p] = append(edits[p], contentTypeEdits(p, renames, dropped)...)
		}
	}

	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	write := func(p *docxPart) error {
		data := p.data
		if len(edits[p]) > 0 {
			var err error
			if data, err = splice(p.data, edits[p]); err != nil {
				return fmt.Errorf("%s: %w", p.name, err)
			}
		}
		name := p.name
		if r, ok := renames[name]; ok {
			name = r.to
		}
		w, err := zw.CreateHeader(&zip.FileHeader{Name: name, Method: zip.Deflate, Modified: docxTime})
		if err != nil {
			return err
		}
		_, err = w.Write(data)
		return err
	}
	// [Content_Types].xml goes first, as Office writes it.
	for _, first := range []bool{true, false} {
		for _, p := range pkg.parts {
			if (p.rule == ruleContentTypes) != first || dropped[p.name] {
				continue
			}
			if err := write(p); err != nil {
				return nil, err
			}
		}
	}
	if err := zw.Close(); err != nil {
		return nil, err
	}
	for _, p := range pkg.parts {
		if dropped[p.name] {
			out.DroppedParts = append(out.DroppedParts, p.name)
		}
	}
	return buf.Bytes(), nil
}

// docxTime is every entry's time, so the same input gives the same bytes.
var docxTime = time.Date(1980, 1, 1, 0, 0, 0, 0, time.UTC)

// freeName returns base+ext, or base-2+ext… when that name is taken.
func (pkg *docxPackage) freeName(base, ext string, renames map[string]docxRename) string {
	taken := func(n string) bool {
		if _, ok := pkg.byName[n]; ok {
			return true
		}
		for _, r := range renames {
			if r.to == n {
				return true
			}
		}
		return false
	}
	name := base + ext
	for n := 2; taken(name); n++ {
		name = fmt.Sprintf("%s-%d%s", base, n, ext)
	}
	return name
}

// relsSource returns the part a relationships part belongs to:
// word/_rels/document.xml.rels belongs to word/document.xml, and _rels/.rels
// to the package ("").
func relsSource(rels string) string {
	dir := path.Dir(path.Dir(rels))
	base := strings.TrimSuffix(path.Base(rels), ".rels")
	if dir == "." {
		return base
	}
	return dir + "/" + base
}

// relsFor returns the relationships part of a part.
func relsFor(part string) string {
	dir, base := path.Dir(part), path.Base(part)
	if dir == "." {
		return "_rels/" + base + ".rels"
	}
	return dir + "/_rels/" + base + ".rels"
}

// resolveTarget returns the part an internal target points to from source.
func resolveTarget(source, target string) string {
	if strings.HasPrefix(target, "/") {
		return strings.TrimPrefix(path.Clean(target), "/")
	}
	dir := path.Dir(source)
	if source == "" || dir == "." {
		return path.Clean(target)
	}
	return path.Join(dir, target)
}

// relsEdits points relationships at renamed parts and removes those to
// dropped parts.
func relsEdits(p *docxPart, renames map[string]docxRename, dropped map[string]bool) []xmlEdit {
	var edits []xmlEdit
	source := relsSource(p.name)
	for i := range p.nodes {
		n := &p.nodes[i]
		if n.kind != xmlStart || !is(n.name, nsPR, "Relationship") {
			continue
		}
		if mode, _ := n.attr(xmlName{local: "TargetMode"}); mode == "External" {
			continue
		}
		target, ti := n.attr(xmlName{local: "Target"})
		if ti < 0 {
			continue
		}
		part := resolveTarget(source, target)
		switch {
		case dropped[part]:
			edits = append(edits, xmlEdit{start: n.start, end: p.nodes[n.match].end})
		case renames[part].to != "":
			to := renames[part].to
			newTarget := strings.TrimSuffix(target, path.Base(target)) + path.Base(to)
			edits = append(edits, xmlEdit{start: n.attrs[ti].start, end: n.attrs[ti].end, with: attrEscaper.Replace(newTarget)})
		}
	}
	return edits
}

// contentTypeEdits removes the overrides of dropped parts, moves those of
// renamed ones, and adds a default for an extension that is new.
func contentTypeEdits(p *docxPart, renames map[string]docxRename, dropped map[string]bool) []xmlEdit {
	var edits []xmlEdit
	defaults := map[string]bool{}
	typesEnd := -1
	for i := range p.nodes {
		n := &p.nodes[i]
		if n.kind != xmlStart {
			continue
		}
		switch {
		case is(n.name, nsCT, "Types"):
			typesEnd = n.end
		case is(n.name, nsCT, "Default"):
			ext, _ := n.attr(xmlName{local: "Extension"})
			defaults[strings.ToLower(ext)] = true
		case is(n.name, nsCT, "Override"):
			name, ni := n.attr(xmlName{local: "PartName"})
			part := strings.TrimPrefix(name, "/")
			r, renamed := renames[part]
			switch {
			case dropped[part], renamed && r.contentType != "":
				edits = append(edits, xmlEdit{start: n.start, end: p.nodes[n.match].end})
			case renamed:
				edits = append(edits, xmlEdit{start: n.attrs[ni].start, end: n.attrs[ni].end, with: attrEscaper.Replace("/" + r.to)})
			}
		}
	}
	added := map[string]bool{}
	for _, r := range renames {
		ext := strings.TrimPrefix(strings.ToLower(path.Ext(r.to)), ".")
		if r.contentType == "" || defaults[ext] || added[ext] || typesEnd < 0 {
			continue
		}
		added[ext] = true
		edits = append(edits, xmlEdit{start: typesEnd, end: typesEnd,
			with: `<Default Extension="` + attrEscaper.Replace(ext) + `" ContentType="` + attrEscaper.Replace(r.contentType) + `"/>`})
	}
	return edits
}

// checkDOCX checks a written DOCX opens: every XML part well-formed, every
// relationship a part uses there and pointing at a part that is, and every
// part with a content type.
func checkDOCX(data []byte) error {
	parts, err := readDOCX(data)
	if err != nil {
		return err
	}
	byName := map[string]*docxPart{}
	for _, p := range parts {
		byName[p.name] = p
		if strings.HasSuffix(strings.ToLower(p.name), ".xml") || strings.HasSuffix(strings.ToLower(p.name), ".rels") {
			if p.nodes, err = scanXML(p.data); err != nil {
				return fmt.Errorf("%s: %w", p.name, err)
			}
		}
	}
	ids := map[string]map[string]bool{} // part -> relationship ids
	for _, p := range parts {
		if p.rule != ruleRels {
			continue
		}
		source := relsSource(p.name)
		ids[source] = map[string]bool{}
		for i := range p.nodes {
			n := &p.nodes[i]
			if n.kind != xmlStart || !is(n.name, nsPR, "Relationship") {
				continue
			}
			id, _ := n.attr(xmlName{local: "Id"})
			ids[source][id] = true
			if mode, _ := n.attr(xmlName{local: "TargetMode"}); mode == "External" {
				continue
			}
			target, _ := n.attr(xmlName{local: "Target"})
			if _, ok := byName[resolveTarget(source, target)]; !ok {
				return fmt.Errorf("%s points to %s, which is not there", p.name, resolveTarget(source, target))
			}
		}
	}
	for _, p := range parts {
		for i := range p.nodes {
			n := &p.nodes[i]
			if n.kind != xmlStart {
				continue
			}
			for _, a := range n.attrs {
				if nsR[a.name.space] && !ids[p.name][a.value] {
					return fmt.Errorf("%s uses relationship %s, which is not there", p.name, a.value)
				}
			}
		}
	}
	types := byName["[Content_Types].xml"]
	if types == nil {
		return errors.New("no [Content_Types].xml")
	}
	defaults, overrides := map[string]bool{}, map[string]bool{}
	for i := range types.nodes {
		n := &types.nodes[i]
		if is(n.name, nsCT, "Default") && n.kind == xmlStart {
			ext, _ := n.attr(xmlName{local: "Extension"})
			defaults[strings.ToLower(ext)] = true
		}
		if is(n.name, nsCT, "Override") && n.kind == xmlStart {
			name, _ := n.attr(xmlName{local: "PartName"})
			overrides[strings.ToLower(strings.TrimPrefix(name, "/"))] = true
		}
	}
	for _, p := range parts {
		ext := strings.TrimPrefix(strings.ToLower(path.Ext(p.name)), ".")
		if p.rule != ruleContentTypes && !defaults[ext] && !overrides[strings.ToLower(p.name)] {
			return fmt.Errorf("%s has no content type", p.name)
		}
	}
	return nil
}

// mediaTypeOf returns the media type for a file extension, for the
// placeholder that replaces a file.
func mediaTypeOf(ext string) string {
	switch strings.ToLower(ext) {
	case ".png":
		return "image/png"
	case ".jpg", ".jpeg":
		return "image/jpeg"
	case ".gif":
		return "image/gif"
	case ".pdf":
		return "application/pdf"
	case ".csv":
		return "text/csv"
	case ".txt":
		return "text/plain"
	case ".html", ".htm":
		return "text/html"
	case ".rtf":
		return "application/rtf"
	case ".docx":
		return "application/vnd.openxmlformats-officedocument.wordprocessingml.document"
	case ".xlsx":
		return "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet"
	}
	return "application/octet-stream"
}
