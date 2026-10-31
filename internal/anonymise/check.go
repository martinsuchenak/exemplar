package anonymise

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/martinsuchenak/exemplar/internal/mimetree"
)

// The check compares a file with its anonymised copy and lists what came
// through exactly as it was: email and web addresses, long numbers, names
// and capitalised words. Detection misses some things (a company with no
// "Pty Ltd", a customer number with no label), and the list is what a
// reviewer looks at first. It holds only values the anonymised file holds
// too, so it says nothing the output does not; what it adds is that each is
// real.

// Kinds of values the check lists.
const (
	KeptEmail  = "email"
	KeptURL    = "url"
	KeptNumber = "number"
	KeptName   = "name" // two or more capitalised words
	KeptWord   = "word" // one capitalised word
)

// Kept is a value an anonymised file holds exactly as its original did.
type Kept struct {
	Kind  string `json:"kind"`
	Value string `json:"value"`
	// Count is how many times the anonymised file holds it.
	Count int `json:"count"`
}

var (
	keptEmail  = regexp.MustCompile(`[A-Za-z0-9._%+'-]+@[A-Za-z0-9-]+(?:\.[A-Za-z0-9-]+)+`)
	keptURL    = regexp.MustCompile(`(?i)\b(?:https?://|www\.)[^\s<>"')\]]+`)
	keptNumber = regexp.MustCompile(`\+?\(?\d(?:[ ().\-/]?\d){5,}`)
	// A date, with the hour of a time after it ("13/10/2026 14" out of
	// "13/10/2026 14:38:29"): kept on purpose, so not listed.
	keptDate = regexp.MustCompile(`^\d{1,4}[-/.]\d{1,2}[-/.]\d{1,4}(?:[ T]\d{1,2})?$`)
	keptName = regexp.MustCompile(`\p{Lu}[\p{L}'’-]*(?: \p{Lu}[\p{L}'’-]*)+`)
	keptWord = regexp.MustCompile(`\p{Lu}[\p{L}'’-]{2,}`)
)

// Unchanged returns what anonymised holds exactly as original did, most
// telling kinds first, then the most frequent. Both are read as the
// anonymiser reads their format.
func Unchanged(ctx context.Context, original, anonymised File) ([]Kept, error) {
	before, err := ReadText(ctx, original)
	if err != nil {
		return nil, fmt.Errorf("reading the original: %w", err)
	}
	after, err := ReadText(ctx, anonymised)
	if err != nil {
		return nil, fmt.Errorf("reading the anonymised file: %w", err)
	}
	return kept(strings.Join(before, "\n"), strings.Join(after, "\n")), nil
}

// kept lists the values of after that before holds too, as whole words.
func kept(before, after string) []Kept {
	var out []Kept
	seen := map[string]bool{}
	original := newWordIndex(before)
	add := func(kind string, re *regexp.Regexp, skip func(string) bool) {
		counts := map[string]int{}
		var order []string
		for _, v := range re.FindAllString(after, -1) {
			v = strings.TrimRight(v, ".,;:")
			if skip != nil && skip(v) {
				continue
			}
			if counts[v] == 0 {
				order = append(order, v)
			}
			counts[v]++
		}
		var found []Kept
		for _, v := range order {
			if !seen[v] && original.holds(v) {
				found = append(found, Kept{Kind: kind, Value: v, Count: counts[v]})
			}
		}
		sort.SliceStable(found, func(i, j int) bool { return found[i].Count > found[j].Count })
		for _, k := range found {
			seen[k.Value] = true
		}
		out = append(out, found...)
	}
	add(KeptEmail, keptEmail, nil)
	add(KeptURL, keptURL, nil)
	add(KeptNumber, keptNumber, func(v string) bool { return keptDate.MatchString(v) })
	add(KeptName, keptName, nil)
	// A word in a name already listed says nothing more.
	var names []string
	for _, k := range out {
		if k.Kind == KeptName {
			names = append(names, k.Value)
		}
	}
	listed := newWordIndex(strings.Join(names, "\n"))
	add(KeptWord, keptWord, listed.holds)
	return out
}

// wordIndex finds whole-word values in a text without scanning all of it
// for each: it indexes where each run of word characters starts, and looks
// for a value only where its rarest run is. Every run of a value bounded as
// a whole word is a whole run of the text, so this finds what holdsWord
// does.
type wordIndex struct {
	text string
	at   map[string][]int
}

func newWordIndex(text string) wordIndex {
	x := wordIndex{text: text, at: map[string][]int{}}
	for _, r := range wordRuns(text) {
		w := text[r[0]:r[1]]
		x.at[w] = append(x.at[w], r[0])
	}
	return x
}

