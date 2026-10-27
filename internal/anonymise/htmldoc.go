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
)

func parseHTML(_, text string) (doc, error) { return htmlDoc{text}, nil }

func (d htmlDoc) texts() []docText { return []docText{{value: htmlPageText(d.text)}} }

func (d htmlDoc) rewrite(rep *replacer) (string, error) { return rep.Apply(d.text), nil }

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
