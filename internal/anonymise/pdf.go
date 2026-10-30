package anonymise

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"maps"
	"math"
	"slices"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/klippa-app/go-pdfium/enums"
	pdferrors "github.com/klippa-app/go-pdfium/errors"
	"github.com/klippa-app/go-pdfium/references"
	"github.com/klippa-app/go-pdfium/requests"
	"github.com/klippa-app/go-pdfium/structs"
)

// A PDF stores no words: its pages are drawing instructions that place
// characters, often in fonts holding only the letters the file uses. So a
// PDF is rewritten in place rather than as text (see docs/anonymisation.md,
// "PDF"):
//
//  1. PDFium (pdfium.go) opens it, refusing one that needs a password, and
//     flattens every page, so annotations and form fields become page
//     content. Flattening removes links too, so each is read first, as
//     where it sits and where it goes, and added back at the end. A page
//     that is mostly image with little or invisible text is a scan, and
//     refused.
//  2. Each page's text is read with the box of every character, and every
//     text and image object with its bounds, inside forms too. A character
//     belongs to the smallest text object holding the centre of its box.
//  3. Detection reads the pages' text, the strings of marked content, and
//     every string stored outside the drawing (metadata, bookmarks, link
//     targets: pdfcpu.go).
//  4. A text object holding a match is replaced by one with the fakes in
//     it, at the same place and size, in the standard font nearest its own
//     (fakes need letters a subset font may lack), squeezed to its width if
//     longer. Every image is replaced by a plain grey one (inline images
//     here; image objects, drawn or not, by pdfcpu too).
//  5. pdfcpu rewrites the strings outside the drawing and drops what no
//     rule knows (pdfcpu.go), and the result is opened again to check that
//     its text is what was written. PDFium writes it once more, as pdfcpu
//     lays out objects in no fixed order, so the same file anonymised with
//     the same key comes out the same.

const (
	pdfMaxPages   = 2000
	pdfMaxChars   = 4 << 20 // characters across the document
	pdfMaxObjects = 1 << 20 // page objects across the document, forms included
	pdfMaxDepth   = 16      // forms inside forms
	// A page whose images cover pdfScanArea of it is a scan when it has
	// fewer than pdfScanText characters of visible text, or any invisible
	// text (the layer OCR adds over a scanned image).
	pdfScanArea = 0.5
	pdfScanText = 50
)

// pdfHandler reads and rewrites PDFs.
type pdfHandler struct{}

func (pdfHandler) prepare(ctx context.Context, e *Engine, file File) error {
	ctx, cancel := context.WithTimeout(ctx, pdfTimeout)
	defer cancel()
	d, err := readPDF(ctx, file.Data)
	if err != nil {
		return err
	}
	defer d.s.close()
	return e.learnNames(ctx, d.texts())
}

func (pdfHandler) anonymise(ctx context.Context, e *Engine, file File) (*Output, error) {
	// One deadline for the file, however many engines its attempts start.
	ctx, cancel := context.WithTimeout(ctx, pdfTimeout)
	defer cancel()
	d, err := readPDF(ctx, file.Data)
	if err != nil {
		return nil, err
	}
	defer func() { d.s.close() }()
	pairs, f, rep, err := e.docReplacements(ctx, d.texts())
	if err != nil {
		return nil, err
	}
	// PDFium writes some fonts' text back wrong when it writes a page again
	// (the codes of a symbolic subset, re-derived from Unicode): checkPDF
	// finds the objects, and the file is written again with them drawn
	// in a standard font too.
	redraw := pdfRedraw{}
	for attempt := 1; ; attempt++ {
		out := &Output{Replacements: pairs, Entities: f.entities}
		redo, err := d.rewrite(ctx, rep, redraw, out)
		if err != nil {
			return nil, err
		}
		if len(redo) == 0 {
			return out, nil
		}
		if attempt == 3 {
			return nil, errors.New("PDFium cannot write some of the PDF's text back as it was")
		}
		for page, objs := range redo {
			if redraw[page] == nil {
				redraw[page] = map[int]bool{}
			}
			for k := range objs {
				redraw[page][k] = true
			}
		}
		d.s.close()
		if d, err = readPDF(ctx, file.Data); err != nil {
			return nil, err
		}
	}
}

// pdfRedraw is, by page, the text objects to draw again in a standard font
// though they hold no match, by index in the page's objs.
type pdfRedraw map[int]map[int]bool

// rewrite writes the anonymised PDF into out, and returns the objects
// PDFium wrote back wrong, if any, in which case out is not to be used.
func (d *pdfDoc) rewrite(ctx context.Context, rep *replacer, redraw pdfRedraw, out *Output) (pdfRedraw, error) {
	plans := make([]pdfPlan, len(d.pages))
	var err error
	for i := range d.pages {
		if plans[i], err = d.pages[i].plan(rep, redraw[i]); err != nil {
			return nil, fmt.Errorf("page %d: %w", i+1, err)
		}
	}
	for i := range d.pages {
		if err := d.rewritePage(i, plans[i]); err != nil {
			return nil, fmt.Errorf("page %d: %w", i+1, err)
		}
	}
	written, err := d.save()
	if err != nil {
		return nil, err
	}
	links := make([][]pdfLink, len(d.pages))
	for i, page := range d.pages {
		links[i] = page.links
	}
	finished, err := pdfFinish(d.s.ctx, written, rep, links, out)
	if err != nil {
		return nil, err
	}
	d.s.close() // one engine at a time
	var redo pdfRedraw
	out.Data, redo, err = checkPDF(ctx, finished, d.pages, plans)
	return redo, err
}

// pdfDoc is a PDF open in PDFium, read.
type pdfDoc struct {
	s       *pdfSession
	doc     references.FPDF_DOCUMENT
	secured bool // encrypted, though it needs no password to open
	pages   []pdfPage
	// strings are the strings stored outside the drawing, read by pdfcpu
	// from PDFium's copy of the file.
	strings []pdfString
}

// pdfPage is what a page holds that can name someone.
type pdfPage struct {
	// text is the page's text in PDFium's reading order, with the spaces
	// and line breaks PDFium adds between runs (which no object owns).
	text []rune
	// owner is, for each character, the index in objs of the text object
	// drawing it plus one, or 0.
	owner []int
	// boxes are the characters' boxes, in page space (empty for those
	// PDFium adds).
	boxes []pdfRect
	objs  []pdfObj
	marks []pdfMark
	links []pdfLink
	// inline says the page draws an image inline, in its content.
	inline bool
}

// pdfLink is a link on a page, as plain values: where it sits, and the web
// address or the page (with the point on it) it goes to.
type pdfLink struct {
	rect pdfRect
	uri  string
	page int // -1 for a web address
	x, y *float32
}

// pdfObj is a text or image object: by its path, the index on the page and
// then in each form it sits in.
type pdfObj struct {
	path   []int
	image  bool
	text   string                 // a text object's own text
	box    pdfRect                // in page space
	matrix structs.FPDF_FS_MATRIX // in page space
	width  float64                // a text object's length along its baseline, in page space
	mode   enums.FPDF_TEXT_RENDERMODE
}