// holds reports whether the text holds v as a whole word (see holdsWord).
func (x wordIndex) holds(v string) bool {
	runs := wordRuns(v)
	if len(runs) == 0 {
		return holdsWord(x.text, v)
	}
	var rarest []int
	offset := 0
	for i, r := range runs {
		at, ok := x.at[v[r[0]:r[1]]]
		if !ok {
			return false
		}
		if i == 0 || len(at) < len(rarest) {
			rarest, offset = at, r[0]
		}
	}
	for _, p := range rarest {
		start := p - offset
		if start >= 0 && start+len(v) <= len(x.text) && x.text[start:start+len(v)] == v && wordBounded(x.text, start, start+len(v)) {
			return true
		}
	}
	return false
}

// wordRuns returns the byte spans of s's runs of word characters.
func wordRuns(s string) [][2]int {
	var out [][2]int
	start := -1
	for i, r := range s {
		switch w := isWordRune(r); {
		case w && start < 0:
			start = i
		case !w && start >= 0:
			out = append(out, [2]int{start, i})
			start = -1
		}
	}
	if start >= 0 {
		out = append(out, [2]int{start, len(s)})
	}
	return out
}

// holdsWord reports whether s holds v with no letter or digit right before
// or after it.
func holdsWord(s, v string) bool {
	for i := 0; ; {
		j := strings.Index(s[i:], v)
		if j < 0 {
			return false
		}
		if wordBounded(s, i+j, i+j+len(v)) {
			return true
		}
		i += j + 1
	}
}

// wordBounded reports whether s[start:end] has no letter or digit right
// before or after it.
func wordBounded(s string, start, end int) bool {
	r1, _ := utf8.DecodeLastRuneInString(s[:start])
	r2, _ := utf8.DecodeRuneInString(s[end:])
	return !isWordRune(r1) && !isWordRune(r2)
}

// ReadText returns a file's text as the anonymiser reads it for detection:
// a document's text, an email's headers, text parts and the attachments
// the anonymiser reads.
func ReadText(ctx context.Context, f File) ([]string, error) {
	def, err := findFormat(f.Name)
	if err != nil {
		return nil, err
	}
	return readText(ctx, def.handler, f, 0)
}

func readText(ctx context.Context, h handler, f File, depth int) ([]string, error) {
	var texts []docText
	switch h := h.(type) {
	case emailHandler:
		return emailText(ctx, f.Data, depth)
	case docHandler:
		text, _, err := decodeText(f.Data)
		if err != nil {
			return nil, err
		}
		d, err := h.parse(f.Name, text)
		if err != nil {
			return nil, err
		}
		texts = d.texts()
	case ooxmlHandler:
		pkg, err := h.format.open(f.Data)
		if err != nil {
			return nil, err
		}
		texts = pkg.texts()
	case rtfHandler:
		d, err := parseRTF(f.Data)
		if err != nil {
			return nil, err
		}
		texts = d.texts()
	case pdfHandler:
		d, err := readPDF(ctx, f.Data)
		if err != nil {
			return nil, err
		}
		defer d.s.close()
		texts = d.texts()
	default:
		return nil, errors.New("this format cannot be read")
	}
	out := make([]string, 0, len(texts))
	for _, t := range texts {
		if t.column == "" {
			out = append(out, t.value)
		}
	}
	return out, nil
}

// emailText returns an email's header values, text parts, attached emails'
// text and the text of attachments in a format the anonymiser reads (one it
// cannot read was replaced, so holds nothing of the original).
func emailText(ctx context.Context, data []byte, depth int) ([]string, error) {
	if depth > mimetree.MaxDepth {
		return nil, fmt.Errorf("attached emails nest more than %d levels deep", mimetree.MaxDepth)
	}
	root, err := mimetree.ParseAt(data, depth)
	if err != nil {
		return nil, err
	}
	var out []string
	root.Walk(func(p *mimetree.Part) {
		for _, h := range p.Header.Fields {
			out = append(out, mimetree.DecodeHeader(h.Value))
		}
		if p.IsMultipart() {
			return
		}
		mt, _ := p.MediaType()
		name := filename(p)
		switch {
		case mt == "message/rfc822":
			if b, err := p.Decoded(); err == nil {
				if inner, err := emailText(ctx, b, depth+1); err == nil {
					out = append(out, inner...)
				}
			}
			return
		case strings.HasPrefix(mt, "text/") && name == "":
			if t, err := p.Text(); err == nil {
				out = append(out, t)
			}
			return
		}
		if name == "" {
			if f, ok := attachmentFormats[mt]; ok {
				name = NameFor(f)
			}
		}
		def, err := findFormat(name)
		if err != nil || def.format == FormatEmail {
			return
		}
		b, err := p.Decoded()
		if err != nil {
			return
		}
		if inner, err := readText(ctx, def.handler, File{Name: name, Data: b}, depth+1); err == nil {
			out = append(out, inner...)
		}
	})
	return out, nil
}
