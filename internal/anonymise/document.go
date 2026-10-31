package anonymise

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"unicode/utf16"
	"unicode/utf8"

	"github.com/martinsuchenak/exemplar/internal/textswap"
)

// A document is a file of text with some structure (Markdown, CSV, JSON,
// HTML, a template). docHandler runs every document format through the same
// steps as an email body: find what looks personal, build the fakes, swap
// them in. Each format only says where its text is and how to write it back,
// and the output must parse again with the same structure.

// docText is one piece of a document's text to search.
type docText struct {
	value string
	// entity, when set, says what the whole value is, as a CSV column or
	// a JSON key named "email" does. It is added whatever detection finds.
	entity string
	// column, when set, names the table column the value is a cell of,
	// below the column's label: a CSV column, a sheet's column, a JSON key.
	// Such a text is only counted (see addColumnNames), not searched, since
	// the same value is also in a text that is.
	column string
}

// doc is one parsed document.
type doc interface {
	// texts returns every piece of text to search, in reading order.
	texts() []docText
	// rewrite returns the document with rep applied to its text, and only
	// its text.
	rewrite(rep *replacer) (string, error)
	// shape describes the structure that must survive rewriting: the
	// output's shape must equal the input's.
	shape() string
}

// docHandler reads and rewrites one document format.
type docHandler struct {
	// parse reads a document, already decoded to UTF-8. name is the file
	// name, which tells a template what it renders.
	parse func(name, text string) (doc, error)
}

// prepare finds the names in a document, so they are found in every file of
// the batch: they are sent to the detector with each text from now on, and
// their name tokens are mapped before any file is rewritten, as an email's
// headers are. So "Sarah Lane" in one file and sarah.lane@… in another get
// matching fakes whichever is rewritten first.
func (h docHandler) prepare(ctx context.Context, e *Engine, file File) error {
	text, _, err := decodeText(file.Data)
	if err != nil {
		return err
	}
	d, err := h.parse(file.Name, text)
	if err != nil {
		return err
	}
	return e.learnNames(ctx, d.texts())
}

// learnNames detects the names in a document's texts and maps them before
// any file of the batch is rewritten (see docHandler.prepare).
func (e *Engine) learnNames(ctx context.Context, texts []docText) error {
	f := newFound(e.profile)
	if err := e.detectTexts(ctx, texts, f); err != nil {
		return err
	}
	for _, v := range fakeOrder(f.entities) {
		if f.entities[v] != EntityPerson {
			break
		}
		e.names[v] = true
		if _, err := e.fakeName(ctx, v); err != nil {
			return err
		}
	}
	return nil
}

// docReplacements detects what is personal in a document's texts and
// returns the replacements and the replacer that applies them.
func (e *Engine) docReplacements(ctx context.Context, texts []docText) (map[string]string, *found, *replacer, error) {
	f := newFound(e.profile)
	if err := e.detectTexts(ctx, texts, f); err != nil {
		return nil, nil, nil, err
	}
	f.dropFragments()
	pairs, err := e.replacements(ctx, f)
	if err != nil {
		return nil, nil, nil, err
	}
	rep, err := textswap.NewSets(e.sets(pairs, f))
	if err != nil {
		return nil, nil, nil, err
	}
	return pairs, f, rep, nil
}

// anonymise rewrites one document.
func (h docHandler) anonymise(ctx context.Context, e *Engine, file File) (*Output, error) {
	text, encode, err := decodeText(file.Data)
	if err != nil {
		return nil, err
	}
	d, err := h.parse(file.Name, text)
	if err != nil {
		return nil, err
	}
	pairs, f, rep, err := e.docReplacements(ctx, d.texts())
	if err != nil {
		return nil, err
	}
	out, err := d.rewrite(rep)
	if err != nil {
		return nil, err
	}
	again, err := h.parse(file.Name, out)
	if err != nil {
		return nil, fmt.Errorf("anonymised output does not parse: %w", err)
	}
	if again.shape() != d.shape() {
		return nil, errors.New("anonymising changed the file's structure")
	}
	return &Output{Data: encode(out), Replacements: pairs, Entities: f.entities}, nil
}

// detectChunk is about how much text goes to the detector at once. A
// document's texts are joined up to this size, so a CSV of many short rows
// takes a few calls rather than one per row.
const detectChunk = 32 * 1024

