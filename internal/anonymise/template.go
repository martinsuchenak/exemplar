package anonymise

import (
	"fmt"
	"path"
	"regexp"
	"strings"
)

// tplDoc is a Twig or Jinja template, which must still compile after
// anonymising. It is split into literal text, comments and tags. Template
// code is not personal data ({{ customer.name }} is a variable), so a tag is
// left as written except inside its quoted strings, where real data can sit
// ({% set signer = "Jane Doe" %}). Literal text is HTML when the template
// renders HTML (page.html.twig) and plain text otherwise.
type tplDoc struct {
	text string
	html bool
	segs []tplSeg
}

type tplKind int

const (
	tplLiteral tplKind = iota
	tplComment
	tplTag
)

// tplSeg is one segment: for a comment, start and end are its text inside
// {# #}; for a tag, strs are the contents of its quoted strings.
type tplSeg struct {
	kind       tplKind
	start, end int
	strs       []tplStr
}

type tplStr struct {
	start, end int
	quote      byte
}

// templateRenders are the extensions a template's name may carry before
// its own (page.html.twig), with whether its literal text is HTML.
var templateRenders = map[string]bool{".html": true, ".htm": true, ".xml": true, ".txt": false, ".md": false}

// rawBlockRe matches the start of a block whose body is literal text:
// Twig's verbatim and Jinja's raw.
var rawBlockRe = regexp.MustCompile(`^\{%[-~]?\s*(verbatim|raw)\s*[-~]?%\}`)

func parseTemplate(name, text string) (doc, error) {
	lower := strings.ToLower(name)
	inner := path.Ext(strings.TrimSuffix(lower, path.Ext(lower)))
	d := &tplDoc{text: text, html: templateRenders[inner]}
	for i := 0; i < len(text); {
		k := nextTag(text, i)
		if k < 0 {
			k = len(text)
		}
		if k > i {
			d.segs = append(d.segs, tplSeg{kind: tplLiteral, start: i, end: k})
		}
		if k == len(text) {
			break
		}
		next, err := d.readTag(k)
		if err != nil {
			return nil, fmt.Errorf("line %d: %w", strings.Count(text[:k], "\n")+1, err)
		}
		i = next
	}
	return d, nil
}

// nextTag returns where the next {{, {% or {# starts at or after i.
func nextTag(text string, i int) int {
	for {
		j := strings.IndexByte(text[i:], '{')
		if j < 0 || i+j+1 >= len(text) {
			return -1
		}
		if c := text[i+j+1]; c == '{' || c == '%' || c == '#' {
			return i + j
		}
		i += j + 1
	}
}

// readTag reads the tag or comment at k and returns where it ends.
func (d *tplDoc) readTag(k int) (int, error) {
	text := d.text
	if text[k+1] == '#' {
		end := strings.Index(text[k+2:], "#}")
		if end < 0 {
			return 0, fmt.Errorf("a comment is never closed")
		}
		d.segs = append(d.segs, tplSeg{kind: tplComment, start: k + 2, end: k + 2 + end})
		return k + 2 + end + 2, nil
	}
	closer := "}}"
	if text[k+1] == '%' {
		closer = "%}"
	}
	seg := tplSeg{kind: tplTag, start: k}
	j := k + 2
	for {
		if j >= len(text) {
			return 0, fmt.Errorf("a tag is never closed")
		}
		if strings.HasPrefix(text[j:], closer) {
			break
		}
		if q := text[j]; q == '"' || q == '\'' {
			s := tplStr{start: j + 1, quote: q}
			j++
			for j < len(text) && text[j] != q {
				if text[j] == '\\' {
					j++
				}
				j++
			}
			if j >= len(text) {
				return 0, fmt.Errorf("a string in a tag is never closed")
			}
			s.end = j
			seg.strs = append(seg.strs, s)
		}
		j++
	}
	seg.end = j + 2
	d.segs = append(d.segs, seg)
	// The body of a verbatim or raw block is literal text up to its end
	// tag, whatever it holds.
	if m := rawBlockRe.FindStringSubmatch(text[k:seg.end]); m != nil {
		endRe := regexp.MustCompile(`\{%[-~]?\s*end` + m[1] + `\s*[-~]?%\}`)
		loc := endRe.FindStringIndex(text[seg.end:])
		if loc == nil {
			return 0, fmt.Errorf("{%% %s %%} is never ended", m[1])
		}
		if loc[0] > 0 {
			d.segs = append(d.segs, tplSeg{kind: tplLiteral, start: seg.end, end: seg.end + loc[0]})
		}
		return seg.end + loc[0], nil
	}
	return seg.end, nil
}