// pdfMark is a string in the marked content of an object: the text a
// screen reader says for it (ActualText, Alt), or its expansion (E).
type pdfMark struct {
	path       []int
	mark       int
	key, value string
}

type pdfRect struct{ left, bottom, right, top float64 }

func (r pdfRect) area() float64 { return math.Max(0, r.right-r.left) * math.Max(0, r.top-r.bottom) }

func (r pdfRect) holds(x, y float64) bool {
	return x >= r.left && x <= r.right && y >= r.bottom && y <= r.top
}

// readPDF opens a PDF, flattens it and reads every page.
func readPDF(ctx context.Context, data []byte) (*pdfDoc, error) {
	s, err := openPDFium(ctx)
	if err != nil {
		return nil, err
	}
	d := &pdfDoc{s: s}
	if err := d.read(data); err != nil {
		s.close()
		return nil, err
	}
	return d, nil
}

func (d *pdfDoc) read(data []byte) error {
	p := d.s.p
	doc, err := p.FPDF_LoadMemDocument64(&requests.FPDF_LoadMemDocument64{Data: &data})
	if err != nil {
		switch {
		case errors.Is(err, pdferrors.ErrPassword):
			return errors.New("the PDF is password-protected")
		case errors.Is(err, pdferrors.ErrFormat), errors.Is(err, pdferrors.ErrFile):
			return errors.New("not a PDF, or a damaged one")
		case errors.Is(err, pdferrors.ErrSecurity):
			return errors.New("the PDF is encrypted in a way PDFium cannot read")
		}
		return fmt.Errorf("not a PDF PDFium can read: %w", d.s.fail(err))
	}
	d.doc = doc.Document
	if rev, err := p.FPDF_GetSecurityHandlerRevision(&requests.FPDF_GetSecurityHandlerRevision{Document: d.doc}); err == nil {
		d.secured = rev.SecurityHandlerRevision != -1
	}
	count, err := p.FPDF_GetPageCount(&requests.FPDF_GetPageCount{Document: d.doc})
	if err != nil {
		return d.s.fail(err)
	}
	if count.PageCount > pdfMaxPages {
		return fmt.Errorf("the PDF has %d pages; at most %d are read", count.PageCount, pdfMaxPages)
	}
	// Annotations and form fields become page content, so their text is
	// the page's; a page is read again after it is flattened.
	links := make([][]pdfLink, count.PageCount)
	for i := 0; i < count.PageCount; i++ {
		if err := d.withPage(i, func(page requests.Page) error {
			var err error
			if links[i], err = d.readLinks(page); err != nil {
				return err
			}
			_, err = p.FPDFPage_Flatten(&requests.FPDFPage_Flatten{Page: page, Usage: requests.FPDFPage_FlattenUsageNormalDisplay})
			return err
		}); err != nil {
			return fmt.Errorf("page %d: %w", i+1, d.s.fail(err))
		}
	}
	chars, objects := 0, 0
	for i := 0; i < count.PageCount; i++ {
		page, err := d.readPage(i)
		if err != nil {
			return fmt.Errorf("page %d: %w", i+1, d.s.fail(err))
		}
		page.links = links[i]
		chars, objects = chars+len(page.text), objects+len(page.objs)+len(page.marks)+len(page.links)
		if chars > pdfMaxChars || objects > pdfMaxObjects {
			return fmt.Errorf("the PDF holds more than %d characters or %d objects", pdfMaxChars, pdfMaxObjects)
		}
		d.pages = append(d.pages, page)
	}
	copied, err := d.save()
	if err != nil {
		return err
	}
	var inline []bool
	if d.strings, inline, err = pdfStrings(d.s.ctx, copied); err != nil {
		return err
	}
	for i := range d.pages {
		d.pages[i].inline = i < len(inline) && inline[i]
	}
	return nil
}

// readLinks returns a page's links that go to a page of the document or to
// a web address. Others (to files, to programs, to scripts) are dropped.
func (d *pdfDoc) readLinks(page requests.Page) ([]pdfLink, error) {
	var out []pdfLink
	for pos := 0; len(out) < pdfMaxObjects; {
		l, err := d.s.p.FPDFLink_Enumerate(&requests.FPDFLink_Enumerate{Page: page, StartPos: pos})
		if err != nil || l.Link == nil {
			break
		}
		if link, ok := d.readLink(*l.Link); ok {
			out = append(out, link)
		}
		if l.NextStartPos == nil || *l.NextStartPos <= pos {
			break
		}
		pos = *l.NextStartPos
	}
	return out, nil
}

// readLink returns where a link sits and where it goes, or false for one
// that goes to neither a page nor a web address.
func (d *pdfDoc) readLink(l references.FPDF_LINK) (pdfLink, bool) {
	p := d.s.p
	r, err := p.FPDFLink_GetAnnotRect(&requests.FPDFLink_GetAnnotRect{Link: l})
	if err != nil || r.Rect == nil {
		return pdfLink{}, false
	}
	link := pdfLink{rect: pdfRect{float64(r.Rect.Left), float64(r.Rect.Bottom), float64(r.Rect.Right), float64(r.Rect.Top)}, page: -1}
	// A link goes to a destination of its own, or through its action.
	var dest *references.FPDF_DEST
	if g, err := p.FPDFLink_GetDest(&requests.FPDFLink_GetDest{Document: d.doc, Link: l}); err == nil && g.Dest != nil {
		dest = g.Dest
	} else if a, err := p.FPDFLink_GetAction(&requests.FPDFLink_GetAction{Link: l}); err == nil && a.Action != nil {
		ty, err := p.FPDFAction_GetType(&requests.FPDFAction_GetType{Action: *a.Action})
		switch {
		case err != nil:
		case ty.Type == enums.FPDF_ACTION_ACTION_URI:
			if u, err := p.FPDFAction_GetURIPath(&requests.FPDFAction_GetURIPath{Document: d.doc, Action: *a.Action}); err == nil && u.URIPath != nil {
				link.uri = *u.URIPath
			}
		case ty.Type == enums.FPDF_ACTION_ACTION_GOTO:
			if ad, err := p.FPDFAction_GetDest(&requests.FPDFAction_GetDest{Document: d.doc, Action: *a.Action}); err == nil {
				dest = ad.Dest
			}
		}
	}
	if dest != nil {
		if pi, err := p.FPDFDest_GetDestPageIndex(&requests.FPDFDest_GetDestPageIndex{Document: d.doc, Dest: *dest}); err == nil && pi.Index >= 0 {
			link.page = pi.Index
			if at, err := p.FPDFDest_GetLocationInPage(&requests.FPDFDest_GetLocationInPage{Dest: *dest}); err == nil {
				link.x, link.y = at.X, at.Y
			}
		}
	}
	return link, link.uri != "" || link.page >= 0
}

// withPage loads page i for fn and closes it after.
func (d *pdfDoc) withPage(i int, fn func(requests.Page) error) error {
	pg, err := d.s.p.FPDF_LoadPage(&requests.FPDF_LoadPage{Document: d.doc, Index: i})
	if err != nil {
		return err
	}
	defer func() { _, _ = d.s.p.FPDF_ClosePage(&requests.FPDF_ClosePage{Page: pg.Page}) }()
	return fn(requests.Page{ByReference: &pg.Page})
}

