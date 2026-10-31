package anonymise

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/pdfcpu/pdfcpu/pkg/api"
	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/model"
	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/types"
)

// Much of what a PDF says about people sits outside its pages' drawing, as
// strings in its objects: the document's title and author, bookmarks, link
// targets, the names of its layers and destinations. PDFium cannot write
// those, so pdfcpu does, on the file PDFium wrote (never on the original,
// which only PDFium's sandbox reads). The same pass drops what no rule
// knows: scripts, embedded files, form fields, the structure tree, XMP
// metadata, page thumbnails and applications' private data.

// pdfString is a string stored outside the drawing, under the key it sits
// at ("Title", "URI").
type pdfString struct {
	key, value string
}

// pdfDrops are the keys whose values are dropped wherever they are, under
// the name the dropped part is listed by.
var pdfDrops = map[string]string{
	"Metadata":  "XMP metadata",
	"PieceInfo": "application data",
	"Thumb":     "page thumbnails",
	"AA":        "JavaScript",
	"JS":        "JavaScript",
}

// pdfRootDrops are the catalog's entries that are dropped.
var pdfRootDrops = map[string]string{
	"AcroForm":       "form fields",
	"StructTreeRoot": "structure tree",
	"MarkInfo":       "structure tree",
	"Perms":          "digital signatures",
	"DSS":            "digital signatures",
	"Collection":     "portfolio",
	"SpiderInfo":     "web capture",
	"Legal":          "digital signatures",
}

// pdfNameDrops are the name trees that are dropped.
var pdfNameDrops = map[string]string{
	"EmbeddedFiles": "embedded files",
	"JavaScript":    "JavaScript",
}

const (
	// pdfMaxDecoded is the most one content stream may decode to.
	pdfMaxDecoded = 64 << 20
	// pdfMaxFormContent is the most a form's /Contents may decode to in
	// total, however many streams hold it, and pdfMaxContentTotal the most
	// every page's content may decode to while the pages are read: a file
	// of many large streams must not multiply into many times its size in
	// memory.
	pdfMaxFormContent  = 64 << 20
	pdfMaxContentTotal = 256 << 20
)

// pdfID matches a file identifier as PDFium writes it.
var pdfID = regexp.MustCompile(`/ID\s*\[\s*<([0-9A-Fa-f]*)>\s*<([0-9A-Fa-f]*)>\s*\]`)

// stableID replaces the random identifier PDFium gives a file it writes
// with a hash of the rest of the file, the same length, so no offset moves
// and the same file anonymised with the same key comes out the same.
func stableID(data []byte) []byte {
	m := pdfID.FindSubmatchIndex(data)
	if m == nil {
		return data
	}
	sum := sha256.Sum256(pdfID.ReplaceAll(data, nil))
	id := hex.EncodeToString(sum[:])
	for _, g := range [][2]int{{m[2], m[3]}, {m[4], m[5]}} {
		if n := g[1] - g[0]; n <= len(id) {
			copy(data[g[0]:g[1]], strings.ToUpper(id[:n]))
		}
	}
	return data
}

func readPDFContext(c context.Context, data []byte) (ctx *model.Context, err error) {
	defer func() {
		if r := recover(); r != nil {
			ctx, err = nil, fmt.Errorf("reading the PDF's objects: %v", r)
		}
	}()
	// Stateless: pdfcpu's default configuration reads and writes a folder
	// in the user's home.
	conf := model.NewStatelessConfiguration()
	conf.ValidationMode = model.ValidationRelaxed
	conf.Offline = true
	// Keep the dates and producer the file has; pdfcpu would put the
	// time of the run in them.
	conf.PreserveInfoDict = true
	// Plain objects, which PDFium writes again in their numbers' order
	// (checkPDF); objects packed in streams it would copy as pdfcpu laid
	// them out, which is in no fixed order.
	conf.WriteObjectStream = false
	conf.WriteXRefStream = false
	ctx, err = api.ReadContext(c, bytes.NewReader(data), conf)
	if err != nil {
		return nil, fmt.Errorf("reading the PDF's objects: %w", err)
	}
	return ctx, nil
}