// texts returns each literal (as page text when it is HTML), each comment
// and each string in a tag, on its own: nothing is matched across a tag.
func (d *tplDoc) texts() []docText {
	var out []docText
	for _, s := range d.segs {
		switch s.kind {
		case tplLiteral:
			v := d.text[s.start:s.end]
			if d.html {
				v = htmlPageText(v)
			}
			out = append(out, docText{value: v})
		case tplComment:
			out = append(out, docText{value: d.text[s.start:s.end]})
		case tplTag:
			for _, str := range s.strs {
				for _, part := range outsideInterpolation(d.text[str.start:str.end]) {
					out = append(out, docText{value: part})
				}
			}
		}
	}
	return out
}

func (d *tplDoc) rewrite(rep *replacer) (string, error) {
	var b strings.Builder
	last := 0
	put := func(start, end int, v string) {
		b.WriteString(d.text[last:start])
		b.WriteString(v)
		last = end
	}
	for _, s := range d.segs {
		switch s.kind {
		case tplLiteral:
			if d.html {
				put(s.start, s.end, applyHTML(d.text[s.start:s.end], rep))
			} else {
				put(s.start, s.end, rep.Apply(d.text[s.start:s.end]))
			}
		case tplComment:
			put(s.start, s.end, rep.Apply(d.text[s.start:s.end]))
		case tplTag:
			for _, str := range s.strs {
				old := d.text[str.start:str.end]
				if v := rewriteTplString(old, rep, str.quote); v != old {
					put(str.start, str.end, v)
				}
			}
		}
	}
	b.WriteString(d.text[last:])
	return b.String(), nil
}

// shape is every tag as written, with the contents of its strings left out,
// and the order of the segments.
func (d *tplDoc) shape() string {
	var b strings.Builder
	for _, s := range d.segs {
		switch s.kind {
		case tplLiteral:
			b.WriteString("L")
		case tplComment:
			b.WriteString("C")
		case tplTag:
			at := s.start
			for _, str := range s.strs {
				b.WriteString(d.text[at:str.start])
				at = str.end
			}
			b.WriteString(d.text[at:s.end])
		}
		b.WriteByte(0)
	}
	return b.String()
}

// interpolationRe matches Twig's #{…} inside a double-quoted string: code.
var interpolationRe = regexp.MustCompile(`#\{[^}]*\}`)

// outsideInterpolation returns the parts of a string that are text.
func outsideInterpolation(s string) []string {
	return interpolationRe.Split(s, -1)
}

// rewriteTplString applies rep to a tag's string outside any #{…}, and
// escapes a quote a fake brings in (O'Brien in a single-quoted string).
func rewriteTplString(s string, rep *replacer, quote byte) string {
	var b strings.Builder
	last := 0
	for _, loc := range append(interpolationRe.FindAllStringIndex(s, -1), []int{len(s), len(s)}) {
		part := s[last:loc[0]]
		if v := rep.Apply(part); v != part {
			part = escapeQuote(v, quote)
		}
		b.WriteString(part)
		b.WriteString(s[loc[0]:loc[1]])
		last = loc[1]
	}
	return b.String()
}

// escapeQuote backslash-escapes any unescaped quote in s.
func escapeQuote(s string, quote byte) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '\\':
			b.WriteByte('\\')
			if i+1 < len(s) {
				i++
				b.WriteByte(s[i])
			}
		case quote:
			b.WriteByte('\\')
			b.WriteByte(quote)
		default:
			b.WriteByte(s[i])
		}
	}
	return b.String()
}