func (d *pdfDoc) readPage(i int) (pdfPage, error) {
	var page pdfPage
	err := d.withPage(i, func(ref requests.Page) error {
		p := d.s.p
		tp, err := p.FPDFText_LoadPage(&requests.FPDFText_LoadPage{Page: ref})
		if err != nil {
			return err
		}
		defer func() { _, _ = p.FPDFText_ClosePage(&requests.FPDFText_ClosePage{TextPage: tp.TextPage}) }()
		n, err := p.FPDFText_CountChars(&requests.FPDFText_CountChars{TextPage: tp.TextPage})
		if err != nil {
			return err
		}
		if n.Count > pdfMaxChars {
			return fmt.Errorf("more than %d characters", pdfMaxChars)
		}
		page.text = make([]rune, n.Count)
		boxes := make([]pdfRect, n.Count)
		for c := range page.text {
			u, err := p.FPDFText_GetUnicode(&requests.FPDFText_GetUnicode{TextPage: tp.TextPage, Index: c})
			if err != nil {
				return err
			}
			r := rune(u.Unicode)
			if r == 0 || !utf8.ValidRune(r) {
				r = utf8.RuneError
			}
			page.text[c] = r
			if b, err := p.FPDFText_GetCharBox(&requests.FPDFText_GetCharBox{TextPage: tp.TextPage, Index: c}); err == nil {
				boxes[c] = pdfRect{b.Left, b.Bottom, b.Right, b.Top}
			}
		}
		identity := structs.FPDF_FS_MATRIX{A: 1, D: 1}
		if err := d.walk(ref, tp.TextPage, "", nil, identity, &page, 0); err != nil {
			return err
		}
		page.own(boxes)
		page.boxes = boxes
		return d.checkScan(ref, &page)
	})
	return page, err
}

// walk reads the objects on a page, or in the form object form, whose
// space maps to the page's by outer.
func (d *pdfDoc) walk(page requests.Page, tp references.FPDF_TEXTPAGE, form references.FPDF_PAGEOBJECT, path []int, outer structs.FPDF_FS_MATRIX, out *pdfPage, depth int) error {
	if depth > pdfMaxDepth {
		return fmt.Errorf("forms nested more than %d deep", pdfMaxDepth)
	}
	p := d.s.p
	count, err := d.countObjects(page, form)
	if err != nil {
		return err
	}
	for i := 0; i < count; i++ {
		obj, err := d.object(page, form, i)
		if err != nil {
			return err
		}
		at := append(slices.Clone(path), i)
		if err := d.readMarks(obj, at, out); err != nil {
			return err
		}
		ty, err := p.FPDFPageObj_GetType(&requests.FPDFPageObj_GetType{PageObject: obj})
		if err != nil {
			return err
		}
		switch ty.Type {
		case enums.FPDF_PAGEOBJ_TEXT, enums.FPDF_PAGEOBJ_IMAGE:
			o := pdfObj{path: at, image: ty.Type == enums.FPDF_PAGEOBJ_IMAGE}
			b, err := p.FPDFPageObj_GetBounds(&requests.FPDFPageObj_GetBounds{PageObject: obj})
			if err != nil {
				return err
			}
			o.box = transformRect(pdfRect{float64(b.Left), float64(b.Bottom), float64(b.Right), float64(b.Top)}, outer)
			m, err := p.FPDFPageObj_GetMatrix(&requests.FPDFPageObj_GetMatrix{PageObject: obj})
			if err != nil {
				return err
			}
			o.matrix = mulMatrix(m.Matrix, outer)
			if !o.image {
				t, err := p.FPDFTextObj_GetText(&requests.FPDFTextObj_GetText{PageObject: obj, TextPage: tp})
				if err != nil {
					return err
				}
				o.text = t.Text
				mode, err := p.FPDFTextObj_GetTextRenderMode(&requests.FPDFTextObj_GetTextRenderMode{PageObject: obj})
				if err != nil {
					return err
				}
				o.mode = mode.TextRenderMode
				q, err := p.FPDFPageObj_GetRotatedBounds(&requests.FPDFPageObj_GetRotatedBounds{PageObject: obj})
				if err != nil {
					return err
				}
				o.width = baseline(q.QuadPoints, outer)
			}
			out.objs = append(out.objs, o)
		case enums.FPDF_PAGEOBJ_FORM:
			m, err := p.FPDFPageObj_GetMatrix(&requests.FPDFPageObj_GetMatrix{PageObject: obj})
			if err != nil {
				return err
			}
			if err := d.walk(page, tp, obj, at, mulMatrix(m.Matrix, outer), out, depth+1); err != nil {
				return err
			}
		}
	}
	return nil
}

func (d *pdfDoc) countObjects(page requests.Page, form references.FPDF_PAGEOBJECT) (int, error) {
	if form == "" {
		n, err := d.s.p.FPDFPage_CountObjects(&requests.FPDFPage_CountObjects{Page: page})
		if err != nil {
			return 0, err
		}
		return n.Count, nil
	}
	n, err := d.s.p.FPDFFormObj_CountObjects(&requests.FPDFFormObj_CountObjects{PageObject: form})
	if err != nil {
		return 0, err
	}
	return n.Count, nil
}

func (d *pdfDoc) object(page requests.Page, form references.FPDF_PAGEOBJECT, i int) (references.FPDF_PAGEOBJECT, error) {
	if form == "" {
		o, err := d.s.p.FPDFPage_GetObject(&requests.FPDFPage_GetObject{Page: page, Index: i})
		if err != nil {
			return "", err
		}
		return o.PageObject, nil
	}
	o, err := d.s.p.FPDFFormObj_GetObject(&requests.FPDFFormObj_GetObject{PageObject: form, Index: uint64(i)})
	if err != nil {
		return "", err
	}
	return o.PageObject, nil
}

// find returns the object at path, and the form holding it ("" on the
// page itself).
func (d *pdfDoc) find(page requests.Page, path []int) (obj, form references.FPDF_PAGEOBJECT, err error) {
	for _, i := range path {
		form = obj
		if obj, err = d.object(page, form, i); err != nil {
			return "", "", err
		}
	}
	return obj, form, nil
}

func (d *pdfDoc) readMarks(obj references.FPDF_PAGEOBJECT, path []int, out *pdfPage) error {
	p := d.s.p
	n, err := p.FPDFPageObj_CountMarks(&requests.FPDFPageObj_CountMarks{PageObject: obj})
	if err != nil {
		return err
	}
	for m := 0; m < n.Count; m++ {
		mark, err := p.FPDFPageObj_GetMark(&requests.FPDFPageObj_GetMark{PageObject: obj, Index: uint64(m)})
		if err != nil {
			return err
		}
		params, err := p.FPDFPageObjMark_CountParams(&requests.FPDFPageObjMark_CountParams{PageObjectMark: mark.Mark})
		if err != nil {
			return err
		}
		for k := 0; k < params.Count; k++ {
			key, err := p.FPDFPageObjMark_GetParamKey(&requests.FPDFPageObjMark_GetParamKey{PageObjectMark: mark.Mark, Index: uint64(k)})
			if err != nil {
				return err
			}
			ty, err := p.FPDFPageObjMark_GetParamValueType(&requests.FPDFPageObjMark_GetParamValueType{PageObjectMark: mark.Mark, Key: key.Key})
			if err != nil || ty.ValueType != enums.FPDF_OBJECT_TYPE_STRING {
				continue
			}
			v, err := p.FPDFPageObjMark_GetParamStringValue(&requests.FPDFPageObjMark_GetParamStringValue{PageObjectMark: mark.Mark, Key: key.Key})
			if err != nil {
				return err
			}
			out.marks = append(out.marks, pdfMark{path: path, mark: m, key: key.Key, value: v.Value})
		}
	}
	return nil
}

