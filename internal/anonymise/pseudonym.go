package anonymise

import (
	"context"
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"unicode"

	"github.com/martinsuchenak/exemplar/internal/synth"
	"github.com/martinsuchenak/exemplar/internal/textswap"
)

// Registry gives the engine fake people and name tokens that last across
// runs. person.Identities implements it; hashes are Key.Hash values.
type Registry interface {
	Token(ctx context.Context, hash, fake string) (string, error)
	LookupToken(ctx context.Context, hash string) (string, bool, error)
	Person(ctx context.Context, hashes []string, p synth.Person) (synth.Person, error)
}

// Name token roles.
const (
	roleFirst = "first"
	roleLast  = "last"
)

var (
	nameLocalRe     = regexp.MustCompile(`^[A-Za-z]+([._-][A-Za-z]+){0,2}$`)
	wordRe          = regexp.MustCompile(`[A-Za-zÀ-ÿ][A-Za-zÀ-ÿ'’\-]*`)
	companySuffixRe = regexp.MustCompile(`(?i)\s+(pty\.?\s*ltd\.?|limited|ltd\.?|inc\.?|llc|group|holdings)\s*$`)
	messageIDRe     = regexp.MustCompile(`<([^<>\s]+)>`)
)

// nameToken returns the fake for one real name token in role, the same in
// every email and run: the registry's stored fake, or a new keyed one. A
// fake never equals its original, even though the pools hold real names.
func (e *Engine) nameToken(ctx context.Context, role, token string) (string, error) {
	g := synth.New(e.key.Seed("token:"+role, token))
	fake := token
	for strings.EqualFold(fake, token) {
		if role == roleLast {
			fake = g.LastName()
		} else {
			fake = g.FirstName()
		}
	}
	return e.registry.Token(ctx, e.key.Hash("token:"+role, token), fake)
}

// fakeName replaces a detected person name word by word. Known tokens keep
// their mapping; a lone unknown word is treated as a first name.
func (e *Engine) fakeName(ctx context.Context, value string) (string, error) {
	words := wordRe.FindAllString(value, -1)
	named := 0
	var err error
	out := wordRe.ReplaceAllStringFunc(value, func(w string) string {
		if err != nil || e.profile.isStop(w) {
			return w
		}
		named++
		var fake string
		switch {
		case named == 1 && len(words) > 1:
			fake, err = e.nameToken(ctx, roleFirst, w)
		case len(words) == 1:
			fake, err = e.loneToken(ctx, w)
		default:
			fake, err = e.nameToken(ctx, roleLast, w)
		}
		return matchCase(w, fake)
	})
	return out, err
}

// loneToken maps a single name word: as a first name if one is known,
// else as a last name if known, else as a new first name.
func (e *Engine) loneToken(ctx context.Context, w string) (string, error) {
	for _, role := range []string{roleFirst, roleLast} {
		if v, ok, err := e.registry.LookupToken(ctx, e.key.Hash("token:"+role, w)); err != nil || ok {
			return v, err
		}
	}
	return e.nameToken(ctx, roleFirst, w)
}

// companyName returns the fake company for a normalised key; the domain and
// any written form of the same company share it.
func (e *Engine) companyName(key string) string {
	return synth.New(e.key.Seed("company", key)).CompanyName()
}

