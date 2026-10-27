package anonymise

import (
	"html"
	"regexp"
	"strings"
)

// htmlDoc is an HTML page. Like an email's HTML body, it is searched as its
// visible text and rewritten as raw HTML; unlike a body, the <head> is read
// too (its <title> and <meta> content), and so are comments, since a page
// is kept and shared whole.
type htmlDoc struct{ text string }

var (
	// pageSkipBlocks hold code, not text.
	pageSkipBlocks = regexp.MustCompile(`(?is)<(style|script)\b.*?</(style|script)>`)
	htmlComments   = regexp.MustCompile(`(?s)<!--(.*?)-->`)
	// pageAttrs are the attributes whose values are text or links.
	pageAttrs = regexp.MustCompile(`(?i)\b(?:alt|title|href|src|content|placeholder|aria-label|value)\s*=\s*(?:"([^"]*)"|'([^']*)')`)
	tagNames  = regexp.MustCompile(`<\s*(/?)\s*([A-Za-z][A-Za-z0-9:-]*)`)
	// charRefs are HTML character references: &eacute; &#233; &#xE9;.
	charRefs = regexp.MustCompile(`&(#[0-9]{1,7}|#[xX][0-9a-fA-F]{1,6}|[A-Za-z][A-Za-z0-9]{1,31});`)
)

func parseHTML(_, text string) (doc, error) { return htmlDoc{text}, nil }

func (d htmlDoc) texts() []docText { return []docText{{value: htmlPageText(d.text)}} }

func (d htmlDoc) rewrite(rep *replacer) (string, error) { return applyHTML(d.text, rep), nil }

// shape is the sequence of tags, outside comments.
func (d htmlDoc) shape() string { return htmlTags(d.text) }

// htmlPageText returns a page's text for detection: visible text, the text
// and links in attributes, and comments.
func htmlPageText(doc string) string {
	doc = pageSkipBlocks.ReplaceAllString(doc, " ")
	var extra strings.Builder
	for _, m := range htmlComments.FindAllStringSubmatch(doc, -1) {
		extra.WriteString("\n" + m[1] + "\n")
	}
	doc = htmlComments.ReplaceAllString(doc, " ")
	for _, m := range pageAttrs.FindAllStringSubmatch(doc, -1) {
		extra.WriteString("\n" + strings.TrimPrefix(html.UnescapeString(m[1]+m[2]), "mailto:") + "\n")
	}
	doc = blockTags.ReplaceAllString(doc, "\n")
	doc = tags.ReplaceAllString(doc, " ")
	return html.UnescapeString(doc) + extra.String()
}

// htmlTags lists a page's tags in order, lower-cased, ignoring comments.
func htmlTags(doc string) string {
	var b strings.Builder
	for _, m := range tagNames.FindAllStringSubmatch(htmlComments.ReplaceAllString(doc, ""), -1) {
		b.WriteString(m[1] + strings.ToLower(m[2]) + " ")
	}
	return b.String()
}

// applyHTML returns doc with rep applied. Detection reads HTML decoded
// (Jos&eacute; is José), so matching runs on the decoded text too, and each
// match replaces the bytes it came from: a name written with character
// references is replaced like any other, and every other byte stays as
// written. A fake that replaces references is written escaped.
func applyHTML(doc string, rep *replacer) string {
	if !strings.Contains(doc, "&") {
		return rep.Apply(doc)
	}
	dec, starts, ends := decodeRefs(doc)
	matches := rep.Matches(dec)
	if len(matches) == 0 {
		return doc
	}
	var b strings.Builder
	last := 0
	for _, m := range matches {
		from, to := starts[m.Start], ends[m.End]
		b.WriteString(doc[last:from])
		if doc[from:to] == dec[m.Start:m.End] {
			b.WriteString(m.To)
		} else {
			b.WriteString(html.EscapeString(m.To))
		}
		last = to
	}
	b.WriteString(doc[last:])
	return b.String()
}

// decodeRefs decodes the character references in s. For each byte of the
// result, starts holds where the character it belongs to starts in s, and
// ends[k+1] where the character holding byte k ends, so a range of the
// result maps back to whole characters of s.
func decodeRefs(s string) (string, []int, []int) {
	var b strings.Builder
	var starts, ends []int
	ends = append(ends, 0)
	add := func(text string, from, to int) {
		for range len(text) {
			starts, ends = append(starts, from), append(ends, to)
		}
		b.WriteString(text)
	}
	last := 0
	for _, m := range charRefs.FindAllStringIndex(s, -1) {
		for i := last; i < m[0]; i++ {
			add(s[i:i+1], i, i+1)
		}
		ref := s[m[0]:m[1]]
		if d := html.UnescapeString(ref); d != ref {
			add(d, m[0], m[1])
		} else {
			for i := m[0]; i < m[1]; i++ {
				add(s[i:i+1], i, i+1)
			}
		}
		last = m[1]
	}
	for i := last; i < len(s); i++ {
		add(s[i:i+1], i, i+1)
	}
	starts = append(starts, len(s))
	return b.String(), starts, ends
}
