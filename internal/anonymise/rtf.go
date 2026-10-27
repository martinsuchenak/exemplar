package anonymise

import (
	"bytes"
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"unicode/utf16"

	"github.com/martinsuchenak/exemplar/internal/mimetree"
)

// RTF is one stream of groups, control words and text. The handler
// tokenises it, decodes the text of every destination that holds text, and
// rewrites only text that changes; every other byte stays as it was.
// Pictures are replaced, embedded object data and the data store dropped.

type rtfKind int

const (
	rtfOpen   rtfKind = iota // {
	rtfClose                 // }
	rtfWord                  // \word, \wordN
	rtfSymbol                // \x for any other character x
	rtfHex                   // \'xx
	rtfText                  // plain bytes
	rtfBin                   // the data after \binN
)

type rtfTok struct {
	kind       rtfKind
	start, end int
	word       string
	param      int
	hasParam   bool
	// spaced is a word whose delimiting space is part of the token.
	spaced bool
	sym    byte
	hex    byte
}

// rtfMaxDepth caps group nesting.
const rtfMaxDepth = 2000

// tokenizeRTF splits an RTF file into tokens. It refuses anything that does
// not start as RTF, unbalanced groups and broken escapes.
func tokenizeRTF(data []byte) ([]rtfTok, error) {
	if !bytes.HasPrefix(bytes.TrimLeft(data, " \t\r\n"), []byte(`{\rtf`)) {
		return nil, errors.New(`not an RTF file: it does not start with {\rtf`)
	}
	var toks []rtfTok
	depth := 0
	for i := 0; i < len(data); {
		start := i
		switch c := data[i]; c {
		case '{':
			if depth++; depth > rtfMaxDepth {
				return nil, fmt.Errorf("groups nest more than %d deep", rtfMaxDepth)
			}
			toks = append(toks, rtfTok{kind: rtfOpen, start: i, end: i + 1})
			i++
		case '}':
			if depth--; depth < 0 {
				return nil, fmt.Errorf("a } at byte %d closes no group", i)
			}
			toks = append(toks, rtfTok{kind: rtfClose, start: i, end: i + 1})
			i++
		case '\\':
			if i+1 >= len(data) {
				return nil, errors.New("the file ends in a backslash")
			}
			n := data[i+1]
			switch {
			case isLetter(n):
				j := i + 1
				for j < len(data) && isLetter(data[j]) && j-i <= 32 {
					j++
				}
				t := rtfTok{kind: rtfWord, start: i, word: string(data[i+1 : j])}
				k := j
				if k < len(data) && data[k] == '-' {
					k++
				}
				d := k
				for d < len(data) && data[d] >= '0' && data[d] <= '9' && d-k < 10 {
					d++
				}
				if d > k {
					p, err := strconv.Atoi(string(data[j:d]))
					if err != nil {
						return nil, fmt.Errorf("a bad number after \\%s at byte %d", t.word, i)
					}
					t.param, t.hasParam, j = p, true, d
				}
				if j < len(data) && data[j] == ' ' {
					t.spaced = true
					j++
				}
				t.end = j
				toks = append(toks, t)
				i = j
				if t.word == "bin" && t.hasParam {
					if t.param < 0 || t.param > len(data)-i {
						return nil, fmt.Errorf("\\bin%d at byte %d runs past the end", t.param, start)
					}
					toks = append(toks, rtfTok{kind: rtfBin, start: i, end: i + t.param})
					i += t.param
				}
			case n == '\'':
				if i+4 > len(data) {
					return nil, fmt.Errorf("a cut-off \\' escape at byte %d", i)
				}
				b, err := hex.DecodeString(string(data[i+2 : i+4]))
				if err != nil || len(b) != 1 {
					return nil, fmt.Errorf("a bad \\' escape at byte %d", i)
				}
				toks = append(toks, rtfTok{kind: rtfHex, start: i, end: i + 4, hex: b[0]})
				i += 4
			case n == '\r' || n == '\n':
				// A backslash before a line break is a paragraph mark.
				toks = append(toks, rtfTok{kind: rtfWord, start: i, end: i + 2, word: "par"})
				i += 2
			default:
				toks = append(toks, rtfTok{kind: rtfSymbol, start: i, end: i + 2, sym: n})
				i += 2
			}
		default:
			j := i
			for j < len(data) && data[j] != '{' && data[j] != '}' && data[j] != '\\' {
				j++
			}
			toks = append(toks, rtfTok{kind: rtfText, start: i, end: j})
			i = j
		}
	}
	if depth != 0 {
		return nil, errors.New("a group is never closed")
	}
	return toks, nil
}

