package anonymise

import (
	"bytes"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"regexp"
	"sort"
	"strings"
)

// Office files are zips of XML parts. They are rewritten by splicing: the
// decoder finds where each piece of text and each attribute value sits in
// the bytes, and only those bytes change. Re-encoding with encoding/xml
// would rename namespace prefixes and lose formatting Word depends on.

// xmlName is an element or attribute name with its namespace resolved.
type xmlName struct {
	space, local string
}

type xmlKind int

const (
	xmlStart xmlKind = iota
	xmlEnd
	xmlText
	xmlOther // comments, processing instructions, directives
)

// xmlNode is one token of a part and where it sits in the bytes.
type xmlNode struct {
	kind       xmlKind
	name       xmlName
	start, end int
	attrs      []xmlAttr
	// text is the decoded text of an xmlText node.
	text string
	// match is the index of a start node's end node, and the reverse.
	match int
}

// xmlAttr is one attribute: its decoded value and where the raw value sits
// (inside the quotes).
type xmlAttr struct {
	name       xmlName
	value      string
	start, end int
}

// attrRe finds each attribute in a start tag, with its quoted value.
var attrRe = regexp.MustCompile(`([^\s=<>/"']+)\s*=\s*("[^"]*"|'[^']*')`)

// xmlNamespace is the namespace of the xml: prefix, which needs no declaring.
const xmlNamespace = "http://www.w3.org/XML/1998/namespace"

// xmlMaxNodes caps how many tokens one XML part may hold. Every node is
// materialised with its names, attributes and text while scanning, so a part
// of nothing but tiny tags must not multiply into many times its size.
const xmlMaxNodes = 2 << 20

// scanXML tokenises a whole part. It fails on anything that is not
// well-formed, so a part that cannot be read is never passed through.
func scanXML(data []byte) ([]xmlNode, error) {
	d := xml.NewDecoder(bytes.NewReader(data))
	d.Strict = true
	var nodes []xmlNode
	var open []int                                       // indexes of open start nodes
	var openNames []string                               // their names as written, to match end tags
	scopes := []map[string]string{{"xml": xmlNamespace}} // prefix -> namespace
	resolve := func(prefix, local string, element bool) xmlName {
		if prefix == "" && !element {
			return xmlName{local: local} // unprefixed attributes have no namespace
		}
		for i := len(scopes) - 1; i >= 0; i-- {
			if uri, ok := scopes[i][prefix]; ok {
				return xmlName{space: uri, local: local}
			}
		}
		return xmlName{space: prefix, local: local}
	}
	for {
		start := int(d.InputOffset())
		tok, err := d.RawToken()
		if err == io.EOF {
			break
		}
		if err != nil {
			// The decoder's message quotes element and entity names from
			// the document; keep only where it went wrong.
			var se *xml.SyntaxError
			if errors.As(err, &se) {
				return nil, fmt.Errorf("not well-formed XML at line %d", se.Line)
			}
			return nil, errors.New("not well-formed XML")
		}
		end := int(d.InputOffset())
		if len(nodes) >= xmlMaxNodes {
			return nil, fmt.Errorf("the XML holds more than %d tokens", xmlMaxNodes)
		}
		switch t := tok.(type) {
		case xml.StartElement:
			scope := map[string]string{}
			for _, a := range t.Attr {
				switch {
				case a.Name.Space == "xmlns":
					scope[a.Name.Local] = a.Value
				case a.Name.Space == "" && a.Name.Local == "xmlns":
					scope[""] = a.Value
				}
			}
			scopes = append(scopes, scope)
			n := xmlNode{kind: xmlStart, name: resolve(t.Name.Space, t.Name.Local, true), start: start, end: end, match: -1}
			raw := attrRe.FindAllSubmatchIndex(data[start:end], -1)
			if len(raw) != len(t.Attr) {
				return nil, fmt.Errorf("unreadable attributes at byte %d", start)
			}
			for i, a := range t.Attr {
				m := raw[i]
				if got := string(data[start+m[2] : start+m[3]]); got != qualified(a.Name) {
					return nil, fmt.Errorf("unreadable attributes at byte %d", start)
				}
				n.attrs = append(n.attrs, xmlAttr{
					name: resolve(a.Name.Space, a.Name.Local, false), value: a.Value,
					start: start + m[4] + 1, end: start + m[5] - 1,
				})
			}
			open, openNames = append(open, len(nodes)), append(openNames, qualified(t.Name))
			nodes = append(nodes, n)
		case xml.EndElement:
			if len(open) == 0 {
				return nil, errors.New("an end tag with no start")
			}
			s := open[len(open)-1]
			if openNames[len(openNames)-1] != qualified(t.Name) {
				return nil, fmt.Errorf("an end tag closes a different element at byte %d", start)
			}
			open, openNames = open[:len(open)-1], openNames[:len(openNames)-1]
			nodes[s].match = len(nodes)
			nodes = append(nodes, xmlNode{kind: xmlEnd, name: nodes[s].name, start: start, end: end, match: s})
			scopes = scopes[:len(scopes)-1]
		case xml.CharData:
			nodes = append(nodes, xmlNode{kind: xmlText, text: string(t), start: start, end: end, match: -1})
		default:
			nodes = append(nodes, xmlNode{kind: xmlOther, start: start, end: end, match: -1})
		}
	}
	if len(open) > 0 {
		return nil, errors.New("an element is never closed")
	}
	return nodes, nil
}

func qualified(n xml.Name) string {
	if n.Space == "" {
		return n.Local
	}
	return n.Space + ":" + n.Local
}

// attr returns the value of the attribute called name, and its index.
func (n *xmlNode) attr(name xmlName) (string, int) {
	for i, a := range n.attrs {
		if a.name == name {
			return a.value, i
		}
	}
	return "", -1
}

// xmlEdit replaces data[start:end] with with.
type xmlEdit struct {
	start, end int
	with       string
}

// splice applies edits, which must not overlap, to data.
func splice(data []byte, edits []xmlEdit) ([]byte, error) {
	sort.SliceStable(edits, func(i, j int) bool { return edits[i].start < edits[j].start })
	var b bytes.Buffer
	last := 0
	for _, e := range edits {
		if e.start < last || e.end < e.start || e.end > len(data) {
			return nil, errors.New("overlapping edits")
		}
		b.Write(data[last:e.start])
		b.WriteString(e.with)
		last = e.end
	}
	b.Write(data[last:])
	return b.Bytes(), nil
}

var (
	textEscaper = strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;")
	attrEscaper = strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&quot;", "'", "&apos;", "\t", "&#x9;", "\n", "&#xA;", "\r", "&#xD;")
)