// own gives each character the smallest text object holding the centre of
// its box. Objects are bucketed in a grid so a page of many objects is not
// searched whole for every character.
func (page *pdfPage) own(boxes []pdfRect) {
	page.owner = make([]int, len(page.text))
	const cells = 32
	var all pdfRect
	first := true
	for _, o := range page.objs {
		if o.image {
			continue
		}
		if first {
			all, first = o.box, false
		}
		all = pdfRect{math.Min(all.left, o.box.left), math.Min(all.bottom, o.box.bottom), math.Max(all.right, o.box.right), math.Max(all.top, o.box.top)}
	}
	if first {
		return
	}
	w, h := math.Max(all.right-all.left, 1e-6), math.Max(all.top-all.bottom, 1e-6)
	cell := func(x, y float64) (int, int) {
		cx, cy := int((x-all.left)/w*cells), int((y-all.bottom)/h*cells)
		return min(max(cx, 0), cells-1), min(max(cy, 0), cells-1)
	}
	grid := make([][]int, cells*cells)
	for k, o := range page.objs {
		if o.image {
			continue
		}
		x0, y0 := cell(o.box.left, o.box.bottom)
		x1, y1 := cell(o.box.right, o.box.top)
		for x := x0; x <= x1; x++ {
			for y := y0; y <= y1; y++ {
				grid[y*cells+x] = append(grid[y*cells+x], k)
			}
		}
	}
	for c, b := range boxes {
		if b.right <= b.left && b.top <= b.bottom {
			continue // a character PDFium added
		}
		x, y := (b.left+b.right)/2, (b.bottom+b.top)/2
		if !all.holds(x, y) {
			continue
		}
		cx, cy := cell(x, y)
		best, area := -1, 0.0
		for _, k := range grid[cy*cells+cx] {
			if o := page.objs[k]; o.box.holds(x, y) && (best < 0 || o.box.area() < area) {
				best, area = k, o.box.area()
			}
		}
		page.owner[c] = best + 1
	}
	// A space PDFium adds inside a run (for a gap TJ leaves) belongs to
	// the object on both sides of it, so its new text keeps the word gap.
	for c := 1; c+1 < len(page.text); c++ {
		if page.owner[c] == 0 && page.text[c] == ' ' && page.owner[c-1] != 0 && page.owner[c-1] == page.owner[c+1] {
			page.owner[c] = page.owner[c-1]
		}
	}
}

// checkScan refuses a scanned page: rewriting its text layer would leave
// the names in the image under it.
func (d *pdfDoc) checkScan(ref requests.Page, page *pdfPage) error {
	// The page's box (its crop box within its media box, inherited or not),
	// in the space objects' bounds are in: it need not start at 0,0, and the
	// page's rotation does not turn it.
	bb, err := d.s.p.FPDF_GetPageBoundingBox(&requests.FPDF_GetPageBoundingBox{Page: ref})
	if err != nil {
		return err
	}
	r := bb.Rect
	bounds := pdfRect{float64(min(r.Left, r.Right)), float64(min(r.Bottom, r.Top)), float64(max(r.Left, r.Right)), float64(max(r.Bottom, r.Top))}
	images := 0.0
	for _, o := range page.objs {
		if o.image {
			images += intersect(o.box, bounds).area()
		}
	}
	if images < pdfScanArea*bounds.area() {
		return nil
	}
	visible, hidden := 0, 0
	for c, r := range page.text {
		if unicode.IsSpace(r) {
			continue
		}
		if k := page.owner[c]; k > 0 && invisible(page.objs[k-1].mode) {
			hidden++
		} else {
			visible++
		}
	}
	if visible < pdfScanText || hidden > 0 {
		return errors.New("it is a scan: its text is in an image, which cannot be anonymised")
	}
	return nil
}

func invisible(mode enums.FPDF_TEXT_RENDERMODE) bool {
	return mode == enums.FPDF_TEXTRENDERMODE_INVISIBLE || mode == enums.FPDF_TEXTRENDERMODE_CLIP
}

// texts returns what detection reads: each page's text, the text of any
// object the page's text does not account for, marked content, and the
// strings outside the drawing (a document's author is a person).
func (d *pdfDoc) texts() []docText {
	var out []docText
	for _, page := range d.pages {
		out = append(out, docText{value: string(page.text)})
		accounted := page.accounting()
		for k, o := range page.objs {
			if !o.image && !accounted[k] {
				out = append(out, docText{value: o.text})
			}
		}
		for _, m := range page.marks {
			out = append(out, docText{value: m.value})
		}
		for _, l := range page.links {
			if l.uri != "" {
				out = append(out, docText{value: l.uri})
			}
		}
	}
	for _, s := range d.strings {
		t := docText{value: s.value}
		if s.key == "Author" {
			t.entity = EntityPerson
		}
		out = append(out, t)
	}
	return out
}

// accounting reports, for each object, whether it is a text object whose
// characters in the page's text are its own text, so it can be rewritten
// from them. One pass over the page.
func (page *pdfPage) accounting() []bool {
	own := make([]strings.Builder, len(page.objs))
	for c, k := range page.owner {
		if k > 0 {
			own[k-1].WriteRune(page.text[c])
		}
	}
	out := make([]bool, len(page.objs))
	for k, o := range page.objs {
		out[k] = !o.image && withoutSpace(own[k].String()) == withoutSpace(o.text)
	}
	return out
}

func withoutSpace(s string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsSpace(r) {
			return -1
		}
		return r
	}, s)
}

// pdfPlan is what changes on a page: text objects' new text (by index in
// objs; "" removes one), and marked-content strings.
type pdfPlan struct {
	text  map[int]string
	marks map[int]string
	// room is, for a text object written from the page's characters, the
	// stretch of the page its new text may take: from its first character
	// kept to the last of what it now holds, the characters of a match
	// whose fake it took included, though they were in the next objects.
	room map[int]pdfRect
}