// pdfStrings returns the strings of a PDF outside its drawing, and which
// of its pages draw an image inline (in their content, not as an object).
func pdfStrings(c context.Context, data []byte) ([]pdfString, []bool, error) {
	ctx, err := readPDFContext(c, data)
	if err != nil {
		return nil, nil, err
	}
	var out []pdfString
	walkPDFStrings(ctx.XRefTable, func(key, v string) (string, bool) {
		out = append(out, pdfString{key: key, value: v})
		return "", false
	})
	inline, err := inlineImages(c, ctx)
	if err != nil {
		return nil, nil, err
	}
	return out, inline, nil
}

// pdfInlineImage finds the operator that starts an inline image.
var pdfInlineImage = regexp.MustCompile(`(?:^|\s)BI\s*/`)

// inlineImages reports, page by page, whether a page's content holds an
// inline image. Its pixels are in the content stream, which pdfcpu does not
// rewrite, so such a page is written again by PDFium, which replaces them.
func inlineImages(c context.Context, ctx *model.Context) ([]bool, error) {
	if err := ctx.EnsurePageCount(); err != nil {
		return nil, fmt.Errorf("reading the PDF's pages: %w", err)
	}
	x := ctx.XRefTable
	out := make([]bool, x.PageCount)
	decoded := 0
	for i := range out {
		d, _, _, err := x.PageDict(c, i+1, false)
		if err != nil || d == nil {
			return nil, fmt.Errorf("reading page %d: %v", i+1, err)
		}
		contents, ok := d.Find("Contents")
		if !ok {
			continue
		}
		o, err := x.Dereference(contents)
		if err != nil {
			return nil, fmt.Errorf("reading page %d: %w", i+1, err)
		}
		streams := []types.Object{o}
		if a, ok := o.(types.Array); ok {
			streams = a
		}
		for _, r := range streams {
			so, err := x.Dereference(r)
			if err != nil {
				return nil, fmt.Errorf("reading page %d: %w", i+1, err)
			}
			sd, ok := so.(types.StreamDict)
			if !ok {
				continue
			}
			if err := sd.DecodeWithLimit(pdfMaxDecoded); err != nil {
				return nil, fmt.Errorf("reading page %d: %w", i+1, err)
			}
			decoded += len(sd.Content)
			if decoded > pdfMaxContentTotal {
				return nil, fmt.Errorf("the PDF's content expands past %d MiB in total", pdfMaxContentTotal>>20)
			}
			if pdfInlineImage.Match(sd.Content) {
				out[i] = true
				break
			}
		}
	}
	return out, nil
}

// pdfFinish rewrites the strings of the PDF PDFium wrote, repairs the forms
// it rewrote, drops what no rule knows, adds back the links of each page,
// and writes it again.
func pdfFinish(c context.Context, data []byte, rep *replacer, links [][]pdfLink, out *Output) (result []byte, err error) {
	defer func() {
		if r := recover(); r != nil {
			result, err = nil, fmt.Errorf("writing the PDF's objects: %v", r)
		}
	}()
	ctx, err := readPDFContext(c, data)
	if err != nil {
		return nil, err
	}
	if err := ctx.EnsurePageCount(); err != nil {
		return nil, fmt.Errorf("reading the PDF's pages: %w", err)
	}
	x := ctx.XRefTable
	if err := repairForms(x); err != nil {
		return nil, err
	}
	if err := blankImages(x); err != nil {
		return nil, err
	}
	dropped := map[string]bool{}
	dropPDFParts(x, dropped)
	walkPDFStrings(x, func(_, v string) (string, bool) {
		w := rep.Apply(v)
		return w, w != v
	})
	if err := addLinks(c, x, rep, links); err != nil {
		return nil, err
	}
	// pdfcpu finds an information dictionary, so it adds none dated now.
	// PDFium can write a trailer pointing to one it left out (from a file
	// updated incrementally, which has the dictionary twice): that one is
	// missing too.
	if x.Info != nil {
		if d, err := x.DereferenceDict(*x.Info); err != nil || d == nil {
			x.Info = nil
		}
	}
	if x.Info == nil {
		info := types.NewDict()
		info.InsertString("Producer", "exemplar")
		if x.Info, err = x.IndRefForNewObject(info); err != nil {
			return nil, fmt.Errorf("writing the PDF's objects: %w", err)
		}
	}
	var b bytes.Buffer
	if err := api.WriteContext(c, ctx, &b); err != nil {
		return nil, fmt.Errorf("writing the PDF's objects: %w", err)
	}
	written := b.Bytes()
	for name := range dropped {
		out.DroppedParts = append(out.DroppedParts, name)
	}
	sort.Strings(out.DroppedParts)
	return written, nil
}

