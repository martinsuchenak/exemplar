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

// Header handling, ported from anonymise-corpus.py. Headers not in
// keepHeaders are dropped: Received, DKIM and ARC signatures,
// X-MS-Exchange-*, X-Originating-IP and the like carry addresses, IPs and
// tenant ids that nothing downstream reads.
var (
	keepHeaders = set([]string{
		"From", "To", "Cc", "Bcc", "Reply-To", "Sender", "Return-Path", "Subject", "Date",
		"Message-ID", "In-Reply-To", "References", "MIME-Version", "Content-Type",
		"Content-Transfer-Encoding", "Content-Disposition", "Content-ID", "Content-Language",
		"Content-Description", "Thread-Topic", "Thread-Index", "Auto-Submitted", "X-Autoreply",
		"X-Autorespond", "X-Auto-Response-Suppress", "Precedence", "List-Id", "List-Unsubscribe",
		"List-Unsubscribe-Post", "List-Post", "List-Help", "Importance", "X-Priority", "X-Mailer",
		"User-Agent", "Disposition-Notification-To", "Accept-Language", "X-MS-Has-Attach",
		"X-MS-TNEF-Correlator",
	})
	addressHeaders = set([]string{"From", "To", "Cc", "Bcc", "Reply-To", "Sender", "Return-Path", "Disposition-Notification-To"})
	idHeaders      = set([]string{"Message-ID", "In-Reply-To", "References"})
	textHeaders    = set([]string{"Subject", "Thread-Topic", "Content-Description", "X-Mailer", "User-Agent"})
	urlHeaders     = set([]string{"List-Unsubscribe", "List-Post", "List-Help", "List-Id"})
	headerRoles    = map[string]string{"from": "from", "sender": "from", "to": "to", "cc": "cc", "bcc": "bcc", "reply-to": "reply-to"}
	reportTypes    = set([]string{"message/delivery-status", "message/disposition-notification"})
	emailRe        = regexp.MustCompile(`[\w.+'-]+@[\w-]+(?:\.[\w-]+)+`)
	letterRe       = regexp.MustCompile(`[A-Za-z]+`)
)

// Engine anonymises a batch of emails with one key, profile, detector and
// registry. Call Prepare with every email of the batch first, then
// Anonymise each. An Engine is not safe for concurrent use.
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

// Output is one anonymised email.
type Output struct {
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

// Prepare reads the address headers of every email in the batch, so that a
// person named in one email's headers is recognised in all of them.
func (e *Engine) Prepare(ctx context.Context, emails [][]byte) error {
	for _, data := range emails {
		root, err := mimetree.Parse(data)
		if err != nil {
			return err
		}
		if err := e.prepareMessage(ctx, root); err != nil {
			return err
		}
	}
	return nil
}

func (e *Engine) prepareMessage(ctx context.Context, msg *mimetree.Part) error {
	for _, f := range msg.Header.Fields {
		if !addressHeaders[strings.ToLower(f.Name)] {
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
				if inner, perr := mimetree.Parse(raw); perr == nil {
					err = e.prepareMessage(ctx, inner)
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

// Anonymise rewrites one email. Prepare must have seen it.
func (e *Engine) Anonymise(ctx context.Context, data []byte) (*Output, error) {
	root, err := mimetree.Parse(data)
	if err != nil {
		return nil, fmt.Errorf("parsing: %w", err)
	}
	if _, err := mail.ReadMessage(bytes.NewReader(data)); err != nil {
		return nil, fmt.Errorf("not a valid email: %w", err)
	}
	f := newFound(e.profile)
	if err := e.collect(ctx, root, f); err != nil {
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
	out := &Output{Replacements: pairs, Entities: f.entities}
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

// collect finds personal data in one message (and messages attached to it).
func (e *Engine) collect(ctx context.Context, msg *mimetree.Part, f *found) error {
	for _, field := range msg.Header.Fields {
		name := strings.ToLower(field.Name)
		value := mimetree.DecodeHeader(field.Value)
		switch {
		case addressHeaders[name]:
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
		case textHeaders[name]:
			if err := e.detect(ctx, value, f); err != nil {
				return err
			}
		}
	}
	var err error
	walkLeaves(msg, func(p *mimetree.Part) {
		if err != nil {
			return
		}
		if cid := strings.Trim(p.Header.Get("Content-ID"), "<> "); cid != "" {
			f.add(cid, EntityContentID)
		}
		mt, _ := p.MediaType()
		switch {
		case mt == "message/rfc822":
			raw, derr := p.Decoded()
			if derr != nil {
				err = derr
				return
			}
			inner, perr := mimetree.Parse(raw)
			if perr != nil {
				err = perr
				return
			}
			err = e.collect(ctx, inner, f)
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
	for v, entity := range f.entities {
		if _, ok := pairs[v]; ok {
			continue
		}
		fake, err := e.fake(ctx, entity, v)
		if err != nil {
			return nil, err
		}
		if fake != v {
			pairs[v] = fake
		}
	}
	return pairs, nil
}