func isLetter(c byte) bool {
	return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z'
}

// rtfChars are the control words that stand for a character.
var rtfChars = map[string]rune{
	"emdash": '—', "endash": '–', "emspace": ' ', "enspace": ' ', "qmspace": ' ', "bullet": '•',
	"lquote": '‘', "rquote": '’', "ldblquote": '“', "rdblquote": '”',
}

// rtfBreaks end a run of text: no name is matched across a paragraph,
// line, tab or cell.
var rtfBreaks = words("par", "sect", "page", "line", "tab", "cell", "row", "nestcell", "nestrow", "column", "lbr", "softline", "softpage", "chftn", "chatn")

// words is a set of RTF control words or property names, which are
// case-sensitive (set lower-cases).
func words(ws ...string) map[string]bool { return namespaces(ws...) }

// rtfSkip are the destinations that hold no text: tables of fonts, colours,
// styles and lists, and machine data.
var rtfSkip = words(
	"fonttbl", "colortbl", "stylesheet", "listtable", "listoverridetable", "rsidtbl", "generator", "xmlnstbl",
	"themedata", "colorschememapping", "latentstyles", "mmathPr", "pgdsctbl", "filetbl", "falt", "panose",
	"listtext", "pntext", "pntxta", "pntxtb", "defchp", "defpap", "wgrffmtfilter", "passwordhash", "ftnsep",
	"ftnsepc", "aftnsep", "aftnsepc", "bkmkstart", "bkmkend", "fldtype", "atnref", "atndate", "atnparent",
	"blipuid", "propname", "creatim", "revtim", "printim", "buptim", "version", "vern", "edmins", "nofpages",
	"nofwords", "nofchars", "nofcharsws", "id",
)

// rtfTextDests are destinations whose text is document text: notes,
// headers and footers, field results, comments, shape text.
var rtfTextDests = words(
	"footnote", "header", "headerl", "headerr", "headerf", "footer", "footerl", "footerr", "footerf",
	"fldrslt", "annotation", "shptxt", "shprslt", "docvar", "nonesttables", "xe", "tc", "txe",
)

// rtfInfo are the fields of \info that hold text, with what they are.
var rtfInfo = map[string]string{
	"title": "", "subject": "", "keywords": "", "doccomm": "", "category": "",
	"author": EntityPerson, "operator": EntityPerson, "manager": EntityPerson, "company": EntityCompany,
}

// rtfShapeText are the shape properties whose value is text.
var rtfShapeText = words("wzDescription", "wzName", "wzTooltip", "gtextUNICODE", "wzSigSetupSuggSigner")

type rtfMode int

const (
	rtfModeInherit   rtfMode = iota // a formatting group
	rtfModeSkip                     // nothing to read
	rtfModeContainer                // its own text is not read; its groups are
	rtfModeText                     // text, in runs
	rtfModeField                    // one value (an info field, a field instruction, a name)
	rtfModeInfo                     // \info
	rtfModeRevtbl                   // the revision authors, each in a group
	rtfModeSP                       // a shape property: \sn name, \sv value
	rtfModePict                     // a picture
	rtfModeDrop                     // removed whole
)

// rtfSink collects the text of one destination into units.
type rtfSink struct {
	entity   string
	fake     func(context.Context, *Engine, string) (string, error)
	single   bool // the whole destination is one unit
	initials bool // \atnid: initials of the comment's author
	cur      []rtfPiece
}