// repairForms undoes what PDFium does to a form it rewrote: it writes the
// new content to a separate stream under the form's /Contents, which forms
// do not have, and leaves the old content (the text removed) in the form
// itself. The new content replaces the old.
func repairForms(x *model.XRefTable) error {
	total := 0
	for _, nr := range sortedObjects(x) {
		e := x.Table[nr]
		sd, ok := e.Object.(types.StreamDict)
		if !ok || sd.Subtype() == nil || *sd.Subtype() != "Form" {
			continue
		}
		contents, ok := sd.Find("Contents")
		if !ok {
			continue
		}
		refs := []types.Object{contents}
		if a, ok := contents.(types.Array); ok {
			refs = a
		}
		var content []byte
		for _, r := range refs {
			o, err := x.Dereference(r)
			if err != nil {
				return fmt.Errorf("repairing a form: %w", err)
			}
			cs, ok := o.(types.StreamDict)
			if !ok {
				return fmt.Errorf("repairing a form: its new content is a %T", o)
			}
			if err := cs.DecodeWithLimit(pdfMaxDecoded); err != nil {
				return fmt.Errorf("repairing a form: %w", err)
			}
			if len(content)+len(cs.Content)+1 > pdfMaxFormContent {
				return fmt.Errorf("repairing a form: its content expands past %d MiB in total", pdfMaxFormContent>>20)
			}
			content = append(content, cs.Content...)
			content = append(content, '\n')
		}
		total += len(content)
		if total > pdfMaxContentTotal {
			return fmt.Errorf("repairing the PDF's forms: their content expands past %d MiB in total", pdfMaxContentTotal>>20)
		}
		sd.Delete("Contents")
		sd.Content = content
		if err := sd.Encode(); err != nil {
			return fmt.Errorf("repairing a form: %w", err)
		}
		e.Object = sd
	}
	return nil
}

// pdfImageKeys are the entries of an image that say how its pixels are
// stored or masked: they go with the pixels.
var pdfImageKeys = []string{"Filter", "DecodeParms", "SMask", "Mask", "Decode", "ImageMask", "Alternates", "SMaskInData", "Interpolate", "Intent", "Metadata", "Name"}

// blankImages replaces every image the file holds with one grey pixel:
// those PDFium replaced as it drew them, and the ones no page draws, a
// page's thumbnail, a soft mask, a pattern's tile. Photos, signatures and
// scans say who a document is about as plainly as its text.
func blankImages(x *model.XRefTable) error {
	for _, nr := range sortedObjects(x) {
		e := x.Table[nr]
		sd, ok := e.Object.(types.StreamDict)
		if !ok || sd.Subtype() == nil || *sd.Subtype() != "Image" {
			continue
		}
		for _, k := range pdfImageKeys {
			sd.Delete(k)
		}
		sd.Update("Width", types.Integer(1))
		sd.Update("Height", types.Integer(1))
		sd.Update("ColorSpace", types.Name("DeviceGray"))
		sd.Update("BitsPerComponent", types.Integer(8))
		sd.FilterPipeline = nil
		sd.Content = []byte{0xd9}
		if err := sd.Encode(); err != nil {
			return fmt.Errorf("replacing an image: %w", err)
		}
		e.Object = sd
	}
	return nil
}

