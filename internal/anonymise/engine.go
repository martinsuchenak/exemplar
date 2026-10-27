package anonymise

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/mail"
	"regexp"
	"sort"
	"strings"

	"github.com/martinsuchenak/exemplar/internal/mimetree"
	"github.com/martinsuchenak/exemplar/internal/synth"
	"github.com/martinsuchenak/exemplar/internal/textswap"
)

// Which headers are kept, and how each is rewritten, is in headers.go.
var (
	headerRoles = map[string]string{"from": "from", "sender": "from", "to": "to", "cc": "cc", "bcc": "bcc", "reply-to": "reply-to"}
	reportTypes = set([]string{"message/delivery-status", "message/disposition-notification"})
	emailRe     = regexp.MustCompile(`[\w.+'-]+@[\w-]+(?:\.[\w-]+)+`)
	letterRe    = regexp.MustCompile(`[A-Za-z]+`)
)

// Engine anonymises a batch of files with one key, profile, detector and
// registry. Call Prepare with every file of the batch first, then Anonymise
// each. Each format is read and written by its handler (format.go). An
// Engine is not safe for concurrent use.
type Engine struct {
	key      Key
	profile  *Profile
	detector Detector
	registry Registry

	// identities maps a real address (lower-case) to its fake.
	identities map[string]*identity
	// pairs are corpus-wide replacements learned from headers: display
	// names, name tokens and addresses, applied in every email.
	pairs map[string]string
	// companies are the company names learned from address domains
	// ("acme" from acme.com.au), replaced even inside kept local parts.
	companies map[string]bool
	// names are the real names from headers, sent to Presidio so it finds
	// them wherever they appear.
	names map[string]bool
}

type identity struct {
	fake   string
	person synth.Person
}

// New returns an engine.
func New(key Key, profile *Profile, detector Detector, registry Registry) (*Engine, error) {
	if !key.Valid() {
		return nil, errors.New("a valid anonymisation key is required")
	}
	if profile == nil || detector == nil || registry == nil {
		return nil, errors.New("a profile, a detector and a registry are required")
	}
	return &Engine{
		key: key, profile: profile, detector: detector, registry: registry,
		identities: map[string]*identity{}, pairs: map[string]string{}, names: map[string]bool{}, companies: map[string]bool{},
	}, nil
}

// PersonRole links an output email to a registry person.
type PersonRole struct {
	PersonID string `json:"person_id"`
	Role     string `json:"role"`
}

// Output is one anonymised file.
type Output struct {
	Format Format
	// Name is the file's fake name: the format's prefix and a keyed hash
	// of the original name, keeping its extension (email-3f9a1c.eml).
	Name string
	Data []byte
	// Replacements maps original text to its fake. It contains the
	// original personal data: never store or log it.
	Replacements map[string]string
	// Entities maps original text to its entity type (also original data).
	Entities       map[string]string
	DroppedHeaders []string
	Attachments    int
	People         []PersonRole
}

// ReportLine is one replacement. It holds original data: show it to the
// person who sent the file, never store or log it.
type ReportLine struct {
	Entity   string `json:"entity"`
	Original string `json:"original"`
	Fake     string `json:"fake"`
}

// Report lists the replacements, sorted by entity then original. A value
// with no entity came from an email header (a display name or its tokens).
func (o *Output) Report() []ReportLine {
	lines := make([]ReportLine, 0, len(o.Replacements))
	for orig, fake := range o.Replacements {
		entity := o.Entities[orig]
		if entity == "" {
			entity = "HEADER_NAME"
		}
		lines = append(lines, ReportLine{Entity: entity, Original: orig, Fake: fake})
	}
	sort.Slice(lines, func(i, j int) bool {
		if lines[i].Entity != lines[j].Entity {
			return lines[i].Entity < lines[j].Entity
		}
		return lines[i].Original < lines[j].Original
	})
	return lines
}