// rtfPiece is a run of text tokens: its bytes and decoded value.
type rtfPiece struct {
	start, end int
	value      string
	// uc is how many fallback characters follow a \u at this point.
	uc int
	// afterWord: the piece follows a control word with no delimiting space.
	afterWord bool
}

type rtfUnit struct {
	pieces     []rtfPiece
	entity     string
	fake       func(context.Context, *Engine, string) (string, error)
	initialsOf int
}

func (u *rtfUnit) text() string {
	var b strings.Builder
	for _, p := range u.pieces {
		b.WriteString(p.value)
	}
	return b.String()
}

// rtfPict is a picture: its group, its format and its data tokens.
type rtfPict struct {
	open int // its { token
	blip string
	data []int
	bin  bool
}

// rtfDoc is a parsed RTF file.
type rtfDoc struct {
	data  []byte
	toks  []rtfTok
	units []rtfUnit
	picts []rtfPict
	// drops are groups removed whole, by their open token, with what they
	// were.
	drops map[int]string
}

type rtfGroup struct {
	mode   rtfMode
	sink   *rtfSink
	uc     int
	first  bool // no control word seen yet
	star   bool // started with \*
	open   int  // its { token
	spName string
	isName bool // an \sn: its text names the parent \sp
	pict   int  // index into picts, or -1
	// lastAuthor and pendingInitials pair \atnauthor with \atnid.
	lastAuthor, pendingInitials int
}

