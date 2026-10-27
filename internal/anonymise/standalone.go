package anonymise

import (
	"context"
	"regexp"
	"sync"

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
// hints), as whole words; the Go patterns still find addresses, URLs,
// phone numbers and the rest. Names that appear only in prose are missed.
type PatternsOnly struct{}

// Detect implements Detector.
func (PatternsOnly) Detect(_ context.Context, text string, names []string, _ float64) ([]Span, error) {
	var spans []Span
	for _, n := range names {
		if n == "" {
			continue
		}
		re, err := regexp.Compile(`(?i)\b` + regexp.QuoteMeta(n) + `\b`)
		if err != nil {
			continue
		}
		for _, m := range re.FindAllStringIndex(text, -1) {
			spans = append(spans, Span{Start: m[0], End: m[1], Entity: EntityPerson})
		}
	}
	return spans, nil
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