// emailHandler reads and rewrites RFC 5322 messages.
type emailHandler struct{}

// prepare reads an email's address headers, and those of the emails
// attached to it, so a person named there is recognised in every file.
func (emailHandler) prepare(ctx context.Context, e *Engine, f File) error {
	root, _, err := e.parse(f.Data, 0)
	if err != nil {
		return err
	}
	return e.prepareMessage(ctx, root, 0)
}

// parse reads a message nested depth levels deep (0 for an email, an
// attached message's Depth+1) and cleans its headers. It returns the names
// of the headers clean dropped.
func (e *Engine) parse(data []byte, depth int) (*mimetree.Part, []string, error) {
	root, err := mimetree.ParseAt(data, depth)
	if err != nil {
		return nil, nil, fmt.Errorf("parsing: %w", err)
	}
	return root, e.clean(root, true), nil
}

func (e *Engine) prepareMessage(ctx context.Context, msg *mimetree.Part, depth int) error {
	if depth > mimetree.MaxDepth {
		return fmt.Errorf("message nests attached emails more than %d levels deep", mimetree.MaxDepth)
	}
	for _, f := range msg.Header.Fields {
		if messageHeaderKinds[strings.ToLower(f.Name)] != kindAddress {
			continue
		}
		addrs, err := mail.ParseAddressList(mimetree.DecodeHeader(f.Value))
		if err != nil {
			continue
		}
		for _, a := range addrs {
			if _, err := e.resolve(ctx, a.Name, a.Address); err != nil {
				return err
			}
		}
	}
	var err error
	walkLeaves(msg, func(p *mimetree.Part) {
		if mt, _ := p.MediaType(); err == nil && mt == "message/rfc822" {
			if raw, derr := p.Decoded(); derr == nil {
				if inner, _, perr := e.parse(raw, p.Depth+1); perr == nil {
					err = e.prepareMessage(ctx, inner, depth+1)
				}
			}
		}
	})
	return err
}

// resolve returns the fake for a real address and display name, creating the
// registry person on first sight and learning the name pairs.
func (e *Engine) resolve(ctx context.Context, display, addr string) (*identity, error) {
	addr = strings.TrimSpace(addr)
	key := strings.ToLower(addr)
	if id, ok := e.identities[key]; ok {
		return id, nil
	}
	i := strings.LastIndex(addr, "@")
	if i <= 0 {
		return nil, nil
	}
	local, domain := addr[:i], addr[i+1:]
	// The address's domain is replaced wherever it appears on its own too
	// ("Report domain: acme.com.au" in a subject).
	if fake := e.fakeDomain(domain); fake != domain {
		e.pairs[strings.ToLower(domain)] = fake
		if _, registrable := e.profile.splitDomain(domain); len(registrable) >= 4 {
			e.companies[strings.ToLower(registrable)] = true
		}
	}
	if e.profile.keepsLocal(local) {
		id := &identity{fake: e.scrubLocal(local) + "@" + e.fakeDomain(domain)}
		e.identities[key], e.pairs[addr] = id, id.fake
		return id, nil
	}

	display = strings.TrimSpace(display)
	tokens, lastFirst := nameTokens(display, local)
	var first, last string
	var err error
	if len(tokens) > 0 {
		if first, err = e.nameToken(ctx, roleFirst, tokens[0]); err != nil {
			return nil, err
		}
	}
	if len(tokens) > 1 {
		if last, err = e.nameToken(ctx, roleLast, tokens[len(tokens)-1]); err != nil {
			return nil, err
		}
	}
	g := synth.New(e.key.Seed("person", key))
	if first == "" {
		first = g.FirstName()
	}
	if last == "" {
		last = g.LastName()
	}
	company := ""
	if !e.profile.keepsDomain(domain) {
		_, registrable := e.profile.splitDomain(domain)
		company = e.companyName(companyKey(registrable))
	}
	candidate := g.Person(company)
	candidate.FirstName, candidate.LastName = first, last
	if company == "" {
		candidate.Email = synth.EmailLocal(first, last) + "@" + strings.ToLower(domain)
	} else {
		candidate.Email = synth.EmailLocal(first, last) + "@" + synth.CompanyFor(company).Domain
	}
	hashes := []string{e.key.Hash("email", key)}
	if len(tokens) > 1 {
		hashes = append(hashes, e.key.Hash("name", strings.Join(tokens, " ")))
	}
	p, err := e.registry.Person(ctx, hashes, candidate)
	if err != nil {
		return nil, err
	}

	id := &identity{fake: p.Email, person: p}
	e.identities[key] = id
	e.pairs[addr] = p.Email
	if display != "" && !strings.Contains(display, "@") {
		e.names[display] = true
		fakeDisplay := p.Name()
		if lastFirst {
			fakeDisplay = p.LastName + ", " + p.FirstName
		}
		e.pairs[display] = fakeDisplay
	}
	if len(tokens) > 0 {
		e.pairs[tokens[0]] = p.FirstName
		e.names[tokens[0]] = true
	}
	if len(tokens) > 1 {
		e.pairs[tokens[len(tokens)-1]] = p.LastName
		e.names[tokens[len(tokens)-1]] = true
	}
	return id, nil
}

