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
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/martinsuchenak/exemplar/internal/textswap"
)

// DOCX and XLSX files are Office Open XML packages: zips of XML parts tied
// together by relationships and a list of content types. This file reads and
// writes the package; docx.go and xlsx.go say how each format's parts are
// treated. Each part is read by a rule that knows what it can hold. Text is
// rewritten in place (xmlscan.go), images and embedded files are replaced,
// and a part no rule knows is dropped, since it could hold anything. The
// output must open again: every part well-formed, every relationship and
// content type in place.

var (
	nsA  = namespaces("http://schemas.openxmlformats.org/drawingml/2006/main", "http://purl.oclc.org/ooxml/drawingml/main")
	nsR  = namespaces("http://schemas.openxmlformats.org/officeDocument/2006/relationships", "http://purl.oclc.org/ooxml/officeDocument/relationships")
	nsC  = namespaces("http://schemas.openxmlformats.org/drawingml/2006/chart", "http://purl.oclc.org/ooxml/drawingml/chart")
	nsDC = namespaces("http://purl.org/dc/elements/1.1/")
	nsCP = namespaces("http://schemas.openxmlformats.org/package/2006/metadata/core-properties")
	nsEP = namespaces("http://schemas.openxmlformats.org/officeDocument/2006/extended-properties", "http://purl.oclc.org/ooxml/officeDocument/extendedProperties")
	nsVT = namespaces("http://schemas.openxmlformats.org/officeDocument/2006/docPropsVTypes", "http://purl.oclc.org/ooxml/officeDocument/docPropsVTypes")
	nsPR = namespaces("http://schemas.openxmlformats.org/package/2006/relationships")
	nsCT = namespaces("http://schemas.openxmlformats.org/package/2006/content-types")
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
	// ooxmlMaxParts caps the entries of one package.
	ooxmlMaxParts = 2000
	// A package may expand to ooxmlRatio times its size, and to at least
	// ooxmlMinExpand bytes: XML compresses well, a zip bomb far better.
	// However well it compresses, it may never expand past ooxmlMaxExpand
	// bytes in total.
	ooxmlRatio     = 100
	ooxmlMinExpand = 16 << 20
	ooxmlMaxExpand = 256 << 20
)

// ooxmlRule says how a part is read.
type ooxmlRule int

const (
	ruleDrop ooxmlRule = iota
	ruleKeep
	ruleContentTypes
	ruleRels
	ruleCore
	ruleApp
	ruleCustom
	ruleDrawing
	ruleChart
	ruleAllText
	ruleMedia
	ruleEmbedding
	// Word (docx.go)
	ruleBody
	ruleSettings
	rulePeople
	// Excel (xlsx.go)
	ruleWorkbook
	ruleSheet
	ruleSharedStrings
	ruleComments
	ruleThreaded
	rulePersons
	ruleTable
	rulePivotCache
	rulePivotTable
	ruleExternalLink
	ruleConnections
	ruleQueryTable
	ruleCaptions
	ruleVML
)

// partRule gives the rule of the parts whose lower-cased names match re.
type partRule struct {
	re   *regexp.Regexp
	rule ooxmlRule
}

// ooxmlFormat is what tells one Office format from another.
type ooxmlFormat struct {
	// name is how errors name the format ("DOCX"), with its article.
	name, article string
	// main is the part every file of the format has.
	main string
	// legacy is the extension of the binary format it replaced (".doc"),
	// which password-protected files also use.
	legacy string
	// ext is the format's extension, for the hint to save a file as one.
	ext string
	// rules say how each part is treated; a part none matches is dropped.
	rules []partRule
	// office matches the names Office gives the parts it writes. Errors
	// name a part only when it has one of these, is the relationships of
	// one, or is a known drop: any other name the sender chose, and it can
	// say anything.
	office *regexp.Regexp
	// knownDrops are the parts Office writes that exemplar drops (macros,
	// add-ins, ActiveX controls, printer settings…), listed by name. Any
	// other dropped part is listed by a keyed name.
	knownDrops *regexp.Regexp
	// newState, when set, makes what the format's hooks keep while a file
	// is read (xlsx.go's workbook), held in ooxmlPackage.state.
	newState func() any
	// collect finds a part's values.
	collect func(pkg *ooxmlPackage, p *ooxmlPart)
	// finish, when set, runs once every part is collected, for what one
	// part says about another (the column a shared string sits in).
	finish func(pkg *ooxmlPackage)
	// edit, when set, adds edits once every value is rewritten, for what
	// follows from a change elsewhere (a mention whose comment changed).
	edit func(pkg *ooxmlPackage, newText []string, dropped map[string]bool, edits map[*ooxmlPart][]xmlEdit)
}