// dropPDFParts removes what no rule knows, noting each under its name, and
// the annotations flattening left (comments without an appearance, popups),
// keeping links to pages and to the web.
func dropPDFParts(x *model.XRefTable, dropped map[string]bool) {
	if root := x.RootDict; root != nil {
		for key, name := range pdfRootDrops {
			if root.Delete(key) != nil {
				dropped[name] = true
			}
		}
		if a, ok := root.Find("OpenAction"); ok && !safeAction(x, a) {
			root.Delete("OpenAction")
			dropped["JavaScript"] = true
		}
		if n, ok := root.Find("Names"); ok {
			if names, err := x.DereferenceDict(n); err == nil && names != nil {
				for key, name := range pdfNameDrops {
					if names.Delete(key) != nil {
						dropped[name] = true
					}
				}
			}
		}
	}
	for _, nr := range sortedObjects(x) {
		d, ok := dictOf(x.Table[nr].Object)
		if !ok {
			continue
		}
		for key, name := range pdfDrops {
			if d.Delete(key) != nil {
				dropped[name] = true
			}
		}
		if t := d.Type(); t != nil && *t == "Page" {
			dropAnnotations(x, d, dropped)
		}
	}
}

// dropAnnotations keeps a page's links that go to a page or a web address.
func dropAnnotations(x *model.XRefTable, page types.Dict, dropped map[string]bool) {
	annots, ok := page.Find("Annots")
	if !ok {
		return
	}
	arr, err := x.DereferenceArray(annots)
	if err != nil {
		page.Delete("Annots")
		dropped["annotations"] = true
		return
	}
	var keep types.Array
	for _, a := range arr {
		d, err := x.DereferenceDict(a)
		if err != nil || d == nil {
			continue
		}
		if st := d.Subtype(); st != nil && *st == "Link" {
			if act, ok := d.Find("A"); !ok || safeAction(x, act) {
				keep = append(keep, a)
				continue
			}
		}
		dropped["annotations"] = true
	}
	if len(keep) == 0 {
		page.Delete("Annots")
	} else {
		page.Update("Annots", keep)
	}
}

// addLinks adds each page's links, a web address rewritten like any other
// string.
func addLinks(c context.Context, x *model.XRefTable, rep *replacer, links [][]pdfLink) error {
	pageRef := func(i int) (types.Dict, *types.IndirectRef, error) {
		d, ref, _, err := x.PageDict(c, i+1, false)
		if err == nil && (d == nil || ref == nil) {
			err = fmt.Errorf("no page %d", i+1)
		}
		return d, ref, err
	}
	for i, page := range links {
		if len(page) == 0 {
			continue
		}
		d, _, err := pageRef(i)
		if err != nil {
			return fmt.Errorf("adding links: %w", err)
		}
		var annots types.Array
		if a, ok := d.Find("Annots"); ok {
			if annots, err = x.DereferenceArray(a); err != nil {
				return fmt.Errorf("adding links: %w", err)
			}
		}
		for _, l := range page {
			link := types.Dict{
				"Type":    types.Name("Annot"),
				"Subtype": types.Name("Link"),
				"Rect":    types.NewNumberArray(l.rect.left, l.rect.bottom, l.rect.right, l.rect.top),
				"Border":  types.NewIntegerArray(0, 0, 0),
			}
			if l.uri != "" {
				link["A"] = types.Dict{"S": types.Name("URI"), "URI": encodePDFString(rep.Apply(l.uri))}
			} else {
				_, target, err := pageRef(l.page)
				if err != nil {
					continue // a link to a page that is not there
				}
				dest := types.Array{*target, types.Name("Fit")}
				if l.x != nil && l.y != nil {
					dest = types.Array{*target, types.Name("XYZ"), types.Float(*l.x), types.Float(*l.y), types.Integer(0)}
				}
				link["Dest"] = dest
			}
			ref, err := x.IndRefForNewObject(link)
			if err != nil {
				return fmt.Errorf("adding links: %w", err)
			}
			annots = append(annots, *ref)
		}
		d.Update("Annots", annots)
	}
	return nil
}