// nameTokens returns the name words of a display name ("Lane, Sarah" is
// reordered to Sarah, Lane), or of a name-like local part (sarah.lane).
func nameTokens(display, local string) (tokens []string, lastFirst bool) {
	if display != "" && !strings.Contains(display, "@") {
		words := wordRe.FindAllString(strings.ReplaceAll(display, ",", " , "), -1)
		if strings.Contains(display, ",") && len(words) > 1 {
			return append([]string{words[len(words)-1]}, words[:len(words)-1]...), true
		}
		return words, false
	}
	if nameLocalRe.MatchString(local) {
		return letterRe.FindAllString(local, -1), false
	}
	return nil, false
}

// anonymise rewrites one email.
func (emailHandler) anonymise(ctx context.Context, e *Engine, file File) (*Output, error) {
	data := file.Data
	root, dropped, err := e.parse(data, 0)
	if err != nil {
		return nil, err
	}
	if _, err := mail.ReadMessage(bytes.NewReader(data)); err != nil {
		return nil, fmt.Errorf("not a valid email: %w", err)
	}
	f := newFound(e.profile)
	if err := e.collect(ctx, root, f, 0); err != nil {
		return nil, err
	}
	f.dropFragments()
	pairs, err := e.replacements(ctx, f)
	if err != nil {
		return nil, err
	}
	rep, err := textswap.NewSets(e.sets(pairs, f))
	if err != nil {
		return nil, err
	}
	out := &Output{Replacements: pairs, Entities: f.entities, DroppedHeaders: dropped}
	if err := e.rewrite(ctx, root, rep, out, true); err != nil {
		return nil, err
	}
	out.Data = root.Bytes()
	if _, err := mail.ReadMessage(bytes.NewReader(out.Data)); err != nil {
		return nil, fmt.Errorf("anonymised output is not a valid email: %w", err)
	}
	return out, nil
}

// sets splits the replacements by how they match. Profile terms and
// single-word company names (most often learned from an address's domain,
// "acme" from acme.com.au) match loosely, so they are also replaced inside
// identifiers such as "acme-prod" or "logo_acme.png"; everything else
// matches at word boundaries.
func (e *Engine) sets(pairs map[string]string, f *found) textswap.Sets {
	sets := textswap.Sets{AnyCase: map[string]string{}, Loose: map[string]string{}}
	for v, fake := range pairs {
		entity := f.entities[v]
		single := !strings.ContainsAny(v, " \t") && len([]rune(v)) >= 4
		if entity == EntityTerm || (single && (entity == EntityCompany || entity == EntityOrganization)) {
			sets.Loose[v] = fake
		} else {
			sets.AnyCase[v] = fake
		}
	}
	return sets
}