func (f *ooxmlFormat) ruleFor(name string) ooxmlRule {
	lower := strings.ToLower(name)
	for _, r := range f.rules {
		if r.re.MatchString(lower) {
			return r.rule
		}
	}
	return ruleDrop
}

// ooxmlPart is one entry of the package.
type ooxmlPart struct {
	name  string
	data  []byte
	rule  ooxmlRule
	nodes []xmlNode // for XML parts that are read
}

// ooxmlPackage is a package read for anonymising.
type ooxmlPackage struct {
	format *ooxmlFormat
	parts  []*ooxmlPart
	byName map[string]*ooxmlPart
	units  []ooxmlUnit
	// state is the format's own, from newState.
	state any
}

// ooxmlUnit is one value to search and rewrite: a run of text across one or
// more text elements, or an attribute value.
type ooxmlUnit struct {
	part   *ooxmlPart
	pieces []ooxmlPiece
	// entity, when set, is what the whole value is (an author is a person).
	entity string
	// fake, when set, makes the new value directly instead of detection.
	fake func(ctx context.Context, e *Engine, v string) (string, error)
	// rewrite, when set, makes the new value from the replacer itself, for
	// a value with syntax of its own (a formula keeps its references, a
	// sheet name Excel's rules). The unit is one piece.
	rewrite func(rep *replacer, v string) string
	// search, when set, is the part of the value detection reads (a
	// formula's strings, not its syntax); "" reads none of it.
	search func(v string) string
	// initialsOf is the unit holding the author these are the initials
	// of, or -1.
	initialsOf int
	// columns has the table column of each cell the value is in (see
	// docText.column): a shared string used in three cells has three.
	columns []string
}

// ooxmlPiece is one text node or attribute value of a unit.
type ooxmlPiece struct {
	start, end int
	value      string
	attr       bool
}

func (u *ooxmlUnit) text() string {
	var b strings.Builder
	for _, p := range u.pieces {
		b.WriteString(p.value)
	}
	return b.String()
}

// ooxmlHandler reads and rewrites one Office format.
type ooxmlHandler struct {
	format *ooxmlFormat
}

func (h ooxmlHandler) prepare(ctx context.Context, e *Engine, file File) error {
	pkg, err := h.format.open(file.Data)
	if err != nil {
		return err
	}
	return e.learnNames(ctx, pkg.texts())
}

func (h ooxmlHandler) anonymise(ctx context.Context, e *Engine, file File) (*Output, error) {
	pkg, err := h.format.open(file.Data)
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
	if err := h.format.check(out.Data); err != nil {
		return nil, fmt.Errorf("anonymised output is not a valid %s: %w", h.format.name, err)
	}
	return out, nil
}