// detectTexts runs detection over texts, in chunks. Texts are joined with a
// blank line, which no pattern or plausible name runs across.
func (e *Engine) detectTexts(ctx context.Context, texts []docText, f *found) error {
	var chunk strings.Builder
	flush := func() error {
		err := e.detect(ctx, chunk.String(), f)
		chunk.Reset()
		return err
	}
	for _, t := range texts {
		if t.column != "" {
			continue
		}
		if labelled(t) {
			f.add(t.value, t.entity)
		}
		if chunk.Len() > 0 && chunk.Len()+len(t.value) > detectChunk {
			if err := flush(); err != nil {
				return err
			}
		}
		if chunk.Len() > 0 {
			chunk.WriteString("\n\n")
		}
		chunk.WriteString(t.value)
	}
	if err := flush(); err != nil {
		return err
	}
	f.addColumnNames(texts)
	return nil
}

// addColumnNames takes a column whose values are mostly people for a column
// of people, as a "Name" label would, though its label is "Account
// Manager" or nothing a hint knows. Detection finds some of the names in it
// and misses others, often the same few staff names on every row; each of
// those that looks like a name (see columnName) is then a person too. A
// column counts when known people are at least half of it, counted either
// way: by distinct values (at least two), or by cells (at least three), as
// when one detected name fills most rows and the few others are missed.
// A name learned in one column counts in the others,
// so the rule runs again until nothing changes, at most maxColumnPasses
// times: each pass reads every cell, and a sheet of thousands of columns
// that each learn a name for the next would otherwise be read once per
// column. Real tables settle in two or three. Columns are read in name
// order, so where the cap stops it the result is still the same every run.
func (f *found) addColumnNames(texts []docText) {
	columns := map[string]map[string]int{} // value -> cells, by column
	for _, t := range texts {
		v := cleanValue(t.value)
		if t.column == "" || v == "" || placeholders[strings.ToLower(v)] {
			continue
		}
		if columns[t.column] == nil {
			columns[t.column] = map[string]int{}
		}
		columns[t.column][v]++
	}
	names := make([]string, 0, len(columns))
	for c := range columns {
		names = append(names, c)
	}
	sort.Strings(names)
	for pass, changed := 0, true; changed && pass < maxColumnPasses; pass++ {
		changed = false
		for _, c := range names {
			values := columns[c]
			people, peopleCells, cells := 0, 0, 0
			for v, n := range values {
				cells += n
				if f.entities[v] == EntityPerson {
					people, peopleCells = people+1, peopleCells+n
				}
			}
			byValues := people >= 2 && 2*people >= len(values)
			byCells := peopleCells >= 3 && 2*peopleCells >= cells
			if !byValues && !byCells {
				continue
			}
			for v := range values {
				if _, known := f.entities[v]; !known && columnName(v) {
					f.add(v, EntityPerson)
					changed = changed || f.entities[v] == EntityPerson
				}
			}
		}
	}
}

// maxColumnPasses caps how often addColumnNames reads the columns again.
const maxColumnPasses = 8

// columnName reports whether a value in a column of people looks like a
// name: two to five words with letters, no digits and nothing an address or
// a path has. A single word ("Vacant", "Admin") is too often not a person,
// and its fake would replace it everywhere in the file.
func columnName(v string) bool {
	words := strings.Fields(v)
	return len(words) >= 2 && len(words) <= 5 && plausibleName(v) &&
		!strings.ContainsAny(v, "0123456789@#&/\\=+_|<>{}[]")
}

// fieldHints are the column and key names whose values are known to be one
// entity, by their letters and digits only: "E-mail", "email_address" and
// "Email Address" all name an email address.
var fieldHints = map[string]string{
	"email": EntityEmail, "emailaddress": EntityEmail, "mail": EntityEmail,
	"name": EntityPerson, "fullname": EntityPerson, "firstname": EntityPerson, "lastname": EntityPerson,
	"surname": EntityPerson, "givenname": EntityPerson, "contactname": EntityPerson, "customername": EntityPerson,
	"phone": EntityPhone, "phonenumber": EntityPhone, "mobile": EntityPhone, "telephone": EntityPhone, "tel": EntityPhone,
	"company": EntityCompany, "companyname": EntityCompany, "organisation": EntityCompany, "organization": EntityCompany,
	"employer": EntityCompany,
	"bsb":      EntityBankAccount, "accountnumber": EntityBankAccount, "accountno": EntityBankAccount, "bankaccount": EntityBankAccount,
	"bankaccountnumber": EntityBankAccount,
}

// placeholders are values a labelled column holds that are no one: a totals
// row, a gap filled in. A fake replaces a value everywhere in a file, so one
// of these taken for a name would turn every "Total" into a person.
var placeholders = map[string]bool{
	"total": true, "totals": true, "subtotal": true, "grand total": true, "sum": true,
	"n/a": true, "na": true, "none": true, "nil": true, "null": true, "-": true, "--": true, "?": true,
	"tbd": true, "tba": true, "tbc": true, "unknown": true, "various": true, "other": true, "misc": true,
}