// collect finds personal data in one message (and messages attached to it):
// in every kept header that holds text, as well as in the bodies.
func (e *Engine) collect(ctx context.Context, msg *mimetree.Part, f *found, depth int) error {
	if depth > mimetree.MaxDepth {
		return fmt.Errorf("message nests attached emails more than %d levels deep", mimetree.MaxDepth)
	}
	err := e.collectHeaders(ctx, msg, f)
	msg.Walk(func(p *mimetree.Part) {
		if err != nil {
			return
		}
		if p != msg {
			if err = e.collectHeaders(ctx, p, f); err != nil {
				return
			}
		}
		if p.IsMultipart() {
			// The preamble and epilogue are text around the boundary lines,
			// where mailing-list footers carry addresses no header shows.
			for _, text := range []string{string(p.Preamble), string(p.Epilogue)} {
				if strings.TrimSpace(text) != "" {
					if derr := e.detect(ctx, text, f); derr != nil {
						err = derr
						return
					}
				}
			}
			return
		}
		mt, _ := p.MediaType()
		switch {
		case mt == "message/rfc822":
			raw, derr := p.Decoded()
			if derr != nil {
				err = derr
				return
			}
			inner, _, perr := e.parse(raw, p.Depth+1)
			if perr != nil {
				err = perr
				return
			}
			err = e.collect(ctx, inner, f, depth+1)
		case reportTypes[mt]:
			raw, _ := p.Decoded()
			for _, a := range emailRe.FindAllString(string(raw), -1) {
				f.add(a, EntityEmail)
				f.add(a[strings.LastIndex(a, "@")+1:], EntityDomain)
			}
		case isBodyText(p):
			text, terr := p.Text()
			if terr != nil {
				err = terr
				return
			}
			if mt == "text/html" {
				text = htmlText(text)
			}
			err = e.detect(ctx, text, f)
		}
	})
	return err
}

// collectHeaders finds personal data in one part's headers by their kind:
// the people in address headers, and whatever detect finds in free text.
// The structural headers hold no text of their own once cleaned, and IDs
// and URLs are faked whole.
func (e *Engine) collectHeaders(ctx context.Context, p *mimetree.Part, f *found) error {
	for _, field := range p.Header.Fields {
		value := mimetree.DecodeHeader(field.Value)
		switch messageHeaderKinds[strings.ToLower(field.Name)] {
		case kindAddress:
			addrs, err := mail.ParseAddressList(value)
			if err != nil {
				for _, a := range emailRe.FindAllString(value, -1) {
					f.add(a, EntityEmail)
				}
				continue
			}
			for _, a := range addrs {
				id, err := e.resolve(ctx, a.Name, a.Address)
				if err != nil {
					return err
				}
				f.add(a.Address, EntityEmail)
				domain := a.Address[strings.LastIndex(a.Address, "@")+1:]
				f.add(domain, EntityDomain)
				e.addDomainCompany(domain, f)
				if a.Name != "" && id != nil && id.person.Email != "" {
					f.add(a.Name, EntityPerson)
				}
			}
		case kindText:
			if err := e.detect(ctx, value, f); err != nil {
				return err
			}
		case kindListID:
			if err := e.detect(ctx, urlInAngles.ReplaceAllString(value, " "), f); err != nil {
				return err
			}
		case kindContentID:
			f.add(strings.Trim(value, "<> "), EntityContentID)
		}
	}
	return nil
}

