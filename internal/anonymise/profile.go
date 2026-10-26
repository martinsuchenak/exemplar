// Package anonymise turns real emails into fixtures that are safe to share:
// it finds personal and commercial data (with a Presidio sidecar for names and
// Go patterns for the rest), replaces it with consistent fakes from the person
// registry, and keeps everything a mail parser depends on: MIME structure,
// transfer encodings, HTML markup, threading and cid: references. It is a Go
// port of mail-processor's anonymise-corpus.py. It touches no storage itself:
// people and name tokens come through a Registry.
package anonymise

import (
	"embed"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/BurntSushi/toml"
)

//go:embed profiles/*.toml
var builtinProfiles embed.FS

// Profile tunes anonymisation for one source of mail.
type Profile struct {
	Name             string   `toml:"name" json:"name"`
	Description      string   `toml:"description" json:"description"`
	ScoreThreshold   float64  `toml:"score_threshold" json:"score_threshold"`
	KeepDomains      []string `toml:"keep_domains" json:"keep_domains"`
	KeepLocalParts   []string `toml:"keep_local_parts" json:"keep_local_parts"`
	StopWords        []string `toml:"stop_words" json:"stop_words"`
	TwoLevelSuffixes []string `toml:"two_level_suffixes" json:"two_level_suffixes"`

	keepLocal map[string]bool
	stop      map[string]bool
	twoLevel  map[string]bool
}

var profileNameRe = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,63}$`)

// LoadProfiles returns the built-in profiles plus every *.toml file in dir
// (empty for none). A file overrides a built-in profile of the same name.
func LoadProfiles(dir string) (map[string]*Profile, error) {
	out := map[string]*Profile{}
	entries, err := builtinProfiles.ReadDir("profiles")
	if err != nil {
		return nil, err
	}
	for _, e := range entries {
		data, err := builtinProfiles.ReadFile("profiles/" + e.Name())
		if err != nil {
			return nil, err
		}
		p, err := parseProfile(data)
		if err != nil {
			return nil, fmt.Errorf("built-in profile %s: %w", e.Name(), err)
		}
		out[p.Name] = p
	}
	if dir == "" {
		return out, nil
	}
	files, err := filepath.Glob(filepath.Join(dir, "*.toml"))
	if err != nil {
		return nil, err
	}
	for _, f := range files {
		data, err := os.ReadFile(f)
		if err != nil {
			return nil, err
		}
		p, err := parseProfile(data)
		if err != nil {
			return nil, fmt.Errorf("profile %s: %w", f, err)
		}
		out[p.Name] = p
	}
	return out, nil
}

// ProfileNames returns the profile names, sorted.
func ProfileNames(profiles map[string]*Profile) []string {
	names := make([]string, 0, len(profiles))
	for n := range profiles {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

func parseProfile(data []byte) (*Profile, error) {
	var p Profile
	if _, err := toml.Decode(string(data), &p); err != nil {
		return nil, err
	}
	if !profileNameRe.MatchString(p.Name) {
		return nil, fmt.Errorf("name %q must be lower-case letters, digits, '-' or '_'", p.Name)
	}
	if p.ScoreThreshold <= 0 || p.ScoreThreshold > 1 {
		p.ScoreThreshold = 0.5
	}
	p.index()
	return &p, nil
}

func (p *Profile) index() {
	p.keepLocal, p.stop, p.twoLevel = set(p.KeepLocalParts), set(p.StopWords), set(p.TwoLevelSuffixes)
}

func set(values []string) map[string]bool {
	m := make(map[string]bool, len(values))
	for _, v := range values {
		m[strings.ToLower(v)] = true
	}
	return m
}

// keepsDomain reports whether d or a parent of it is kept.
func (p *Profile) keepsDomain(d string) bool {
	d = strings.ToLower(strings.TrimSuffix(d, "."))
	for _, k := range p.KeepDomains {
		k = strings.ToLower(k)
		if d == k || strings.HasSuffix(d, "."+k) {
			return true
		}
	}
	return false
}

// isStop reports whether a word is a stop word.
func (p *Profile) isStop(w string) bool {
	return p.stop[strings.ToLower(w)]
}

// splitDomain separates d into subdomain labels and the registrable label,
// keeping a two-level public suffix together: mail.acme.com.au gives
// ["mail"], "acme".
func (p *Profile) splitDomain(d string) (sub []string, registrable string) {
	labels := strings.Split(strings.ToLower(strings.TrimSuffix(d, ".")), ".")
	if len(labels) < 2 {
		return nil, labels[0]
	}
	suffix := 1
	if len(labels) > 2 && p.twoLevel[strings.Join(labels[len(labels)-2:], ".")] {
		suffix = 2
	}
	return labels[:len(labels)-suffix-1], labels[len(labels)-suffix-1]
}