// parseRTF tokenises an RTF file and finds every value to search.
func parseRTF(data []byte) (*rtfDoc, error) {
	toks, err := tokenizeRTF(data)
	if err != nil {
		return nil, err
	}
	d := &rtfDoc{data: data, toks: toks, drops: map[int]string{}}
	root := &rtfSink{}
	var stack []*rtfGroup
	top := func() *rtfGroup { return stack[len(stack)-1] }
	pieceOpen := false
	skip := 0     // fallback characters still to skip after a \u
	var high rune // the first half of a surrogate pair, from the last \u
	var prev *rtfTok

	addPiece := func(g *rtfGroup, t *rtfTok, value string) {
		if g.sink == nil {
			return
		}
		s := g.sink
		if pieceOpen && len(s.cur) > 0 {
			p := &s.cur[len(s.cur)-1]
			p.end, p.value = t.end, p.value+value
			return
		}
		after := prev != nil && prev.kind == rtfWord && !prev.spaced
		s.cur = append(s.cur, rtfPiece{start: t.start, end: t.end, value: value, uc: g.uc, afterWord: after})
		pieceOpen = true
	}
	flush := func(s *rtfSink) int {
		if s == nil || len(s.cur) == 0 {
			return -1
		}
		d.units = append(d.units, rtfUnit{pieces: s.cur, entity: s.entity, fake: s.fake, initialsOf: -1})
		s.cur = nil
		return len(d.units) - 1
	}

	for i := range toks {
		t := &toks[i]
		if len(stack) == 0 && t.kind != rtfOpen {
			continue // after the document's last }: whitespace, a NUL
		}
		switch t.kind {
		case rtfOpen:
			g := &rtfGroup{first: true, open: i, pict: -1, lastAuthor: -1, pendingInitials: -1}
			if len(stack) > 0 {
				p := top()
				g.mode, g.sink, g.uc = p.mode, p.sink, p.uc
				switch p.mode {
				case rtfModeSkip, rtfModeDrop, rtfModePict:
					g.mode, g.sink = rtfModeSkip, nil
				case rtfModeRevtbl:
					// Each entry of the revision table is an author.
					g.mode, g.sink = rtfModeField, &rtfSink{entity: EntityPerson, single: true}
				case rtfModeInfo, rtfModeContainer, rtfModeSP:
					g.mode, g.sink = rtfModeContainer, nil
				}
			} else {
				g.mode, g.sink, g.uc = rtfModeText, root, 1
			}
			stack = append(stack, g)
			pieceOpen, skip, high = false, 0, 0
		case rtfClose:
			g := top()
			stack = stack[:len(stack)-1]
			if g.isName && len(stack) > 0 {
				var b strings.Builder
				for _, p := range g.sink.cur {
					b.WriteString(p.value)
				}
				top().spName = strings.TrimSpace(b.String())
			} else if len(stack) > 0 && g.sink != top().sink {
				u := flush(g.sink)
				if u >= 0 && g.sink.entity == EntityPerson && g.sink.single {
					p := top()
					if p.pendingInitials >= 0 {
						d.units[p.pendingInitials].initialsOf = u
						p.pendingInitials = -1
					}
					p.lastAuthor = u
				}
				if u >= 0 && g.sink.initials {
					if p := top(); p.lastAuthor >= 0 {
						d.units[u].initialsOf = p.lastAuthor
					} else {
						p.pendingInitials = u
					}
				}
			}
			pieceOpen, skip, high = false, 0, 0
		case rtfSymbol:
			g := top()
			if t.sym == '*' && g.first {
				g.star = true
				continue
			}
			if g.mode == rtfModePict || g.sink == nil {
				continue
			}
			switch t.sym {
			case '\\', '{', '}':
				addPiece(g, t, string(t.sym))
			case '~':
				addPiece(g, t, " ")
			case '_':
				addPiece(g, t, "‑")
			case '-':
				addPiece(g, t, "") // an optional hyphen
			default:
				pieceOpen = false
			}
		case rtfHex:
			g := top()
			if skip > 0 {
				skip--
				addPiece(g, t, "")
				continue
			}
			addPiece(g, t, string(mimetree.Rune1252(t.hex)))
		case rtfText:
			g := top()
			if g.mode == rtfModePict {
				if g.pict >= 0 {
					d.picts[g.pict].data = append(d.picts[g.pict].data, i)
				}
				continue
			}
			var b strings.Builder
			for _, c := range d.data[t.start:t.end] {
				switch {
				case c == '\r' || c == '\n':
				case skip > 0:
					skip--
				default:
					b.WriteRune(mimetree.Rune1252(c))
				}
			}
			addPiece(top(), t, b.String())
		case rtfBin:
			if g := top(); g.pict >= 0 {
				d.picts[g.pict].bin = true
			}
		case rtfWord:
			g := top()
			if g.first {
				g.first = false
				if err := d.enter(g, stack, t, i); err != nil {
					return nil, err
				}
				if g.mode != rtfModeInherit {
					// A destination of its own: the text around it is not
					// joined to its text.
					if len(stack) > 1 && stack[len(stack)-2].sink != nil && stack[len(stack)-2].sink != g.sink {
						flush(stack[len(stack)-2].sink)
					}
					prev, pieceOpen = t, false
					continue
				}
				g.mode = stack[max(len(stack)-2, 0)].mode
			}
			switch {
			case t.word == "uc" && t.hasParam:
				g.uc = t.param
				pieceOpen = false
			case t.word == "u" && t.hasParam:
				r := rune(t.param)
				if r < 0 {
					r += 65536
				}
				// A character past U+FFFF comes as two \u, a surrogate pair.
				switch {
				case utf16.IsSurrogate(r) && r < 0xdc00:
					high = r
					addPiece(g, t, "")
				case utf16.IsSurrogate(r) && high != 0:
					addPiece(g, t, string(utf16.DecodeRune(high, r)))
					high = 0
				default:
					addPiece(g, t, string(r))
					high = 0
				}
				skip = g.uc
			case rtfChars[t.word] != 0:
				addPiece(g, t, string(rtfChars[t.word]))
			case g.mode == rtfModePict && strings.HasSuffix(t.word, "blip") || g.mode == rtfModePict && (t.word == "wmetafile" || t.word == "dibitmap" || t.word == "wbitmap" || t.word == "macpict" || t.word == "pmmetafile"):
				if g.pict >= 0 && d.picts[g.pict].blip == "" {
					d.picts[g.pict].blip = t.word
				}
			case rtfBreaks[t.word]:
				if g.sink != nil && !g.sink.single {
					flush(g.sink)
				}
				pieceOpen = false
			case t.word == "ansicpg" && t.hasParam && t.param != 1252:
				return nil, fmt.Errorf("RTF code page %d is not supported; only 1252 is", t.param)
			case t.word == "mac" || t.word == "pc" || t.word == "pca":
				return nil, fmt.Errorf("the %s character set is not supported; only ANSI (1252) is", t.word)
			default:
				pieceOpen = false
			}
		}
		prev = t
	}
	flush(root)
	return d, nil
}

