// Package emailtemplate turns one email into many: it swaps the people an
// email is about, with their companies, for other people, and gives the
// message fresh Message-IDs. Structure, encodings, wording and dates stay as
// they are, so an approved real email keeps its real shape. It is pure: the
// caller chooses the people and supplies the seeded generator.
package emailtemplate

import (
	"bytes"
	"fmt"
	"net/mail"
	"regexp"
	"strings"

	"github.com/martinsuchenak/exemplar/internal/mimetree"
	"github.com/martinsuchenak/exemplar/internal/synth"
	"github.com/martinsuchenak/exemplar/internal/textswap"
)

// Swap replaces one person, and their company, with another.
type Swap struct {
	From synth.Person
	To   synth.Person
}

// Result is one rendered email.
type Result struct {
	Data []byte
	// Subject is the rendered subject, decoded.
	Subject string
}

var (
	idRe = regexp.MustCompile(`<([^<>@\s]+)@([^<>\s]+)>`)
	// untouched headers carry structure, not content.
	untouched = map[string]bool{
		"content-type": true, "content-transfer-encoding": true, "content-disposition": true,
		"content-id": true, "mime-version": true, "date": true,
	}
	idHeaders  = map[string]bool{"message-id": true, "in-reply-to": true, "references": true}
	reportType = map[string]bool{"message/delivery-status": true, "message/disposition-notification": true}
)

// Render applies swaps to src. Earlier swaps win where two would replace the
// same text (two people sharing a surname, say), so callers list the sender
// first. g draws the new Message-IDs.
func Render(src []byte, swaps []Swap, g *synth.Gen) (*Result, error) {
	if _, err := mail.ReadMessage(bytes.NewReader(src)); err != nil {
		return nil, fmt.Errorf("the template is not a valid email: %w", err)
	}
	root, err := mimetree.Parse(src)
	if err != nil {
		return nil, fmt.Errorf("reading the template: %w", err)
	}
	anyCase, exact := Pairs(swaps)
	rep, err := textswap.NewCased(anyCase, exact)
	if err != nil {
		return nil, err
	}
	r := &renderer{rep: rep, domains: anyCase, g: g, ids: map[string]string{}}
	if err := r.message(root); err != nil {
		return nil, err
	}
	return &Result{Data: root.Bytes(), Subject: mimetree.DecodeHeader(root.Header.Get("Subject"))}, nil
}

// Pairs lists the text replacements for swaps: each person's name forms,
// address, domain, phone, job title and street, and their company's name,
// stem, domain, ABN and phone. A value that would not change is left out.
//
// exact holds the proper nouns (names, job titles, company names, streets),
// matched only as written or in all capitals, since many names are also
// words: "Grace Price" must not turn "the grace period" into "the Ann
// period". anyCase holds addresses, domains and numbers.
func Pairs(swaps []Swap) (anyCase, exact map[string]string) {
	anyCase, exact = map[string]string{}, map[string]string{}
	add := func(m map[string]string, from, to string) {
		from, to = strings.TrimSpace(from), strings.TrimSpace(to)
		if len(from) < 2 || to == "" || strings.EqualFold(from, to) {
			return
		}
		if _, ok := m[from]; !ok {
			m[from] = to
		}
	}
	for _, s := range swaps {
		f, t := s.From, s.To
		add(anyCase, f.Email, t.Email)
		add(exact, f.Name(), t.Name())
		add(exact, f.LastName+", "+f.FirstName, t.LastName+", "+t.FirstName)
		add(exact, f.FirstName, t.FirstName)
		add(exact, f.LastName, t.LastName)
		add(anyCase, domain(f.Email), domain(t.Email))
		add(anyCase, f.Phone, t.Phone)
		add(exact, f.JobTitle, t.JobTitle)
		add(exact, f.Address.Street, t.Address.Street)
		if f.Company == "" || t.Company == "" {
			continue
		}
		fc, tc := synth.CompanyFor(f.Company), synth.CompanyFor(t.Company)
		add(exact, f.Company, t.Company)
		add(exact, synth.CompanyStem(f.Company), synth.CompanyStem(t.Company))
		add(anyCase, compact(f.Company), compact(t.Company))
		add(anyCase, fc.Domain, tc.Domain)
		add(anyCase, fc.ABN, tc.ABN)
		add(anyCase, fc.Phone, tc.Phone)
	}
	return anyCase, exact
}

func domain(addr string) string {
	if i := strings.LastIndex(addr, "@"); i >= 0 {
		return strings.ToLower(addr[i+1:])
	}
	return ""
}

// compact is a company stem as a domain label or slug writes it:
// "Noosa Engineering Pty Ltd" → "noosaengineering".
func compact(company string) string {
	return strings.ToLower(strings.Join(strings.Fields(synth.CompanyStem(company)), ""))
}

type renderer struct {
	rep *textswap.Replacer
	// domains maps old domains (among other any-case pairs) to new ones.
	domains map[string]string
	g       *synth.Gen
	// ids maps each old Message-ID to its new one, so threading headers
	// inside the email still agree.
	ids map[string]string
}

// message rewrites a message's headers and every leaf below it.
func (r *renderer) message(msg *mimetree.Part) error {
	for i, field := range msg.Header.Fields {
		name := strings.ToLower(field.Name)
		switch {
		case untouched[name]:
		case idHeaders[name]:
			msg.Header.Fields[i].Value = idRe.ReplaceAllStringFunc(field.Value, r.newID)
		default:
			value := mimetree.DecodeHeader(field.Value)
			if v := r.rep.Apply(value); v != value {
				msg.Header.Fields[i].Value = mimetree.EncodeHeader(v)
			}
		}
	}
	return r.leaves(msg)
}

func (r *renderer) leaves(p *mimetree.Part) error {
	if p.IsMultipart() {
		for _, c := range p.Parts {
			if err := r.leaves(c); err != nil {
				return err
			}
		}
		return nil
	}
	mt, _ := p.MediaType()
	switch {
	case mt == "message/rfc822":
		raw, err := p.Decoded()
		if err != nil {
			return err
		}
		inner, err := mimetree.Parse(raw)
		if err != nil {
			return err
		}
		if err := r.message(inner); err != nil {
			return err
		}
		if p.Encoding() == "base64" {
			p.SetBytes(inner.Bytes())
		} else {
			p.Body = inner.Bytes()
		}
	case reportType[mt]:
		raw, err := p.Decoded()
		if err != nil {
			return err
		}
		if v := r.rep.Apply(string(raw)); v != string(raw) {
			p.SetBytes([]byte(v))
		}
	case strings.HasPrefix(mt, "text/"):
		if strings.HasPrefix(strings.ToLower(p.Header.Get("Content-Disposition")), "attachment") {
			return nil
		}
		text, err := p.Text()
		if err != nil {
			return err
		}
		if v := r.rep.Apply(text); v != text {
			p.SetText(v)
		}
	}
	return nil
}

// newID maps one <local@domain> Message-ID to a fresh one on the swapped
// domain, the same new ID every time it appears.
func (r *renderer) newID(id string) string {
	if v, ok := r.ids[id]; ok {
		return v
	}
	m := idRe.FindStringSubmatch(id)
	d := strings.ToLower(m[2])
	if to, ok := r.domains[d]; ok {
		d = to
	}
	v := fmt.Sprintf("<%08x%08x@%s>", r.g.Intn(1<<31), r.g.Intn(1<<31), d)
	r.ids[id] = v
	return v
}
