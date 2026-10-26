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
	lookup map[string]string
	re     *regexp.Regexp
}

// New returns a Replacer for pairs (original → replacement). Empty originals
// are ignored.
func New(pairs map[string]string) (*Replacer, error) {
	r := &Replacer{lookup: map[string]string{}}
	esc := strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;")
	keys := make([]string, 0, len(pairs)*2)
	for v, to := range pairs {
		if v == "" {
			continue
		}
		r.lookup[strings.ToLower(v)] = to
		keys = append(keys, v)
		if ev := esc.Replace(v); ev != v {
			r.lookup[strings.ToLower(ev)] = esc.Replace(to)
			keys = append(keys, ev)
		}
	}
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
	r.re = re
	return r, nil
}

// Apply returns s with every replacement made. An all-capitals original gets
// an all-capitals replacement.
func (r *Replacer) Apply(s string) string {
	if r == nil || r.re == nil {
		return s
	}
	var b strings.Builder
	last := 0
	for _, m := range r.re.FindAllStringIndex(s, -1) {
		orig := s[m[0]:m[1]]
		if !boundaryBefore(s, m[0], domainRe.MatchString(orig)) || !boundaryAfter(s, m[1]) {
			continue
		}
		b.WriteString(s[last:m[0]])
		to := r.lookup[strings.ToLower(orig)]
		if strings.ToUpper(orig) == orig && strings.ToLower(orig) != orig {
			to = strings.ToUpper(to)
		}
		b.WriteString(to)
		last = m[1]
	}
	b.WriteString(s[last:])
	return b.String()
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