// enter decides what the group g, whose first control word is t, holds.
func (d *rtfDoc) enter(g *rtfGroup, stack []*rtfGroup, t *rtfTok, i int) error {
	parent := rtfModeText
	var parentGroup *rtfGroup
	if len(stack) > 1 {
		parentGroup = stack[len(stack)-2]
		parent = parentGroup.mode
	}
	w := t.word
	field := func(entity string, fake func(context.Context, *Engine, string) (string, error)) {
		g.mode, g.sink = rtfModeField, &rtfSink{entity: entity, fake: fake, single: true}
	}
	switch {
	case len(stack) == 1:
		g.mode = rtfModeText // the document: {\rtf1 …
	case g.mode == rtfModeSkip || g.mode == rtfModeDrop:
	case parent == rtfModePict:
		g.mode, g.sink = rtfModeSkip, nil
	case w == "pict":
		g.mode, g.sink = rtfModePict, nil
		g.pict = len(d.picts)
		d.picts = append(d.picts, rtfPict{open: g.open})
	case w == "objdata":
		g.mode, g.sink = rtfModeDrop, nil
		d.drops[g.open] = "embedded object data"
	case w == "datastore":
		g.mode, g.sink = rtfModeDrop, nil
		d.drops[g.open] = "data store"
	case parent == rtfModeInfo:
		if entity, ok := rtfInfo[w]; ok {
			field(entity, nil)
		} else if w == "hlinkbase" {
			field("", fakeTarget)
		} else {
			g.mode, g.sink = rtfModeSkip, nil
		}
	case parent == rtfModeSP && w == "sn":
		g.mode, g.sink, g.isName = rtfModeField, &rtfSink{single: true}, true
	case parent == rtfModeSP && w == "sv":
		if rtfShapeText[parentGroup.spName] {
			field("", nil)
		} else {
			g.mode, g.sink = rtfModeContainer, nil // may hold a picture
		}
	case w == "info":
		g.mode, g.sink = rtfModeInfo, nil
	case w == "revtbl" || w == "protusertbl":
		g.mode, g.sink = rtfModeRevtbl, nil
	case w == "fldinst":
		field("", nil)
	case w == "atnauthor":
		field(EntityPerson, nil)
	case w == "atnid":
		field("", nil)
		g.sink.initials = true
	case w == "template":
		field("", fakeTarget)
	case w == "staticval":
		field("", nil)
	case w == "sp":
		g.mode, g.sink = rtfModeSP, nil
	case w == "shp" || w == "shpgrp" || w == "shpinst" || w == "object" || w == "result" || w == "userprops" || w == "shppict" || w == "nonshppict":
		g.mode, g.sink = rtfModeContainer, nil
	case rtfTextDests[w]:
		g.mode, g.sink = rtfModeText, &rtfSink{}
	case rtfSkip[w] || g.star:
		g.mode, g.sink = rtfModeSkip, nil
	default:
		g.mode = rtfModeInherit // a formatting group: {\b Jane}
		return nil
	}
	return nil
}

func (d *rtfDoc) texts() []docText {
	var out []docText
	for i := range d.units {
		u := &d.units[i]
		if u.fake != nil || u.initialsOf >= 0 {
			continue
		}
		if v := u.text(); strings.TrimSpace(v) != "" && v != "Unknown;" && v != "Unknown" {
			out = append(out, docText{value: v, entity: u.entity})
		}
	}
	return out
}

// rtfHandler reads and rewrites RTF files.
type rtfHandler struct{}

func (rtfHandler) prepare(ctx context.Context, e *Engine, file File) error {
	d, err := parseRTF(file.Data)
	if err != nil {
		return err
	}
	return e.learnNames(ctx, d.texts())
}