// addDomainCompany adds the company name a sender's domain spells:
// acme-plumbing.com.au gives "Acme Plumbing", caught in signatures where
// there is no Pty Ltd for the patterns to see.
func (e *Engine) addDomainCompany(domain string, f *found) {
	if e.profile.keepsDomain(domain) || !strings.Contains(domain, ".") {
		return
	}
	_, registrable := e.profile.splitDomain(domain)
	words := strings.FieldsFunc(registrable, func(r rune) bool { return r == '-' || r == '_' })
	if len(words) > 1 || len(words) == 1 && len(words[0]) >= 4 {
		f.add(strings.Join(words, " "), EntityCompany)
		if len(words) > 1 {
			f.add(strings.Join(words, ""), EntityCompany)
		}
	}
}

// detect runs the Go patterns and the detector over text.
func (e *Engine) detect(ctx context.Context, text string, f *found) error {
	if strings.TrimSpace(text) == "" {
		return nil
	}
	spans := patternSpans(text)
	names := make([]string, 0, len(e.names))
	for n := range e.names {
		names = append(names, n)
	}
	sort.Strings(names)
	more, err := e.detector.Detect(ctx, text, names, e.profile.ScoreThreshold)
	if err != nil {
		return err
	}
	for _, s := range append(spans, more...) {
		if s.Start < 0 || s.End > len(text) || s.Start >= s.End {
			continue
		}
		value := text[s.Start:s.End]
		if (s.Entity == EntityPerson || s.Entity == EntityOrganization) && !plausibleName(value) {
			continue
		}
		f.add(value, s.Entity)
	}
	return nil
}

// isBodyText reports whether a leaf is text to rewrite in place: any text/*
// that is not an attachment, and calendar invites, which classifiers read.
func isBodyText(p *mimetree.Part) bool {
	mt, _ := p.MediaType()
	if mt == "text/calendar" {
		return true
	}
	return strings.HasPrefix(mt, "text/") && !strings.HasPrefix(strings.ToLower(p.Header.Get("Content-Disposition")), "attachment")
}

// walkLeaves calls fn for the leaves of one message, not descending into
// attached messages (callers recurse into those themselves).
func walkLeaves(p *mimetree.Part, fn func(*mimetree.Part)) {
	if !p.IsMultipart() {
		fn(p)
		return
	}
	for _, c := range p.Parts {
		walkLeaves(c, fn)
	}
}

// replacer applies every replacement at once; see textswap.
type replacer = textswap.Replacer

// replacements returns the replacements for one email: the corpus-wide
// pairs plus a fake for every entity found in it.
func (e *Engine) replacements(ctx context.Context, f *found) (map[string]string, error) {
	for _, t := range e.profile.Terms {
		if t = strings.TrimSpace(t); t != "" {
			f.entities[t] = EntityTerm
		}
	}
	pairs := map[string]string{}
	for v, fake := range e.pairs {
		pairs[v] = fake
	}
	// A term is a company-style name even if a display name made it a
	// person's name too: it always gets the company fake.
	for v, entity := range f.entities {
		if entity == EntityTerm {
			pairs[v] = e.fakeCompany(v)
		}
	}
	for _, v := range fakeOrder(f.entities) {
		if _, ok := pairs[v]; ok {
			continue
		}
		fake, err := e.fake(ctx, f.entities[v], v)
		if err != nil {
			return nil, err
		}
		if fake != v {
			pairs[v] = fake
		}
	}
	return pairs, nil
}

// fakeOrder returns the found values in the order their fakes are made:
// names first, then the rest, each sorted. Fakes share the registry's name
// tokens, so order matters: once "Sarah Lane" maps Lane as a last name, an
// address sarah.lane@… reuses that mapping rather than claiming "lane" as a
// new first name, and the same input always gives the same output.
func fakeOrder(entities map[string]string) []string {
	values := make([]string, 0, len(entities))
	for v := range entities {
		values = append(values, v)
	}
	sort.Slice(values, func(i, j int) bool {
		pi, pj := entities[values[i]] == EntityPerson, entities[values[j]] == EntityPerson
		if pi != pj {
			return pi
		}
		return values[i] < values[j]
	})
	return values
}
