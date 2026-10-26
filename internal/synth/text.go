package synth

import (
	"fmt"
	"strings"
)

// Profile is the business vocabulary text is written in.
type Profile string

const (
	// ProfileInsurance is commercial insurance and brokerage correspondence.
	ProfileInsurance Profile = "insurance"
	// ProfileManufacturing is fabrication, welding and workshop correspondence.
	ProfileManufacturing Profile = "manufacturing"
)

// Profiles lists every profile.
var Profiles = []Profile{ProfileInsurance, ProfileManufacturing}

// Valid reports whether p is a known profile.
func (p Profile) Valid() bool {
	return p == ProfileInsurance || p == ProfileManufacturing
}

type vocabulary struct {
	subjects, predicates, contexts, openers, closers, titles, qualifiers, labels []string
}

func (p Profile) vocab() vocabulary {
	if p == ProfileManufacturing {
		return vocabulary{mfgSubjects, mfgPredicates, mfgContexts, mfgOpeners, mfgClosers, mfgTitles, mfgQualifiers, mfgLabels}
	}
	return vocabulary{insSubjects, insPredicates, insContexts, insOpeners, insClosers, insTitles, insQualifiers, insLabels}
}

// Sentence returns one grammatical business sentence, sometimes with a
// second, contextual one.
func (g *Gen) Sentence(p Profile) string {
	v := p.vocab()
	s := g.pick(v.subjects) + " " + g.pick(v.predicates) + "."
	if g.Chance(3) {
		s += " " + g.pick(v.contexts)
	}
	return s
}

// Paragraph returns min to min+extra sentences.
func (g *Gen) Paragraph(p Profile, min, extra int) string {
	n := g.Between(min, min+extra)
	if n < 1 {
		n = 1
	}
	parts := make([]string, n)
	for i := range parts {
		parts[i] = g.Sentence(p)
	}
	return strings.Join(parts, " ")
}

// Paragraphs returns n paragraphs.
func (g *Gen) Paragraphs(p Profile, n int) []string {
	out := make([]string, n)
	for i := range out {
		out[i] = g.Paragraph(p, 2, 3)
	}
	return out
}

// Title returns a document title such as "Public Liability - Renewal Notice".
func (g *Gen) Title(p Profile) string {
	v := p.vocab()
	return g.pick(v.qualifiers) + " - " + g.pick(v.titles)
}

// Label returns a short label such as "Claim Assessment".
func (g *Gen) Label(p Profile) string {
	return g.pick(p.vocab().labels)
}

// Subject returns an email subject line.
func (g *Gen) Subject(p Profile) string {
	v := p.vocab()
	switch g.Intn(3) {
	case 0:
		return g.pick(v.labels) + " - " + g.pick(v.qualifiers)
	case 1:
		return fmt.Sprintf("%s (ref %s)", g.pick(v.labels), g.Reference())
	default:
		return g.pick(v.qualifiers) + " " + g.pick(v.titles)
	}
}

// Opening returns an email's first sentence.
func (g *Gen) Opening(p Profile) string {
	return g.pick(p.vocab().openers) + ", " + lowerFirst(g.Sentence(p))
}

// Closing returns an email's last sentence.
func (g *Gen) Closing(p Profile) string {
	return g.pick(p.vocab().closers)
}

// Reference returns a reference number such as "PL-482917".
func (g *Gen) Reference() string {
	prefixes := []string{"PL", "CL", "QU", "JOB", "INV", "PO"}
	return fmt.Sprintf("%s-%06d", g.pick(prefixes), g.Intn(1000000))
}

// Money returns an amount between lo and hi dollars, formatted "$1,234.56".
func (g *Gen) Money(lo, hi int) string {
	cents := g.Between(lo*100, hi*100)
	return formatMoney(cents)
}

func formatMoney(cents int) string {
	whole := fmt.Sprint(cents / 100)
	var b strings.Builder
	for i, r := range whole {
		if i > 0 && (len(whole)-i)%3 == 0 {
			b.WriteByte(',')
		}
		b.WriteRune(r)
	}
	return fmt.Sprintf("$%s.%02d", b.String(), cents%100)
}

func lowerFirst(s string) string {
	if s == "" {
		return s
	}
	// Keep acronyms and names ("The CNC job" stays "the CNC job").
	return strings.ToLower(s[:1]) + s[1:]
}