// read reads every entry of a package, within the limits.
func (f *ooxmlFormat) read(data []byte) ([]*ooxmlPart, error) {
	if bytes.HasPrefix(data, []byte{0xd0, 0xcf, 0x11, 0xe0}) {
		return nil, fmt.Errorf("not %s %s: this looks like a password-protected or old %s file; save it as an unprotected %s first", f.article, f.name, f.legacy, f.ext)
	}
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return nil, fmt.Errorf("not %s %s: not a zip archive", f.article, f.name)
	}
	if len(zr.File) > ooxmlMaxParts {
		return nil, fmt.Errorf("the %s has over %d parts", f.name, ooxmlMaxParts)
	}
	expandable := int64(max(len(data)*ooxmlRatio, ooxmlMinExpand))
	budget := min(expandable, int64(ooxmlMaxExpand))
	var parts []*ooxmlPart
	seen := map[string]bool{}
	for _, zf := range zr.File {
		if strings.HasSuffix(zf.Name, "/") {
			continue
		}
		if zf.Name == "" || strings.HasPrefix(zf.Name, "/") || strings.Contains(zf.Name, `\`) || path.Clean(zf.Name) != zf.Name {
			return nil, fmt.Errorf("the %s has a part with an unsafe name", f.name)
		}
		if seen[strings.ToLower(zf.Name)] {
			return nil, fmt.Errorf("the %s holds %s twice", f.name, f.partLabel(zf.Name))
		}
		seen[strings.ToLower(zf.Name)] = true
		rc, err := zf.Open()
		if err != nil {
			return nil, fmt.Errorf("reading %s: %w", f.partLabel(zf.Name), err)
		}
		b, err := io.ReadAll(io.LimitReader(rc, budget+1))
		_ = rc.Close()
		if err != nil {
			return nil, fmt.Errorf("reading %s: %w", f.partLabel(zf.Name), err)
		}
		if budget -= int64(len(b)); budget < 0 {
			if expandable > int64(ooxmlMaxExpand) {
				return nil, fmt.Errorf("the %s expands to more than %d MiB in total", f.name, ooxmlMaxExpand>>20)
			}
			return nil, fmt.Errorf("the %s expands to more than %d times its size", f.name, ooxmlRatio)
		}
		parts = append(parts, &ooxmlPart{name: zf.Name, data: b, rule: f.ruleFor(zf.Name)})
	}
	if !seen[f.main] || !seen["[content_types].xml"] {
		return nil, fmt.Errorf("not %s %s: it has no %s", f.article, f.name, f.main)
	}
	return parts, nil
}

// open reads a package and finds every value to search.
func (f *ooxmlFormat) open(data []byte) (*ooxmlPackage, error) {
	parts, err := f.read(data)
	if err != nil {
		return nil, err
	}
	pkg := &ooxmlPackage{format: f, parts: parts, byName: map[string]*ooxmlPart{}}
	if f.newState != nil {
		pkg.state = f.newState()
	}
	nodes := 0
	for _, p := range parts {
		pkg.byName[p.name] = p
		switch p.rule {
		case ruleKeep, ruleMedia, ruleEmbedding, ruleDrop:
			continue
		}
		if p.nodes, err = scanXML(p.data); err != nil {
			return nil, fmt.Errorf("%s: %w", f.partLabel(p.name), err)
		}
		if nodes += len(p.nodes); nodes > xmlMaxNodes {
			return nil, fmt.Errorf("the %s holds more than %d XML tokens in total", f.name, xmlMaxNodes)
		}
		f.collect(pkg, p)
	}
	if f.finish != nil {
		f.finish(pkg)
	}
	return pkg, nil
}

// texts returns the values to detect in; directly faked values are not
// searched.
func (pkg *ooxmlPackage) texts() []docText {
	var out []docText
	for i := range pkg.units {
		u := &pkg.units[i]
		if u.fake != nil || u.initialsOf >= 0 {
			continue
		}
		v := u.text()
		if u.search != nil {
			v = u.search(v)
		}
		if strings.TrimSpace(v) != "" {
			out = append(out, docText{value: v, entity: u.entity})
		}
		for _, c := range u.columns {
			out = append(out, docText{value: v, column: c})
		}
	}
	return out
}

func (pkg *ooxmlPackage) add(u ooxmlUnit) int {
	pkg.units = append(pkg.units, u)
	return len(pkg.units) - 1
}

func (pkg *ooxmlPackage) addText(p *ooxmlPart, n *xmlNode, entity string) int {
	return pkg.add(ooxmlUnit{part: p, entity: entity, initialsOf: -1, pieces: []ooxmlPiece{{start: n.start, end: n.end, value: n.text}}})
}

func (pkg *ooxmlPackage) addAttr(p *ooxmlPart, a *xmlAttr, entity string) int {
	return pkg.add(ooxmlUnit{part: p, entity: entity, initialsOf: -1, pieces: []ooxmlPiece{{start: a.start, end: a.end, value: a.value, attr: true}}})
}

func (pkg *ooxmlPackage) addFake(p *ooxmlPart, a *xmlAttr, fake func(context.Context, *Engine, string) (string, error)) {
	pkg.add(ooxmlUnit{part: p, fake: fake, initialsOf: -1, pieces: []ooxmlPiece{{start: a.start, end: a.end, value: a.value, attr: true}}})
}

// fileExt returns a file name's lower-cased extension, or "" when it has
// none or what follows the dot is not an extension.
func fileExt(v string) string {
	ext := strings.ToLower(path.Ext(strings.ReplaceAll(v, `\`, "/")))
	if len(ext) < 2 || len(ext) > 6 || strings.ContainsAny(ext, " ?#&=,") {
		return ""
	}
	return ext
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
	return "file:///anonymised/" + e.key.Hex("path", v, 10) + fileExt(v), nil
}

// fakeOpaque replaces a value whole with a keyed token.
func fakeOpaque(_ context.Context, e *Engine, v string) (string, error) {
	if strings.TrimSpace(v) == "" {
		return v, nil
	}
	return "anonymised-" + e.key.Hex("opaque", v, 12), nil
}

var hexRunRe = regexp.MustCompile(`[0-9A-Fa-f]{8,}`)

// fakeUserID replaces a reviewer's account id (Word's people.xml, Excel's
// persons): the addresses in it, and its hex runs (GUIDs, Live IDs) by keyed
// ones of the same length. One that is neither is replaced whole.
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
func distribute(pieces []string, matches []textswap.Match) []string {
	offs := make([]int, len(pieces)+1)
	for i, p := range pieces {
		offs[i+1] = offs[i] + len(p)
	}
	s := strings.Join(pieces, "")
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

// ooxmlRename is a binary part given a new name, and with it, for an image
// whose format changed, a new content type.
type ooxmlRename struct {
	to          string
	contentType string // empty: the part keeps its type
}

// rewrite returns the anonymised package and counts what it replaced and
// dropped in out.
func (pkg *ooxmlPackage) rewrite(ctx context.Context, e *Engine, rep *replacer, out *Output) ([]byte, error) {
	f := pkg.format
	edits := map[*ooxmlPart][]xmlEdit{}
	newText := make([]string, len(pkg.units))
	for i := range pkg.units {
		u := &pkg.units[i]
		old := u.text()
		var values []string
		switch {
		case u.initialsOf >= 0:
			if newText[u.initialsOf] == pkg.units[u.initialsOf].text() {
				newText[i] = old
				continue // the author did not change
			}
			values = []string{initials(newText[u.initialsOf])}
		case u.fake != nil:
			v, err := u.fake(ctx, e, old)
			if err != nil {
				return nil, err
			}
			values = []string{v}
		case u.rewrite != nil:
			values = []string{u.rewrite(rep, old)}
		default:
			pieces := make([]string, len(u.pieces))
			for j, p := range u.pieces {
				pieces[j] = p.value
			}
			values = distribute(pieces, rep.Matches(old))
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
	renames := map[string]ooxmlRename{}
	dropped := map[string]bool{}
	for _, p := range pkg.parts {
		switch p.rule {
		case ruleDrop:
			dropped[p.name] = true
		case ruleMedia:
			out.Attachments++
			ext := strings.ToLower(path.Ext(p.name))
			// Office names images image1.png; a name another program kept
			// from the file ("Jane Doe passport.jpg") is replaced.
			base := strings.TrimSuffix(p.name, path.Ext(p.name))
			if !ooxmlMediaName.MatchString(strings.ToLower(path.Base(base))) {
				base = path.Dir(p.name) + "/image-" + e.key.Hex("file", path.Base(p.name), 6)
			}
			switch ext {
			case ".png", ".jpg", ".jpeg", ".gif", ".svg":
				to := p.name
				if base != strings.TrimSuffix(p.name, path.Ext(p.name)) {
					to = pkg.freeName(base, ext, renames)
					renames[p.name] = ooxmlRename{to: to}
				}
				if ext == ".svg" {
					p.data = []byte(`<svg xmlns="http://www.w3.org/2000/svg" width="64" height="64"><rect width="64" height="64" fill="#d0d0d0"/></svg>`)
				} else {
					p.data = e.placeholder(mediaTypeOf(ext), path.Base(to), len(p.data), people)
				}
			default:
				// EMF, WMF, BMP, TIFF…: a PNG in its place.
				to := pkg.freeName(base, ".png", renames)
				renames[p.name] = ooxmlRename{to: to, contentType: "image/png"}
				p.data = e.placeholder("image/png", path.Base(to), len(p.data), people)
			}
		case ruleEmbedding:
			out.Attachments++
			ext := strings.ToLower(path.Ext(p.name))
			to := pkg.freeName(path.Dir(p.name)+"/embedding-"+e.key.Hex("file", path.Base(p.name), 6), ext, renames)
			renames[p.name] = ooxmlRename{to: to}
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
	if f.edit != nil {
		f.edit(pkg, newText, dropped, edits)
	}

	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	write := func(p *ooxmlPart) error {
		data := p.data
		if len(edits[p]) > 0 {
			var err error
			if data, err = splice(p.data, edits[p]); err != nil {
				return fmt.Errorf("%s: %w", f.partLabel(p.name), err)
			}
		}
		name := p.name
		if r, ok := renames[name]; ok {
			name = r.to
		}
		w, err := zw.CreateHeader(&zip.FileHeader{Name: name, Method: zip.Deflate, Modified: ooxmlTime})
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
			out.DroppedParts = append(out.DroppedParts, f.droppedPartName(e, p.name))
		}
	}
	return buf.Bytes(), nil
}

// ooxmlMediaName matches the names Office gives images and the thumbnail.
var ooxmlMediaName = regexp.MustCompile(`^(image\d*|thumbnail)$`)

// partLabel is how an error names a part: by its name when Office chose it,
// otherwise without it.
func (f *ooxmlFormat) partLabel(name string) string {
	l := strings.ToLower(name)
	if l == "_rels/.rels" || f.knownDrops.MatchString(l) {
		return name
	}
	if path.Base(path.Dir(l)) == "_rels" && strings.HasSuffix(l, ".rels") {
		l = relsSource(l)
	}
	if f.office.MatchString(l) {
		return name
	}
	return "a part named by the sender"
}

// droppedPartName is how a dropped part is listed.
func (f *ooxmlFormat) droppedPartName(e *Engine, name string) string {
	if f.knownDrops.MatchString(strings.ToLower(name)) {
		return name
	}
	return "part-" + e.key.Hex("part", name, 6) + fileExt(name)
}

// ooxmlTime is every entry's time, so the same input gives the same bytes.
var ooxmlTime = time.Date(1980, 1, 1, 0, 0, 0, 0, time.UTC)

// freeName returns base+ext, or base-2+ext… when that name is taken.
func (pkg *ooxmlPackage) freeName(base, ext string, renames map[string]ooxmlRename) string {
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
func relsEdits(p *ooxmlPart, renames map[string]ooxmlRename, dropped map[string]bool) []xmlEdit {
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

// relTargets returns the parts a part's relationships point to, by
// relationship id; external targets are left out.
func (pkg *ooxmlPackage) relTargets(part string) map[string]string {
	rels := pkg.byName[relsFor(part)]
	if rels == nil {
		return nil
	}
	out := map[string]string{}
	for i := range rels.nodes {
		n := &rels.nodes[i]
		if n.kind != xmlStart || !is(n.name, nsPR, "Relationship") {
			continue
		}
		if mode, _ := n.attr(xmlName{local: "TargetMode"}); mode == "External" {
			continue
		}
		id, _ := n.attr(xmlName{local: "Id"})
		target, _ := n.attr(xmlName{local: "Target"})
		out[id] = resolveTarget(part, target)
	}
	return out
}

// contentTypeEdits removes the overrides of dropped parts, moves those of
// renamed ones, and adds a default for an extension that is new.
func contentTypeEdits(p *ooxmlPart, renames map[string]ooxmlRename, dropped map[string]bool) []xmlEdit {
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

// check checks a written package opens: every XML part well-formed, every
// relationship a part uses there and pointing at a part that is, and every
// part with a content type.
func (f *ooxmlFormat) check(data []byte) error {
	parts, err := f.read(data)
	if err != nil {
		return err
	}
	byName := map[string]*ooxmlPart{}
	nodes := 0
	for _, p := range parts {
		byName[p.name] = p
		lower := strings.ToLower(p.name)
		if strings.HasSuffix(lower, ".xml") || strings.HasSuffix(lower, ".rels") || strings.HasSuffix(lower, ".vml") {
			if p.nodes, err = scanXML(p.data); err != nil {
				return fmt.Errorf("%s: %w", f.partLabel(p.name), err)
			}
			if nodes += len(p.nodes); nodes > xmlMaxNodes {
				return fmt.Errorf("the %s holds more than %d XML tokens in total", f.name, xmlMaxNodes)
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
				return fmt.Errorf("%s points to a part that is not there", f.partLabel(p.name))
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
					return fmt.Errorf("%s uses a relationship that is not there", f.partLabel(p.name))
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
			return fmt.Errorf("%s has no content type", f.partLabel(p.name))
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