func companyKey(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// fakeCompany keeps any company suffix: "Acme Plumbing Pty Ltd" becomes
// "<fake> Pty Ltd"; "acmeplumbing" stays one lower-case word.
func (e *Engine) fakeCompany(value string) string {
	suffix := companySuffixRe.FindString(value)
	base := strings.TrimSuffix(value, suffix)
	fake := synth.CompanyStem(e.companyName(companyKey(base)))
	if companyKey(fake) == companyKey(base) {
		fake = synth.CompanyStem(e.companyName(companyKey(base) + "#"))
	}
	if !strings.Contains(base, " ") && strings.ToLower(base) == base {
		fake = companyKey(fake)
	}
	return fake + suffix
}

// scrubLocal replaces profile terms and known company names inside a local
// part that is otherwise kept ("support-acme" or "acme.systems-1793025610" in
// a DMARC report id), as compact lower-case fakes.
func (e *Engine) scrubLocal(local string) string {
	loose := map[string]string{}
	for c := range e.companies {
		loose[c] = companyKey(e.fakeCompany(c))
	}
	for _, t := range e.profile.Terms {
		if t = strings.TrimSpace(t); t != "" {
			loose[strings.ToLower(t)] = companyKey(e.fakeCompany(t))
		}
	}
	if len(loose) == 0 {
		return local
	}
	r, err := textswap.NewSets(textswap.Sets{Loose: loose})
	if err != nil {
		return local
	}
	return r.Apply(local)
}

// fakeDomain maps a domain to its fake company's domain, keeping subdomains:
// mail.acme.com.au becomes mail.<company>.example. Kept domains stay.
func (e *Engine) fakeDomain(d string) string {
	d = strings.TrimSuffix(d, ".")
	if d == "" || !strings.Contains(d, ".") || e.profile.keepsDomain(d) {
		return d
	}
	sub, registrable := e.profile.splitDomain(d)
	base := synth.CompanyFor(e.companyName(companyKey(registrable))).Domain
	for i, label := range sub {
		sub[i] = e.fakeLabel(label)
	}
	return strings.Join(append(sub, base), ".")
}

// genericLabels are subdomain labels that say what a host does, not whose it
// is; they are kept so hosts still look like mail, web or API hosts. A
// trailing number ("mail2", "mx1") does not change that.
var genericLabels = set([]string{
	"www", "mail", "smtp", "imap", "pop", "mx", "webmail", "email", "e", "em", "m", "mta", "out",
	"outbound", "inbound", "reply", "replies", "bounce", "bounces", "news", "newsletter", "lists",
	"list", "info", "go", "app", "apps", "api", "portal", "support", "help", "docs", "cdn",
	"static", "assets", "img", "images", "media", "files", "link", "links", "click", "clicks",
	"track", "tracking", "t", "s", "o", "md", "login", "auth", "sso", "id", "my", "secure",
	"shop", "store", "blog", "dev", "test", "staging", "prod", "au", "nz", "uk", "us", "eu",
})

// fakeLabel keeps a generic subdomain label and replaces any other (a
// client, product or system name such as "acme-prod") with a fake one, the
// same every time.
func (e *Engine) fakeLabel(label string) string {
	if genericLabels[strings.ToLower(strings.TrimRight(label, "0123456789"))] {
		return label
	}
	return companyKey(e.fakeCompany(strings.ToLower(label)))
}

// fakeEmail maps an address found in text. Addresses from headers resolve to
// their registry person; others keep a name-like shape or become user<hash>.
func (e *Engine) fakeEmail(ctx context.Context, addr string) (string, error) {
	if id, ok := e.identities[strings.ToLower(addr)]; ok {
		return id.fake, nil
	}
	i := strings.LastIndex(addr, "@")
	if i < 0 {
		return addr, nil
	}
	local, domain := addr[:i], addr[i+1:]
	switch {
	case e.profile.keepsLocal(local):
		local = e.scrubLocal(local)
	case nameLocalRe.MatchString(local):
		var err error
		local = letterRe.ReplaceAllStringFunc(local, func(w string) string {
			if err != nil {
				return w
			}
			var fake string
			fake, err = e.loneToken(ctx, w)
			return strings.ToLower(fake)
		})
		if err != nil {
			return "", err
		}
		local = slugLocal(local)
	default:
		local = "user" + e.key.Hex("local", local, 6)
	}
	return local + "@" + e.fakeDomain(domain), nil
}

// slugLocal keeps an address local part to letters, digits and . _ -.
func slugLocal(s string) string {
	return strings.Map(func(r rune) rune {
		if r < 128 && (unicode.IsLetter(r) || unicode.IsDigit(r) || r == '.' || r == '_' || r == '-') {
			return r
		}
		return -1
	}, s)
}

// fakeMessageIDs maps every <id@domain> in v; the same id always maps the
// same way, so threads stay linked.
func (e *Engine) fakeMessageIDs(v string) string {
	return messageIDRe.ReplaceAllStringFunc(v, func(m string) string {
		return "<" + e.fakeID(strings.Trim(m, "<>")) + ">"
	})
}

func (e *Engine) fakeID(id string) string {
	i := strings.LastIndex(id, "@")
	if i < 0 {
		return e.key.Hex("msgid", id, 16)
	}
	return e.key.Hex("msgid", id[:i], 16) + "@" + e.fakeDomain(id[i+1:])
}

// fakeURL maps the host and replaces any path and query with a hash.
func (e *Engine) fakeURL(ctx context.Context, value string) (string, error) {
	raw := value
	if !strings.Contains(raw, "://") && !strings.HasPrefix(strings.ToLower(raw), "mailto:") {
		raw = "http://" + raw
	}
	u, err := url.Parse(raw)
	if err != nil {
		return "https://anonymised.example/", nil
	}
	if strings.EqualFold(u.Scheme, "mailto") {
		addr, err := e.fakeEmail(ctx, u.Opaque)
		return "mailto:" + addr, err
	}
	host := u.Hostname()
	if e.profile.keepsDomain(host) {
		return value, nil
	}
	path := u.Path
	if (u.Path != "" && u.Path != "/") || u.RawQuery != "" {
		path = "/" + e.key.Hex("path", u.Path+"?"+u.RawQuery, 10)
	}
	out := (&url.URL{Scheme: u.Scheme, Host: e.fakeDomain(host), Path: path}).String()
	if !strings.Contains(value, "://") {
		out = strings.SplitN(out, "://", 2)[1]
	}
	return out, nil
}

// fakeDigits replaces digits, keeping separators and the leading digits up to
// the first non-zero one, so "0412 345 678" stays a mobile (04), "(02) 9…"
// keeps its area code and amounts keep their magnitude.
func (e *Engine) fakeDigits(kind, value string) string {
	digits := strings.Map(func(r rune) rune {
		if r >= '0' && r <= '9' {
			return r
		}
		return -1
	}, value)
	stream := e.key.Hex(kind, digits, 64)
	var b strings.Builder
	leading, i := true, 0
	for _, r := range value {
		if r < '0' || r > '9' {
			b.WriteRune(r)
			continue
		}
		if leading {
			b.WriteRune(r)
			leading = r == '0'
		} else {
			b.WriteByte('0' + stream[i%len(stream)]%10)
			i++
		}
	}
	return b.String()
}

// fake returns the replacement for a detected value of entity.
func (e *Engine) fake(ctx context.Context, entity, value string) (string, error) {
	switch {
	case entity == EntityPerson:
		return e.fakeName(ctx, value)
	case entity == EntityEmail:
		return e.fakeEmail(ctx, value)
	case entity == EntityDomain:
		return e.fakeDomain(value), nil
	case entity == EntityContentID:
		return e.fakeID(value), nil
	case entity == EntityURL:
		return e.fakeURL(ctx, value)
	case entity == EntityOrganization || entity == EntityCompany || entity == EntityTerm:
		return e.fakeCompany(value), nil
	case entity == EntityStreet:
		return synth.New(e.key.Seed("street", value)).Address().Street, nil
	case digitEntities[entity]:
		return e.fakeDigits(entity, value), nil
	default:
		return fmt.Sprintf("[%s-%s]", strings.ToLower(entity), e.key.Hex(entity, value, 6)), nil
	}
}

// matchCase gives replacement the case style of original.
func matchCase(original, replacement string) string {
	switch {
	case len(original) > 1 && strings.ToUpper(original) == original && strings.ToLower(original) != original:
		return strings.ToUpper(replacement)
	case strings.ToLower(original) == original:
		return strings.ToLower(replacement)
	default:
		return replacement
	}
}