// plan works out a page's new text, and draws the objects in redraw
// again as they are. A match in the page's text may run
// across text objects ("Sarah" in one, "Lane" in the next); its fake goes
// in the object holding its first character, and the rest of it goes.
// An object the page's text does not account for is rewritten from its own
// text.
func (page *pdfPage) plan(rep *replacer, redraw map[int]bool) (pdfPlan, error) {
	pl := pdfPlan{text: map[int]string{}, marks: map[int]string{}, room: map[int]pdfRect{}}
	text := string(page.text)
	runeAt := make([]int, len(text)+1) // byte offset -> character index
	c := 0
	for i := range text {
		runeAt[i] = c
		c++
	}
	runeAt[len(text)] = c
	accounted := page.accounting()
	inMatch := make([]int, len(page.text)) // match index + 1
	var fakes []string
	var firsts []int
	var spans [][2]int // each match's characters
	for _, m := range rep.Matches(text) {
		fakes = append(fakes, m.To)
		spans = append(spans, [2]int{runeAt[m.Start], runeAt[m.End]})
		first := -1 // the fake goes with the match's first character drawn
		for c := runeAt[m.Start]; c < runeAt[m.End]; c++ {
			inMatch[c] = len(fakes)
			if first < 0 && page.owner[c] > 0 {
				first = c
			}
		}
		firsts = append(firsts, first)
	}
	touched := map[int]*strings.Builder{}
	room := map[int]pdfRect{}
	widen := func(k, c int) {
		if c >= len(page.boxes) {
			return
		}
		b := page.boxes[c]
		if b.right <= b.left {
			return
		}
		r, ok := room[k]
		if !ok {
			r = b
		}
		room[k] = pdfRect{math.Min(r.left, b.left), math.Min(r.bottom, b.bottom), math.Max(r.right, b.right), math.Max(r.top, b.top)}
	}
	for c, k := range page.owner {
		if k == 0 || !accounted[k-1] {
			continue
		}
		b := touched[k-1]
		if b == nil {
			b = &strings.Builder{}
			touched[k-1] = b
		}
		switch m := inMatch[c]; {
		case m == 0:
			b.WriteRune(page.text[c])
			widen(k-1, c)
		case firsts[m-1] == c:
			b.WriteString(fakes[m-1])
			for c2 := spans[m-1][0]; c2 < spans[m-1][1]; c2++ {
				widen(k-1, c2)
			}
		}
	}
	for k, o := range page.objs {
		if o.image {
			continue
		}
		if accounted[k] {
			if b := touched[k]; b != nil && withoutSpace(b.String()) != withoutSpace(o.text) {
				pl.text[k] = b.String()
				if r, ok := room[k]; ok {
					pl.room[k] = r
				}
			}
			continue
		}
		// The part of a match this object draws, when the rest is in other
		// objects, goes from its own text: the fake, where the match's first
		// character is here, or nothing.
		v := o.text
		for mi, sp := range spans {
			var part []rune
			first := false
			for c := sp[0]; c < sp[1]; c++ {
				if page.owner[c] == k+1 {
					part = append(part, page.text[c])
					first = first || c == firsts[mi]
				}
			}
			if len(part) == 0 || len(part) == sp[1]-sp[0] || strings.TrimSpace(string(part)) == "" {
				continue // none of it, or all of it: rep.Apply below
			}
			to := ""
			if first {
				to = fakes[mi]
			}
			if !strings.Contains(v, string(part)) {
				return pl, fmt.Errorf("text object %d draws part of a match its own text does not hold", k)
			}
			v = strings.Replace(v, string(part), to, 1)
		}
		if v = rep.Apply(v); v != o.text {
			pl.text[k] = v
		}
	}
	// An object drawn again only because PDFium wrote it back wrong keeps its
	// text, rebuilt from the page's characters (with the spaces PDFium adds
	// between its words) where they are its own.
	for k := range redraw {
		if _, ok := pl.text[k]; ok {
			continue
		}
		if b := touched[k]; accounted[k] && b != nil {
			pl.text[k] = b.String()
			if r, ok := room[k]; ok {
				pl.room[k] = r
			}
		} else {
			pl.text[k] = page.objs[k].text
		}
	}
	for i, m := range page.marks {
		if v := rep.Apply(m.value); v != m.value {
			pl.marks[i] = v
		}
	}
	for k, v := range pl.text {
		if r, ok := notWinAnsi(v); ok {
			return pl, fmt.Errorf("text object %d holds %U, which the standard fonts its new text is drawn in cannot show", k, r)
		}
	}
	return pl, nil
}

// rewritePage applies a page's plan, and replaces its images.
func (d *pdfDoc) rewritePage(i int, pl pdfPlan) error {
	page := d.pages[i]
	// A page that does not change is not written again: PDFium writes some
	// fonts' text back wrong (see anonymise), and its images are replaced
	// as objects (pdfcpu.go). One with an image inline in its content is,
	// as only PDFium can replace that.
	if len(pl.text) == 0 && len(pl.marks) == 0 && !page.inline {
		return nil
	}
	return d.withPage(i, func(ref requests.Page) error {
		p := d.s.p
		for _, o := range page.objs {
			if o.image {
				if err := d.blankImage(ref, o.path); err != nil {
					return d.s.fail(err)
				}
			}
		}
		// In order, so the same file makes the same calls.
		for _, idx := range slices.Sorted(maps.Keys(pl.marks)) {
			m, v := page.marks[idx], pl.marks[idx]
			obj, _, err := d.find(ref, m.path)
			if err != nil {
				return d.s.fail(err)
			}
			mark, err := p.FPDFPageObj_GetMark(&requests.FPDFPageObj_GetMark{PageObject: obj, Index: uint64(m.mark)})
			if err != nil {
				return d.s.fail(err)
			}
			if _, err := p.FPDFPageObjMark_SetStringParam(&requests.FPDFPageObjMark_SetStringParam{Document: d.doc, PageObject: obj, PageObjectMark: mark.Mark, Key: m.key, Value: v}); err != nil {
				return d.s.fail(err)
			}
		}
		// A text object on the page or in a form on it is replaced where it
		// is; one deeper is drawn again with the form it is in (liftForm).
		// PDFium writes a form's content only when an object leaves it, so
		// an object in a form whose marked content changed leaves it too:
		// moved onto the page, or with its form lifted. The last objects
		// first, so the paths of the others hold.
		const (
			stepReplace = iota
			stepLift
			stepMove
		)
		type step struct {
			path []int
			kind int
			obj  int // for stepReplace
		}
		var steps []step
		planned := map[string]bool{}
		edits := map[string]int{}
		add := func(path []int, kind, obj int) {
			if key := fmt.Sprint(path); !planned[key] {
				planned[key] = true
				steps = append(steps, step{path, kind, obj})
			}
		}
		for k := range pl.text {
			path := page.objs[k].path
			if len(path) <= 2 {
				add(path, stepReplace, k)
				continue
			}
			edits[fmt.Sprint(path)] = k
			add(path[:2], stepLift, 0)
		}
		for idx := range pl.marks {
			switch path := page.marks[idx].path; {
			case len(path) == 2:
				add(path, stepMove, 0) // unless it is replaced
			case len(path) > 2:
				add(path[:2], stepLift, 0)
			}
		}
		sort.Slice(steps, func(a, b int) bool { return slices.Compare(steps[a].path, steps[b].path) > 0 })
		for _, st := range steps {
			var err error
			switch st.kind {
			case stepLift:
				err = d.liftForm(ref, st.path, &page, edits, pl)
			case stepMove:
				err = d.moveOut(ref, st.path)
			default:
				err = d.replaceText(ref, page.objs[st.obj], pl.text[st.obj], pl.room[st.obj])
			}
			if err != nil {
				return d.s.fail(err)
			}
		}
		_, err := p.FPDFPage_GenerateContent(&requests.FPDFPage_GenerateContent{Page: ref})
		return d.s.fail(err)
	})
}

