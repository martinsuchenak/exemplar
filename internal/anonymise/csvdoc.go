package anonymise

import (
	"fmt"
	"strconv"
	"strings"
)

// csvDoc is a CSV or TSV file. It is read with a small tokeniser rather than
// encoding/csv so that a cell that does not change keeps its exact bytes and
// quoting, and so do the line endings: only changed cells are rewritten.
type csvDoc struct {
	text  string
	comma byte
	rows  [][]csvField
}

// csvField is one cell: its bytes in the file (quotes included) and its value.
type csvField struct {
	start, end int
	quoted     bool
	value      string
}

func parseCSV(comma byte) func(string, string) (doc, error) {
	return func(_, text string) (doc, error) {
		d := &csvDoc{text: text, comma: comma}
		for i := 0; i < len(text); {
			row, next, err := d.readRow(i)
			if err != nil {
				return nil, fmt.Errorf("row %d: %w", len(d.rows)+1, err)
			}
			d.rows, i = append(d.rows, row), next
		}
		return d, nil
	}
}

// readRow reads the row starting at i and returns where the next one starts.
func (d *csvDoc) readRow(i int) ([]csvField, int, error) {
	var row []csvField
	for {
		f := csvField{start: i}
		if i < len(d.text) && d.text[i] == '"' {
			f.quoted = true
			var b strings.Builder
			i++
			for {
				j := strings.IndexByte(d.text[i:], '"')
				if j < 0 {
					return nil, 0, fmt.Errorf("field %d: a quoted field is never closed", len(row)+1)
				}
				b.WriteString(d.text[i : i+j])
				i += j + 1
				if i < len(d.text) && d.text[i] == '"' {
					b.WriteByte('"')
					i++
					continue
				}
				break
			}
			if i < len(d.text) && d.text[i] != d.comma && d.text[i] != '\n' && d.text[i] != '\r' {
				return nil, 0, fmt.Errorf("field %d: text after a closing quote", len(row)+1)
			}
			f.value = b.String()
		} else {
			j := i
			for j < len(d.text) && d.text[j] != d.comma && d.text[j] != '\n' && d.text[j] != '\r' {
				j++
			}
			f.value, i = d.text[i:j], j
		}
		f.end = i
		row = append(row, f)
		switch {
		case i >= len(d.text):
			return row, i, nil
		case d.text[i] == d.comma:
			i++
		case strings.HasPrefix(d.text[i:], "\r\n"):
			return row, i + 2, nil
		default: // \n, or a lone \r
			return row, i + 1, nil
		}
	}
}

// texts returns each row as one text, cells on their own lines, and every
// cell under a header that names an entity ("Email", "Phone") as that
// entity. The header row is searched too.
func (d *csvDoc) texts() []docText {
	var hints []string
	if len(d.rows) > 1 {
		for _, f := range d.rows[0] {
			hints = append(hints, fieldHint(f.value))
		}
	}
	var out []docText
	for r, row := range d.rows {
		values := make([]string, len(row))
		for c, f := range row {
			values[c] = f.value
			if r > 0 && c < len(hints) && hints[c] != "" {
				out = append(out, docText{value: f.value, entity: hints[c]})
			}
		}
		out = append(out, docText{value: strings.Join(values, "\n")})
	}
	return out
}

func (d *csvDoc) rewrite(rep *replacer) (string, error) {
	var b strings.Builder
	last := 0
	for _, row := range d.rows {
		for _, f := range row {
			v := rep.Apply(f.value)
			if v == f.value {
				continue
			}
			b.WriteString(d.text[last:f.start])
			b.WriteString(d.quote(v, f.quoted))
			last = f.end
		}
	}
	b.WriteString(d.text[last:])
	return b.String(), nil
}

// quote writes a changed cell, quoted if it was or now must be.
func (d *csvDoc) quote(v string, quoted bool) string {
	if !quoted && !strings.ContainsAny(v, string(d.comma)+"\"\r\n") {
		return v
	}
	return `"` + strings.ReplaceAll(v, `"`, `""`) + `"`
}

// shape is the number of cells in each row.
func (d *csvDoc) shape() string {
	var b strings.Builder
	for _, row := range d.rows {
		b.WriteString(strconv.Itoa(len(row)) + ",")
	}
	return b.String()
}
