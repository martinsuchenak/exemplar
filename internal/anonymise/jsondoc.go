package anonymise

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// jsonDoc is a JSON document, or JSON Lines. Only string values are
// searched and rewritten, in place in the original text, so key order,
// spacing, escaping and numbers stay exactly as sent. Keys are never
// changed: a consumer reads them.
type jsonDoc struct {
	text    string
	strings []jsonString
}

// jsonString is one string in the document: its bytes (quotes included),
// its value, and for a value the key it sits under.
type jsonString struct {
	start, end int
	key        bool
	value      string
	under      string
}

func parseJSON(lines bool) func(string, string) (doc, error) {
	return func(_, text string) (doc, error) {
		if lines {
			for n, line := range strings.Split(text, "\n") {
				if strings.TrimSpace(line) != "" && !json.Valid([]byte(line)) {
					return nil, fmt.Errorf("line %d is not valid JSON", n+1)
				}
			}
		} else if !json.Valid([]byte(text)) {
			return nil, errors.New("not valid JSON")
		}
		strs, err := scanJSON(text)
		if err != nil {
			return nil, err
		}
		return &jsonDoc{text: text, strings: strs}, nil
	}
}

// scanJSON finds every string in valid JSON. A string followed by a colon is
// a key; any other is a value, under the nearest key around it (the values
// of "emails": [...] are under "emails").
func scanJSON(text string) ([]jsonString, error) {
	var out []jsonString
	var under []string // one entry per open object or array
	for i := 0; i < len(text); i++ {
		switch text[i] {
		case '{':
			under = append(under, "")
		case '[':
			parent := ""
			if len(under) > 0 {
				parent = under[len(under)-1]
			}
			under = append(under, parent)
		case '}', ']':
			if len(under) > 0 {
				under = under[:len(under)-1]
			}
		case '"':
			j := i + 1
			for j < len(text) && text[j] != '"' {
				if text[j] == '\\' {
					j++
				}
				j++
			}
			if j >= len(text) {
				return nil, errors.New("not valid JSON: an unclosed string")
			}
			var v string
			if err := json.Unmarshal([]byte(text[i:j+1]), &v); err != nil {
				return nil, err
			}
			s := jsonString{start: i, end: j + 1, value: v}
			k := j + 1
			for k < len(text) && strings.ContainsRune(" \t\r\n", rune(text[k])) {
				k++
			}
			if k < len(text) && text[k] == ':' {
				s.key = true
				if len(under) > 0 {
					under[len(under)-1] = v
				}
			} else if len(under) > 0 {
				s.under = under[len(under)-1]
			}
			out = append(out, s)
			i = j
		}
	}
	return out, nil
}

func (d *jsonDoc) texts() []docText {
	var out []docText
	for _, s := range d.strings {
		if !s.key {
			out = append(out, docText{value: s.value, entity: fieldHint(s.under)})
		}
	}
	return out
}

func (d *jsonDoc) rewrite(rep *replacer) (string, error) {
	var b strings.Builder
	last := 0
	for _, s := range d.strings {
		if s.key {
			continue
		}
		v := rep.Apply(s.value)
		if v == s.value {
			continue
		}
		var enc bytes.Buffer
		e := json.NewEncoder(&enc)
		e.SetEscapeHTML(false)
		if err := e.Encode(v); err != nil {
			return "", err
		}
		b.WriteString(d.text[last:s.start])
		b.WriteString(strings.TrimSuffix(enc.String(), "\n"))
		last = s.end
	}
	b.WriteString(d.text[last:])
	return b.String(), nil
}

// shape is every key in order and the number of values.
func (d *jsonDoc) shape() string {
	var b strings.Builder
	values := 0
	for _, s := range d.strings {
		if s.key {
			b.WriteString(s.value + "\x00")
		} else {
			values++
		}
	}
	return fmt.Sprintf("%s%d", b.String(), values)
}