// replaceText removes a text object and draws text where it was, in the
// standard font nearest its own. One inside a form is drawn on the page,
// just after the form: PDFium cannot add an object to a form.
func (d *pdfDoc) replaceText(ref requests.Page, o pdfObj, text string, room pdfRect) error {
	p := d.s.p
	obj, form, err := d.find(ref, o.path)
	if err != nil {
		return err
	}
	if form == "" {
		_, err = p.FPDFPage_RemoveObject(&requests.FPDFPage_RemoveObject{Page: ref, PageObject: obj})
	} else {
		err = d.s.removeFromForm(form, obj)
	}
	if err != nil {
		return err
	}
	defer func() { _, _ = p.FPDFPageObj_Destroy(&requests.FPDFPageObj_Destroy{PageObject: obj}) }()
	at := o.path[0]
	if len(o.path) > 1 {
		at++
	}
	_, err = d.drawText(ref, obj, o, text, room, at)
	return err
}

// drawText draws text in place of the text object src, described by o, at
// index at on the page. Text running left to right starts at room's left
// and is squeezed to its width; other text starts where o did and is
// squeezed to o's length.
func (d *pdfDoc) drawText(ref requests.Page, src references.FPDF_PAGEOBJECT, o pdfObj, text string, room pdfRect, at int) (bool, error) {
	if strings.TrimSpace(text) == "" {
		return false, nil
	}
	p := d.s.p
	obj := src
	size, err := p.FPDFTextObj_GetFontSize(&requests.FPDFTextObj_GetFontSize{PageObject: obj})
	if err != nil {
		return false, err
	}
	font, err := d.fontOf(obj)
	if err != nil {
		return false, err
	}
	fill, fillErr := p.FPDFPageObj_GetFillColor(&requests.FPDFPageObj_GetFillColor{PageObject: obj})
	stroke, strokeErr := p.FPDFPageObj_GetStrokeColor(&requests.FPDFPageObj_GetStrokeColor{PageObject: obj})
	n, err := p.FPDFPageObj_NewTextObj(&requests.FPDFPageObj_NewTextObj{Document: d.doc, Font: font, FontSize: size.FontSize})
	if err != nil {
		return false, err
	}
	if _, err := p.FPDFText_SetText(&requests.FPDFText_SetText{PageObject: n.PageObject, Text: text}); err != nil {
		return false, err
	}
	if fillErr == nil {
		_, _ = p.FPDFPageObj_SetFillColor(&requests.FPDFPageObj_SetFillColor{PageObject: n.PageObject, FillColor: fill.FillColor})
	}
	if strokeErr == nil {
		_, _ = p.FPDFPageObj_SetStrokeColor(&requests.FPDFPageObj_SetStrokeColor{PageObject: n.PageObject, StrokeColor: stroke.StrokeColor})
	}
	if _, err := p.FPDFTextObj_SetTextRenderMode(&requests.FPDFTextObj_SetTextRenderMode{PageObject: n.PageObject, TextRenderMode: o.mode}); err != nil {
		return false, err
	}
	m, width := o.matrix, o.width
	if upright(m) && room.right > room.left {
		m.E += float32(room.left - o.box.left)
		width = room.right - room.left
	}
	if _, err := p.FPDFPageObj_SetMatrix(&requests.FPDFPageObj_SetMatrix{PageObject: n.PageObject, Transform: m}); err != nil {
		return false, err
	}
	q, err := p.FPDFPageObj_GetRotatedBounds(&requests.FPDFPageObj_GetRotatedBounds{PageObject: n.PageObject})
	if err != nil {
		return false, err
	}
	if w := baseline(q.QuadPoints, structs.FPDF_FS_MATRIX{A: 1, D: 1}); w > width && width > 0 {
		f := float32(width / w)
		m.A, m.B = m.A*f, m.B*f
		if _, err := p.FPDFPageObj_SetMatrix(&requests.FPDFPageObj_SetMatrix{PageObject: n.PageObject, Transform: m}); err != nil {
			return false, err
		}
	}
	if _, err = p.FPDFPage_InsertObjectAtIndex(&requests.FPDFPage_InsertObjectAtIndex{Page: ref, PageObject: n.PageObject, Index: at}); err != nil {
		return false, err
	}
	return true, nil
}

// moveOut moves the object at path, in a form on the page, onto the page
// just after the form, mapped as the form mapped it. An object PDFium
// cannot move (a shading) is dropped.
func (d *pdfDoc) moveOut(ref requests.Page, path []int) error {
	p := d.s.p
	obj, form, err := d.find(ref, path)
	if err != nil {
		return err
	}
	fm, err := p.FPDFPageObj_GetMatrix(&requests.FPDFPageObj_GetMatrix{PageObject: form})
	if err != nil {
		return err
	}
	om, err := p.FPDFPageObj_GetMatrix(&requests.FPDFPageObj_GetMatrix{PageObject: obj})
	if err != nil {
		return err
	}
	if err := d.s.removeFromForm(form, obj); err != nil {
		return err
	}
	if _, err := p.FPDFPageObj_SetMatrix(&requests.FPDFPageObj_SetMatrix{PageObject: obj, Transform: mulMatrix(om.Matrix, fm.Matrix)}); err != nil {
		_, _ = p.FPDFPageObj_Destroy(&requests.FPDFPageObj_Destroy{PageObject: obj})
		return nil
	}
	_, err = p.FPDFPage_InsertObjectAtIndex(&requests.FPDFPage_InsertObjectAtIndex{Page: ref, PageObject: obj, Index: path[0] + 1})
	return err
}