// safeAction reports whether an action only goes somewhere: to a page, a
// named destination or a web address, with nothing chained after it.
func safeAction(x *model.XRefTable, a types.Object) bool {
	if _, ok := a.(types.Array); ok {
		return true // an explicit destination
	}
	d, err := x.DereferenceDict(a)
	if err != nil || d == nil {
		return false
	}
	if _, ok := d.Find("Next"); ok {
		return false
	}
	s := d.NameEntry("S")
	return s != nil && (*s == "GoTo" || *s == "URI")
}

// pdfStructuralKeys hold strings a reader looks things up by, not text about
// anyone: a CID font's registry and ordering pick its character maps, a
// language tag, a font's list of glyph names, a file identifier. They are
// neither searched nor rewritten.
var pdfStructuralKeys = map[string]bool{
	"Registry": true, "Ordering": true, "Lang": true, "CharSet": true, "ID": true,
}

// walkPDFStrings calls fn with every string outside the drawing and the
// key it sits under, and puts back the strings fn changes. Strings under
// pdfStructuralKeys are left alone.
func walkPDFStrings(x *model.XRefTable, fn func(key, v string) (string, bool)) {
	for _, nr := range sortedObjects(x) {
		e := x.Table[nr]
		if o, ok := rewritePDFObject(e.Object, "", fn, 0); ok {
			e.Object = o
		}
	}
}

// rewritePDFObject walks o, calling fn on its strings, and returns o with
// the changed ones, or false when nothing changed. Indirect objects are
// walked once, as entries of the table, not where they are referred to.
func rewritePDFObject(o types.Object, key string, fn func(key, v string) (string, bool), depth int) (types.Object, bool) {
	if depth > 64 {
		return o, false
	}
	switch v := o.(type) {
	case types.StringLiteral, types.HexLiteral:
		s, err := types.StringOrHexLiteral(v)
		if err != nil {
			return o, false
		}
		w, changed := fn(key, *s)
		if !changed {
			return o, false
		}
		return encodePDFString(w), true
	case types.Dict:
		changed := false
		for k, item := range v {
			if pdfStructuralKeys[k] {
				continue
			}
			if n, ok := rewritePDFObject(item, k, fn, depth+1); ok {
				v[k], changed = n, true
			}
		}
		return v, changed
	case types.StreamDict:
		_, changed := rewritePDFObject(v.Dict, key, fn, depth+1)
		return v, changed
	case types.Array:
		changed := false
		for i, item := range v {
			if n, ok := rewritePDFObject(item, key, fn, depth+1); ok {
				v[i], changed = n, true
			}
		}
		return v, changed
	}
	return o, false
}

// encodePDFString writes s as a PDF string: a literal when it is ASCII,
// UTF-16 otherwise.
func encodePDFString(s string) types.Object {
	ascii := true
	for _, r := range s {
		ascii = ascii && r >= 0x20 && r < 0x7f
	}
	if ascii {
		if e, err := types.Escape(s); err == nil {
			return types.StringLiteral(*e)
		}
	}
	return types.NewHexLiteral([]byte(types.EncodeUTF16String(s)))
}

func dictOf(o types.Object) (types.Dict, bool) {
	switch v := o.(type) {
	case types.Dict:
		return v, true
	case types.StreamDict:
		return v.Dict, true
	}
	return nil, false
}

func sortedObjects(x *model.XRefTable) []int {
	nrs := make([]int, 0, len(x.Table))
	for nr, e := range x.Table {
		if e != nil && !e.Free && e.Object != nil {
			nrs = append(nrs, nr)
		}
	}
	sort.Ints(nrs)
	return nrs
}
