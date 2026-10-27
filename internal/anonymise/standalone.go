package anonymise

import (
	"context"
	"regexp"
	"sort"
	"strings"
	"sync"
	"unicode"
	"unicode/utf8"

	"github.com/martinsuchenak/exemplar/internal/synth"
)

// What anonymising without the server's registry needs: a registry that
// lives for one run, and a detector for when no Presidio is at hand.

// MemoryRegistry is a Registry kept in memory for one run. Its people have
// no IDs: they are never stored, so nothing can link to them.
type MemoryRegistry struct {
	mu     sync.Mutex
	tokens map[string]string
	people map[string]synth.Person
}

// NewMemoryRegistry returns an empty MemoryRegistry.
func NewMemoryRegistry() *MemoryRegistry {
	return &MemoryRegistry{tokens: map[string]string{}, people: map[string]synth.Person{}}
}

// Token returns the fake kept for hash, or keeps fake for it.
func (m *MemoryRegistry) Token(_ context.Context, hash, fake string) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if v, ok := m.tokens[hash]; ok {
		return v, nil
	}
	m.tokens[hash] = fake
	return fake, nil
}

// LookupToken returns the fake kept for hash, if any.
func (m *MemoryRegistry) LookupToken(_ context.Context, hash string) (string, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	v, ok := m.tokens[hash]
	return v, ok, nil
}

// Person returns the person kept under any of hashes, or keeps p under all
// of them.
func (m *MemoryRegistry) Person(_ context.Context, hashes []string, p synth.Person) (synth.Person, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, h := range hashes {
		if found, ok := m.people[h]; ok {
			p = found
			break
		}
	}
	for _, h := range hashes {
		m.people[h] = p
	}
	return p, nil
}

// PatternsOnly is a Detector for running without Presidio. It finds the
// names the engine already knows (from email headers, and column or key
// hints), as whole words, in any case; the Go patterns still find
// addresses, URLs, phone numbers and the rest. Names that appear only in
// prose are missed.
type PatternsOnly struct{}

// patternsBatch is how many names go into one pattern.
const patternsBatch = 500

// Detect implements Detector. The names are matched a batch at a time,
// longest first, so "Jo Ann" wins over "Jo" where both fit. A whole word
// is one with no letter, digit or underscore next to it, in any script:
// "José" ends at the é.
func (PatternsOnly) Detect(_ context.Context, text string, names []string, _ float64) ([]Span, error) {
	seen := map[string]bool{}
	var known []string
	for _, n := range names {
		if n != "" && !seen[strings.ToLower(n)] {
			seen[strings.ToLower(n)] = true
			known = append(known, n)
		}
	}
	sort.Slice(known, func(i, j int) bool {
		if len(known[i]) != len(known[j]) {
			return len(known[i]) > len(known[j])
		}
		return known[i] < known[j]
	})
	var spans []Span
	for len(known) > 0 {
		batch := known[:min(patternsBatch, len(known))]
		known = known[len(batch):]
		quoted := make([]string, len(batch))
		for i, n := range batch {
			quoted[i] = regexp.QuoteMeta(n)
		}
		// The name, then a character that ends a word, or the end. What
		// comes before is checked by hand: Go's \b only knows ASCII.
		re, err := regexp.Compile(`(?i)(` + strings.Join(quoted, "|") + `)(?:[^\p{L}\p{M}\p{N}_]|$)`)
		if err != nil {
			return nil, err
		}
		for i := 0; i < len(text); {
			m := re.FindStringSubmatchIndex(text[i:])
			if m == nil {
				break
			}
			start, end := i+m[2], i+m[3]
			if r, _ := utf8.DecodeLastRuneInString(text[:start]); start > 0 && isWordRune(r) {
				_, size := utf8.DecodeRuneInString(text[start:])
				i = start + size
				continue
			}
			spans = append(spans, Span{Start: start, End: end, Entity: EntityPerson})
			i = end
		}
	}
	return spans, nil
}

func isWordRune(r rune) bool {
	return r == '_' || unicode.IsLetter(r) || unicode.IsMark(r) || unicode.IsDigit(r)
}

// MediaType returns the Content-Type a format is served with.
func MediaType(f Format) string {
	for _, def := range formats {
		if def.format == f {
			return def.mediaType
		}
	}
	return "application/octet-stream"
}