// liftForm moves the form at prefix (a form in a form on the page) onto
// the page, object by object, drawing the text objects in edits (by path)
// anew. PDFium writes a form's new content only for a form on the page
// that an object was removed from, so a text object inside a form inside a
// form cannot be removed where it is: the inner form is taken out of the
// outer one instead, and what it drew is drawn on the page, after the
// outer form, each object mapped by the forms it was in. An object PDFium
// cannot move (a shading) is lost.
func (d *pdfDoc) liftForm(ref requests.Page, prefix []int, page *pdfPage, edits map[string]int, plan pdfPlan) error {
	p := d.s.p
	inner, outer, err := d.find(ref, prefix)
	if err != nil {
		return err
	}
	om, err := p.FPDFPageObj_GetMatrix(&requests.FPDFPageObj_GetMatrix{PageObject: outer})
	if err != nil {
		return err
	}
	if err := d.s.removeFromForm(outer, inner); err != nil {
		return err
	}
	at := prefix[0] + 1
	var lift func(form references.FPDF_PAGEOBJECT, path []int, m structs.FPDF_FS_MATRIX) error
	lift = func(form references.FPDF_PAGEOBJECT, path []int, m structs.FPDF_FS_MATRIX) error {
		defer func() { _, _ = p.FPDFPageObj_Destroy(&requests.FPDFPageObj_Destroy{PageObject: form}) }()
		fm, err := p.FPDFPageObj_GetMatrix(&requests.FPDFPageObj_GetMatrix{PageObject: form})
		if err != nil {
			return err
		}
		m = mulMatrix(fm.Matrix, m)
		n, err := d.countObjects(ref, form)
		if err != nil {
			return err
		}
		for i := 0; i < n; i++ {
			child, err := d.object(ref, form, 0) // the others moved up as each went
			if err != nil {
				return err
			}
			if err := d.s.removeFromForm(form, child); err != nil {
				return err
			}
			at2 := append(slices.Clone(path), i)
			ty, err := p.FPDFPageObj_GetType(&requests.FPDFPageObj_GetType{PageObject: child})
			if err != nil {
				return err
			}
			if ty.Type == enums.FPDF_PAGEOBJ_FORM {
				if err := lift(child, at2, m); err != nil {
					return err
				}
				continue
			}
			if k, ok := edits[fmt.Sprint(at2)]; ok {
				drawn, err := d.drawText(ref, child, page.objs[k], plan.text[k], plan.room[k], at)
				_, _ = p.FPDFPageObj_Destroy(&requests.FPDFPageObj_Destroy{PageObject: child})
				if err != nil {
					return err
				}
				if drawn {
					at++
				}
				continue
			}
			cm, err := p.FPDFPageObj_GetMatrix(&requests.FPDFPageObj_GetMatrix{PageObject: child})
			if err == nil {
				_, err = p.FPDFPageObj_SetMatrix(&requests.FPDFPageObj_SetMatrix{PageObject: child, Transform: mulMatrix(cm.Matrix, m)})
			}
			if err != nil {
				_, _ = p.FPDFPageObj_Destroy(&requests.FPDFPageObj_Destroy{PageObject: child})
				continue
			}
			if _, err := p.FPDFPage_InsertObjectAtIndex(&requests.FPDFPage_InsertObjectAtIndex{Page: ref, PageObject: child, Index: at}); err != nil {
				return err
			}
			at++
		}
		return nil
	}
	return lift(inner, prefix, om.Matrix)
}

// fontOf names the standard font nearest a text object's own.
func (d *pdfDoc) fontOf(obj references.FPDF_PAGEOBJECT) (string, error) {
	p := d.s.p
	f, err := p.FPDFTextObj_GetFont(&requests.FPDFTextObj_GetFont{PageObject: obj})
	if err != nil {
		return "", err
	}
	var font pdfFont
	if n, err := p.FPDFFont_GetBaseFontName(&requests.FPDFFont_GetBaseFontName{Font: f.Font}); err == nil {
		font.name = n.BaseFontName
	}
	if n, err := p.FPDFFont_GetFamilyName(&requests.FPDFFont_GetFamilyName{Font: f.Font}); err == nil {
		font.family = n.FamilyName
	}
	if fl, err := p.FPDFFont_GetFlags(&requests.FPDFFont_GetFlags{Font: f.Font}); err == nil {
		font.flags = fl.Flags
	}
	font.weight = -1
	if w, err := p.FPDFFont_GetWeight(&requests.FPDFFont_GetWeight{Font: f.Font}); err == nil {
		font.weight = w.Weight
	}
	if a, err := p.FPDFFont_GetItalicAngle(&requests.FPDFFont_GetItalicAngle{Font: f.Font}); err == nil {
		font.italicAngle = a.ItalicAngle
	}
	return font.standard(), nil
}

// blankImage replaces an image's pixels with a plain grey bitmap, which
// PDF viewers stretch over the same place.
func (d *pdfDoc) blankImage(ref requests.Page, path []int) error {
	p := d.s.p
	obj, _, err := d.find(ref, path)
	if err != nil {
		return err
	}
	bm, err := p.FPDFBitmap_Create(&requests.FPDFBitmap_Create{Width: 8, Height: 8, Alpha: 0})
	if err != nil {
		return err
	}
	defer func() { _, _ = p.FPDFBitmap_Destroy(&requests.FPDFBitmap_Destroy{Bitmap: bm.Bitmap}) }()
	if _, err := p.FPDFBitmap_FillRect(&requests.FPDFBitmap_FillRect{Bitmap: bm.Bitmap, Width: 8, Height: 8, Color: 0xFFD9D9D9}); err != nil {
		return err
	}
	// No pages: PDFium takes an array of them, to clear their caches, but
	// go-pdfium passes it the page itself, which PDFium then reads as an
	// array.
	_, err = p.FPDFImageObj_SetBitmap(&requests.FPDFImageObj_SetBitmap{ImageObject: obj, Bitmap: bm.Bitmap})
	return err
}

// save writes the document as one new file: no earlier revisions, and no
// encryption.
func (d *pdfDoc) save() ([]byte, error) {
	flags := requests.SaveFlagNoIncremental
	if d.secured {
		flags = requests.SaveFlagRemoveSecurity
	}
	var b bytes.Buffer
	if _, err := d.s.p.FPDF_SaveAsCopy(&requests.FPDF_SaveAsCopy{Document: d.doc, Flags: flags, FileWriter: &b}); err != nil {
		return nil, fmt.Errorf("writing the PDF: %w", d.s.fail(err))
	}
	return b.Bytes(), nil
}

// checkPDF opens the anonymised PDF and checks that its text objects hold
// what was written, page by page: the new text, and the objects kept. An
// object PDFium failed to remove, or a form it failed to rewrite, would
// add its old text, which fails the file; an object kept whose text comes
// back different (PDFium wrote it back wrong) is returned, to be drawn
// again. It returns the PDF as PDFium writes it, with an identifier made
// from its content.
func checkPDF(ctx context.Context, data []byte, pages []pdfPage, plans []pdfPlan) ([]byte, pdfRedraw, error) {
	s, err := openPDFium(ctx)
	if err != nil {
		return nil, nil, err
	}
	defer s.close()
	d := &pdfDoc{s: s}
	doc, err := s.p.FPDF_LoadMemDocument64(&requests.FPDF_LoadMemDocument64{Data: &data})
	if err != nil {
		return nil, nil, fmt.Errorf("anonymised PDF does not open: %w", s.fail(err))
	}
	d.doc = doc.Document
	count, err := s.p.FPDF_GetPageCount(&requests.FPDF_GetPageCount{Document: d.doc})
	if err != nil || count.PageCount != len(pages) {
		return nil, nil, errors.New("anonymising changed the PDF's pages")
	}
	redo := pdfRedraw{}
	for i := range pages {
		want := map[string][]int{} // text -> the objects that should hold it
		for k, o := range pages[i].objs {
			if o.image {
				continue
			}
			t, ok := plans[i].text[k]
			if !ok {
				t = o.text
			}
			if t = withoutSpace(t); t != "" {
				want[t] = append(want[t], k)
			}
		}
		// A marked-content string changed must not be found as it was.
		replaced := map[string]bool{}
		for k := range plans[i].text {
			replaced[fmt.Sprint(pages[i].objs[k].path)] = true
		}
		gone := map[string]bool{}
		for idx := range plans[i].marks {
			if m := pages[i].marks[idx]; !replaced[fmt.Sprint(m.path)] {
				gone[m.value] = true
			}
		}
		unexpected, leftMarks := 0, 0
		if err := d.withPage(i, func(ref requests.Page) error {
			tp, err := s.p.FPDFText_LoadPage(&requests.FPDFText_LoadPage{Page: ref})
			if err != nil {
				return err
			}
			defer func() { _, _ = s.p.FPDFText_ClosePage(&requests.FPDFText_ClosePage{TextPage: tp.TextPage}) }()
			var page pdfPage
			if err := d.walk(ref, tp.TextPage, "", nil, structs.FPDF_FS_MATRIX{A: 1, D: 1}, &page, 0); err != nil {
				return err
			}
			for _, o := range page.objs {
				t := withoutSpace(o.text)
				switch {
				case t == "":
				case len(want[t]) > 0:
					want[t] = want[t][1:]
				default:
					unexpected++
				}
			}
			for _, m := range page.marks {
				if gone[m.value] {
					leftMarks++
				}
			}
			return nil
		}); err != nil {
			return nil, nil, fmt.Errorf("anonymised PDF, page %d: %w", i+1, s.fail(err))
		}
		missing := 0
		for _, ks := range want {
			for _, k := range ks {
				if _, written := plans[i].text[k]; written {
					return nil, nil, fmt.Errorf("anonymised PDF, page %d: its new text is not there", i+1)
				}
				if redo[i] == nil {
					redo[i] = map[int]bool{}
				}
				redo[i][k] = true
				missing++
			}
		}
		if leftMarks > 0 {
			return nil, nil, fmt.Errorf("anonymised PDF, page %d: its marked content was not rewritten", i+1)
		}
		if unexpected > missing {
			return nil, nil, fmt.Errorf("anonymised PDF, page %d: it holds text that was not written", i+1)
		}
	}
	if len(redo) > 0 {
		return nil, redo, nil
	}
	written, err := d.save()
	if err != nil {
		return nil, nil, err
	}
	return stableID(written), nil, nil
}