func (rtfHandler) anonymise(ctx context.Context, e *Engine, file File) (*Output, error) {
	d, err := parseRTF(file.Data)
	if err != nil {
		return nil, err
	}
	pairs, f, rep, err := e.docReplacements(ctx, d.texts())
	if err != nil {
		return nil, err
	}
	out := &Output{Replacements: pairs, Entities: f.entities}
	if out.Data, err = d.rewrite(ctx, e, rep, out); err != nil {
		return nil, err
	}
	again, err := tokenizeRTF(out.Data)
	if err != nil {
		return nil, fmt.Errorf("anonymised output is not valid RTF: %w", err)
	}
	if rtfSkeleton(out.Data, again, nil) != rtfSkeleton(d.data, d.toks, d.drops) {
		return nil, errors.New("anonymising changed the file's structure")
	}
	return out, nil
}

func (d *rtfDoc) rewrite(ctx context.Context, e *Engine, rep *replacer, out *Output) ([]byte, error) {
	var edits []xmlEdit
	newText := make([]string, len(d.units))
	// Initials go second: Word writes \atnid before the \atnauthor whose
	// fake they follow.
	order := make([]int, 0, len(d.units))
	for i := range d.units {
		if d.units[i].initialsOf < 0 {
			order = append(order, i)
		}
	}
	for i := range d.units {
		if d.units[i].initialsOf >= 0 {
			order = append(order, i)
		}
	}
	for _, i := range order {
		u := &d.units[i]
		old := u.text()
		var values []string
		switch {
		case u.initialsOf >= 0:
			if newText[u.initialsOf] == d.units[u.initialsOf].text() {
				newText[i] = old
				continue
			}
			values = []string{initials(newText[u.initialsOf])}
		case u.fake != nil:
			v, err := u.fake(ctx, e, old)
			if err != nil {
				return nil, err
			}
			values = []string{v}
		default:
			pieces := make([]string, len(u.pieces))
			for j, p := range u.pieces {
				pieces[j] = p.value
			}
			values = distribute(pieces, rep.Matches(old))
		}
		if len(values) != len(u.pieces) {
			// A one-value unit split across pieces: the value goes in the
			// first, the others are emptied.
			values = append(values, make([]string, len(u.pieces)-len(values))...)
		}
		newText[i] = strings.Join(values, "")
		for j, p := range u.pieces {
			if values[j] == p.value {
				continue
			}
			edits = append(edits, xmlEdit{start: p.start, end: p.end, with: rtfEncode(values[j], p.uc, p.afterWord)})
		}
	}

	// Pictures: a PNG or JPEG is replaced by a generated one; any other
	// picture, and embedded object data and the data store, are dropped.
	people := e.fillerPeople()
	inDrop := func(tok int) bool {
		for open := range d.drops {
			if tok > open && tok < closeIndex(d.toks, open) {
				return true
			}
		}
		return false
	}
	for n, p := range d.picts {
		if inDrop(p.open) {
			continue
		}
		mediaType := map[string]string{"pngblip": "image/png", "jpegblip": "image/jpeg"}[p.blip]
		if mediaType == "" || p.bin || len(p.data) == 0 {
			d.drops[p.open] = fmt.Sprintf("picture %d (%s)", n+1, strings.TrimSuffix(orDefault(p.blip, "unknown"), "blip"))
			continue
		}
		size := 0
		for _, ti := range p.data {
			size += (d.toks[ti].end - d.toks[ti].start) / 2
		}
		img := e.placeholder(mediaType, "picture", size, people)
		for k, ti := range p.data {
			with := ""
			if k == 0 {
				with = hexLines(img)
			}
			edits = append(edits, xmlEdit{start: d.toks[ti].start, end: d.toks[ti].end, with: with})
		}
		out.Attachments++
	}
	var dropped []int
	for open := range d.drops {
		if !inDrop(open) {
			dropped = append(dropped, open)
		}
	}
	sort.Ints(dropped)
	for _, open := range dropped {
		// Edits inside a dropped group give way to it.
		start, end := d.toks[open].start, d.toks[closeIndex(d.toks, open)].end
		kept := edits[:0]
		for _, ed := range edits {
			if ed.start >= end || ed.end <= start {
				kept = append(kept, ed)
			}
		}
		edits = append(kept, xmlEdit{start: start, end: end})
		out.DroppedParts = append(out.DroppedParts, d.drops[open])
	}
	return splice(d.data, edits)
}

