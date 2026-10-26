// Package textswap replaces many strings in text in one pass: longest match
// first, case-insensitively, only at word boundaries, and HTML-escaped forms
// too. A domain (acme.example) is also replaced after '@' or '.', so
// addresses and subdomains at it change with it. One pass means a replacement is never itself replaced again, so
// swapping A for B and B for C turns "A B" into "B C", not "C C".
package textswap

import (
	"regexp"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Replacer applies a fixed set of replacements. The zero value replaces
// nothing.
type Replacer struct {
	lookup map[string]entry
	// written holds every key as written, so a match in the same case as one
	// of several case variants ("Acme", "acme") gets that variant's
	// replacement.
	written map[string]entry
	re      *regexp.Regexp
	// keys are every key, longest first, for falling back to a shorter
	// match where the longest is rejected.
	keys []string
}

type entry struct {
	key, to string
	mode    mode
}

// mode is how a key matches.
type mode int

const (
	// anyCase keys match in any case, at word boundaries.
	anyCase mode = iota
	// loose keys match in any case wherever no letter or digit is next to
	// them, so also inside identifiers: "acme" in "acme-prod" or
	// "logo_acme_2020".
	loose
	// exact keys match only as written or in all capitals.
	exact
)

// Sets are the replacements of a Replacer, by how they match. Where several
// hold a key, Loose wins over AnyCase, and both over Exact.
type Sets struct {
	AnyCase map[string]string
	// Loose suits distinctive single words such as a company name, which
	// identify someone wherever they appear.
	Loose map[string]string
	// Exact suits names that are also words ("Grace", "Price"): replaced as
	// names but not as "grace" or "price".
	Exact map[string]string
}

// New returns a Replacer for pairs (original → replacement), matched in any
// case. Empty originals are ignored.
func New(pairs map[string]string) (*Replacer, error) {
	return NewSets(Sets{AnyCase: pairs})
}

// NewCased is New plus exact pairs.
func NewCased(anyCase, exact map[string]string) (*Replacer, error) {
	return NewSets(Sets{AnyCase: anyCase, Exact: exact})
}

// NewSets returns a Replacer for every set.
func NewSets(sets Sets) (*Replacer, error) {
	r := &Replacer{lookup: map[string]entry{}, written: map[string]entry{}}
	esc := strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;")
	var keys []string
	put := func(v, to string, m mode) {
		e := entry{key: v, to: to, mode: m}
		if _, ok := r.written[v]; !ok {
			r.written[v] = e
		}
		if _, ok := r.lookup[strings.ToLower(v)]; !ok {
			r.lookup[strings.ToLower(v)] = e
			keys = append(keys, v)
		}
	}
	add := func(set map[string]string, m mode) {
		// Sorted, so which case variant answers for the others does not
		// depend on map order.
		vs := make([]string, 0, len(set))
		for v := range set {
			if v != "" {
				vs = append(vs, v)
			}
		}
		sort.Strings(vs)
		for _, v := range vs {
			put(v, set[v], m)
			if ev := esc.Replace(v); ev != v {
				put(ev, esc.Replace(set[v]), m)
			}
		}
	}
	add(sets.Loose, loose)
	add(sets.AnyCase, anyCase)
	add(sets.Exact, exact)
	if len(keys) == 0 {
		return r, nil
	}
	sort.Slice(keys, func(i, j int) bool {
		if len(keys[i]) != len(keys[j]) {
			return len(keys[i]) > len(keys[j])
		}
		return keys[i] < keys[j]
	})
	quoted := make([]string, len(keys))
	for i, k := range keys {
		quoted[i] = regexp.QuoteMeta(k)
	}
	re, err := regexp.Compile("(?i)(?:" + strings.Join(quoted, "|") + ")")
	if err != nil {
		return nil, err
	}
	r.re, r.keys = re, keys
	return r, nil
}

// Apply returns s with every replacement made. An all-capitals original gets
// an all-capitals replacement.
func (r *Replacer) Apply(s string) string {
	if r == nil || r.re == nil {
		return s
	}
	var b strings.Builder
	last, i := 0, 0
	for i < len(s) {
		loc := r.re.FindStringIndex(s[i:])
		if loc == nil {
			break
		}
		start := i + loc[0]
		end, to, ok := r.accept(s, start, i+loc[1])
		if !ok {
			// Nothing acceptable starts here; try from the next character.
			_, n := utf8.DecodeRuneInString(s[start:])
			i = start + n
			continue
		}
		b.WriteString(s[last:start])
		b.WriteString(to)
		last, i = end, end
	}
	b.WriteString(s[last:])
	return b.String()
}

// accept returns the replacement for the match s[start:end], or for the
// longest shorter key at start when that one is rejected (wrong boundary, or
// an exact key in another case).
func (r *Replacer) accept(s string, start, end int) (int, string, bool) {
	if to, ok := r.check(s, start, end); ok {
		return end, to, true
	}
	for _, k := range r.keys {
		e := start + len(k)
		if e >= end || e > len(s) || !strings.EqualFold(s[start:e], k) {
			continue
		}
		if to, ok := r.check(s, start, e); ok {
			return e, to, true
		}
	}
	return 0, "", false
}

func (r *Replacer) check(s string, start, end int) (string, bool) {
	orig := s[start:end]
	e, ok := r.written[orig]
	if !ok {
		e, ok = r.lookup[strings.ToLower(orig)]
	}
	if !ok {
		return "", false
	}
	if e.mode == loose {
		if !looseBoundary(s, start, end) {
			return "", false
		}
	} else if !boundaryBefore(s, start, domainRe.MatchString(orig)) || !boundaryAfter(s, end) {
		return "", false
	}
	upper := strings.ToUpper(orig) == orig && strings.ToLower(orig) != orig
	if e.mode == exact && orig != e.key && !upper {
		return "", false
	}
	if upper {
		return strings.ToUpper(e.to), true
	}
	return e.to, true
}

// looseBoundary: no letter or digit right before or after the match.
func looseBoundary(s string, start, end int) bool {
	if start > 0 {
		r, _ := utf8.DecodeLastRuneInString(s[:start])
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			return false
		}
	}
	if end < len(s) {
		r, _ := utf8.DecodeRuneInString(s[end:])
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			return false
		}
	}
	return true
}

func isWord(r rune) bool {
	return unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_'
}

// domainRe matches a domain name: dotted labels, the last starting with a
// letter (so 1234.56 is not one).
var domainRe = regexp.MustCompile(`(?i)^[a-z0-9-]+(?:\.[a-z0-9-]+)*\.[a-z][a-z0-9-]*$`)

// boundaryBefore: not preceded by a word character or '-', nor by '@' or
// '.' unless the match is a domain.
func boundaryBefore(s string, i int, domain bool) bool {
	if i == 0 {
		return true
	}
	r, _ := utf8.DecodeLastRuneInString(s[:i])
	if r == '@' || r == '.' {
		return domain
	}
	return !isWord(r) && r != '-'
}

// boundaryAfter: not followed by a word character, '@', '-' or '.' + word.
func boundaryAfter(s string, i int) bool {
	if i >= len(s) {
		return true
	}
	r, n := utf8.DecodeRuneInString(s[i:])
	if isWord(r) || r == '@' || r == '-' {
		return false
	}
	if r == '.' && i+n < len(s) {
		next, _ := utf8.DecodeRuneInString(s[i+n:])
		return !isWord(next)
	}
	return true
}