// pdfFont is what PDFium says about a font.
type pdfFont struct {
	name, family string
	flags        uint32
	weight       int
	italicAngle  int
}

// Font descriptor flags (PDF 32000-1, 9.8.2).
const (
	pdfFontFixedPitch = 1 << 0
	pdfFontSerif      = 1 << 1
	pdfFontItalic     = 1 << 6
	pdfFontForceBold  = 1 << 18
)

// standard returns the standard font nearest f: Courier for a fixed-pitch
// font, Times for a serif one, Helvetica for the rest, bold and italic as
// f is. Its name, its family and its descriptor's flags all count, since
// embedded subsets often set only some of them.
func (f pdfFont) standard() string {
	name := strings.ToLower(f.name)
	if i := strings.IndexByte(name, '+'); i == 6 {
		name = name[7:] // a subset's tag, "ABCDEF+"
	}
	name += " " + strings.ToLower(f.family)
	has := func(words ...string) bool {
		for _, w := range words {
			if strings.Contains(name, w) {
				return true
			}
		}
		return false
	}
	bold := f.weight >= 600 || f.flags&pdfFontForceBold != 0 || has("bold", "black", "heavy", "semibold", "demi")
	italic := f.flags&pdfFontItalic != 0 || f.italicAngle != 0 || has("italic", "oblique")
	mono := f.flags&pdfFontFixedPitch != 0 || has("courier", "mono", "consol", "menlo", "monaco", "typewriter")
	serif := !mono && !has("sans") && (f.flags&pdfFontSerif != 0 ||
		has("times", "georgia", "garamond", "cambria", "serif", "palatino", "antiqua", "minion", "baskerville", "caslon", "didot", "bodoni", "century", "roman"))
	switch {
	case mono:
		return "Courier" + pick(bold, italic, "", "-Bold", "-Oblique", "-BoldOblique")
	case serif:
		return "Times" + pick(bold, italic, "-Roman", "-Bold", "-Italic", "-BoldItalic")
	}
	return "Helvetica" + pick(bold, italic, "", "-Bold", "-Oblique", "-BoldOblique")
}

func pick(bold, italic bool, regular, b, i, bi string) string {
	switch {
	case bold && italic:
		return bi
	case bold:
		return b
	case italic:
		return i
	}
	return regular
}

// winAnsiExtra are the characters WinAnsiEncoding, which the standard
// fonts are drawn in, has beyond Latin-1.
const winAnsiExtra = "€‚ƒ„…†‡ˆ‰Š‹ŒŽ‘’“”•–—˜™š›œžŸ"

// notWinAnsi returns the first character of s the standard fonts cannot
// show.
func notWinAnsi(s string) (rune, bool) {
	for _, r := range s {
		if (r >= 0x20 && r <= 0x7e) || (r >= 0xa0 && r <= 0xff) || strings.ContainsRune(winAnsiExtra, r) {
			continue
		}
		return r, true
	}
	return 0, false
}

// upright reports whether text drawn by m runs left to right, unrotated.
func upright(m structs.FPDF_FS_MATRIX) bool {
	return m.A > 0 && m.D > 0 && math.Abs(float64(m.B)) < 1e-3*float64(m.A) && math.Abs(float64(m.C)) < 1e-3*float64(m.D)
}

// mulMatrix returns a then b, as PDF composes them: a point is mapped by a,
// then by b.
func mulMatrix(a, b structs.FPDF_FS_MATRIX) structs.FPDF_FS_MATRIX {
	return structs.FPDF_FS_MATRIX{
		A: a.A*b.A + a.B*b.C, B: a.A*b.B + a.B*b.D,
		C: a.C*b.A + a.D*b.C, D: a.C*b.B + a.D*b.D,
		E: a.E*b.A + a.F*b.C + b.E, F: a.E*b.B + a.F*b.D + b.F,
	}
}

func transformPoint(x, y float64, m structs.FPDF_FS_MATRIX) (float64, float64) {
	return x*float64(m.A) + y*float64(m.C) + float64(m.E), x*float64(m.B) + y*float64(m.D) + float64(m.F)
}

// transformRect returns the box holding r mapped by m.
func transformRect(r pdfRect, m structs.FPDF_FS_MATRIX) pdfRect {
	out := pdfRect{math.Inf(1), math.Inf(1), math.Inf(-1), math.Inf(-1)}
	for _, p := range [][2]float64{{r.left, r.bottom}, {r.right, r.bottom}, {r.left, r.top}, {r.right, r.top}} {
		x, y := transformPoint(p[0], p[1], m)
		out = pdfRect{math.Min(out.left, x), math.Min(out.bottom, y), math.Max(out.right, x), math.Max(out.top, y)}
	}
	return out
}

func intersect(a, b pdfRect) pdfRect {
	return pdfRect{math.Max(a.left, b.left), math.Max(a.bottom, b.bottom), math.Min(a.right, b.right), math.Min(a.top, b.top)}
}

// baseline returns the length of a text object's rotated box along its
// text, the edge from its first corner to its second, mapped by m.
func baseline(q structs.FPDF_FS_QUADPOINTSF, m structs.FPDF_FS_MATRIX) float64 {
	x1, y1 := transformPoint(float64(q.X1), float64(q.Y1), m)
	x2, y2 := transformPoint(float64(q.X2), float64(q.Y2), m)
	return math.Hypot(x2-x1, y2-y1)
}