// labelled reports whether a text's label says what it is: it has one, and
// the text is not a placeholder, nor a phone number too short to be one
// (an extension, a year).
func labelled(t docText) bool {
	v := strings.TrimSpace(t.value)
	if t.entity == "" || v == "" || placeholders[strings.ToLower(v)] {
		return false
	}
	if t.entity == EntityPhone {
		digits := 0
		for _, r := range v {
			if r >= '0' && r <= '9' {
				digits++
			}
		}
		return digits >= minPhoneDigits
	}
	return true
}

// fieldSuffixes name an entity at the end of a longer label: "Mobile Phone",
// "Work Email", "Contact Phone Number", "homePhone". Only phones and emails:
// a label ending in "name" is as often a product's or a file's.
var fieldSuffixes = []struct{ suffix, entity string }{
	{"phone", EntityPhone}, {"phonenumber", EntityPhone}, {"mobile", EntityPhone}, {"mobilenumber", EntityPhone},
	{"email", EntityEmail}, {"emailaddress", EntityEmail},
}

// fieldHint returns the entity a column or key name says its values are.
func fieldHint(label string) string {
	key := companyKey(label)
	if e := fieldHints[key]; e != "" {
		return e
	}
	for _, s := range fieldSuffixes {
		if strings.HasSuffix(key, s.suffix) {
			return s.entity
		}
	}
	return ""
}

// decodeText returns a file's text as UTF-8, and a function that encodes a
// rewritten text the way the file was: UTF-8 with or without a byte-order
// mark, or UTF-16 with one. Anything else is refused rather than guessed,
// since a wrong guess silently misses names.
func decodeText(data []byte) (string, func(string) []byte, error) {
	utf8BOM := []byte{0xef, 0xbb, 0xbf}
	switch {
	case bytes.HasPrefix(data, utf8BOM):
		text := string(data[3:])
		if err := checkText(text); err != nil {
			return "", nil, err
		}
		return text, func(s string) []byte { return append(append([]byte{}, utf8BOM...), s...) }, nil
	case bytes.HasPrefix(data, []byte{0xff, 0xfe}), bytes.HasPrefix(data, []byte{0xfe, 0xff}):
		big := data[0] == 0xfe
		text, err := decodeUTF16(data[2:], big)
		if err != nil {
			return "", nil, err
		}
		if err := checkText(text); err != nil {
			return "", nil, err
		}
		return text, func(s string) []byte { return encodeUTF16(s, big) }, nil
	}
	text := string(data)
	if err := checkText(text); err != nil {
		return "", nil, err
	}
	return text, func(s string) []byte { return []byte(s) }, nil
}

func checkText(text string) error {
	if !utf8.ValidString(text) {
		return errors.New("not UTF-8 text (or UTF-16 with a byte-order mark)")
	}
	if strings.ContainsRune(text, 0) {
		return errors.New("not text: it holds NUL bytes")
	}
	return nil
}

func decodeUTF16(b []byte, big bool) (string, error) {
	if len(b)%2 != 0 {
		return "", errors.New("not UTF-16 text: odd number of bytes")
	}
	units := make([]uint16, len(b)/2)
	for i := range units {
		if big {
			units[i] = uint16(b[2*i])<<8 | uint16(b[2*i+1])
		} else {
			units[i] = uint16(b[2*i+1])<<8 | uint16(b[2*i])
		}
	}
	runes := utf16.Decode(units)
	for _, r := range runes {
		if r == utf8.RuneError {
			return "", errors.New("not UTF-16 text: unpaired surrogate")
		}
	}
	return string(runes), nil
}

func encodeUTF16(s string, big bool) []byte {
	units := utf16.Encode([]rune(s))
	out := make([]byte, 2, 2+2*len(units))
	if big {
		out[0], out[1] = 0xfe, 0xff
	} else {
		out[0], out[1] = 0xff, 0xfe
	}
	for _, u := range units {
		if big {
			out = append(out, byte(u>>8), byte(u))
		} else {
			out = append(out, byte(u), byte(u>>8))
		}
	}
	return out
}

// plainDoc is text without structure: plain text and Markdown. Markdown's
// link and image targets are URLs and addresses to the patterns, and code
// is searched like the rest, since real data in a code sample is still real.
type plainDoc struct{ text string }

func parsePlain(_, text string) (doc, error) { return plainDoc{text}, nil }

func (d plainDoc) texts() []docText                      { return []docText{{value: d.text}} }
func (d plainDoc) rewrite(rep *replacer) (string, error) { return rep.Apply(d.text), nil }
func (d plainDoc) shape() string                         { return "" }