// rtfEncode writes text as RTF: printable ASCII as it is, with \, { and }
// escaped; other Windows-1252 characters as \'xx; anything else as \uN
// followed by uc fallback characters. After a control word with no
// delimiting space, a space comes first, so the text cannot run into it.
func rtfEncode(s string, uc int, afterWord bool) string {
	var b strings.Builder
	if afterWord && s != "" {
		b.WriteByte(' ')
	}
	for _, r := range s {
		switch {
		case r == '\\' || r == '{' || r == '}':
			b.WriteByte('\\')
			b.WriteRune(r)
		case r >= 0x20 && r < 0x7f:
			b.WriteRune(r)
		case r < 0x20:
		default:
			if c, ok := encode1252[r]; ok {
				fmt.Fprintf(&b, `\'%02x`, c)
				continue
			}
			for _, u := range utf16.Encode([]rune{r}) {
				fmt.Fprintf(&b, `\u%d`, int16(u))
				if uc > 0 {
					b.WriteString(strings.Repeat("?", uc))
				} else {
					b.WriteByte(' ')
				}
			}
		}
	}
	return b.String()
}

// encode1252 maps characters back to their Windows-1252 bytes.
var encode1252 = func() map[rune]byte {
	m := map[rune]byte{}
	for c := 0x80; c <= 0xff; c++ {
		if r := mimetree.Rune1252(byte(c)); r != rune(c) || c >= 0xa0 {
			m[r] = byte(c)
		}
	}
	return m
}()

// hexLines writes picture data as hex, 128 digits a line, as Word does.
func hexLines(data []byte) string {
	h := hex.EncodeToString(data)
	var b strings.Builder
	for i := 0; i < len(h); i += 128 {
		b.WriteString("\r\n")
		b.WriteString(h[i:min(i+128, len(h))])
	}
	return b.String()
}

// rtfSkeleton is a file's structure without its text: every group and
// control word in order, leaving out characters, pictures (replaced) and
// the groups in drops (removed).
func rtfSkeleton(data []byte, toks []rtfTok, drops map[int]string) string {
	var b strings.Builder
	for i := 0; i < len(toks); i++ {
		t := &toks[i]
		if t.kind == rtfOpen {
			if _, ok := drops[i]; ok || isPict(toks, i) {
				i = closeIndex(toks, i)
				continue
			}
		}
		switch t.kind {
		case rtfOpen:
			b.WriteString("{")
		case rtfClose:
			b.WriteString("}")
		case rtfWord:
			if t.word == "u" || rtfChars[t.word] != 0 {
				continue
			}
			b.WriteString(`\` + t.word)
			if t.hasParam {
				b.WriteString(strconv.Itoa(t.param))
			}
			b.WriteByte(' ')
		case rtfSymbol:
			if !strings.ContainsRune(`\{}~_-`, rune(t.sym)) {
				b.WriteString(`\` + string(t.sym))
			}
		}
	}
	return b.String()
}

// isPict reports whether the group at open is a picture.
func isPict(toks []rtfTok, open int) bool {
	i := open + 1
	if i < len(toks) && toks[i].kind == rtfSymbol && toks[i].sym == '*' {
		i++
	}
	return i < len(toks) && toks[i].kind == rtfWord && toks[i].word == "pict"
}

// closeIndex returns the index of the } that closes the { at open.
func closeIndex(toks []rtfTok, open int) int {
	depth := 0
	for i := open; i < len(toks); i++ {
		switch toks[i].kind {
		case rtfOpen:
			depth++
		case rtfClose:
			if depth--; depth == 0 {
				return i
			}
		}
	}
	return len(toks) - 1
}

func orDefault(s, d string) string {
	if s == "" {
		return d
	}
	return s
}
