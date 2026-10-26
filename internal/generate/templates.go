package generate

import (
	"context"
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/martinsuchenak/exemplar/internal/apperr"
	"github.com/martinsuchenak/exemplar/internal/emailtemplate"
	"github.com/martinsuchenak/exemplar/internal/fixture"
	"github.com/martinsuchenak/exemplar/internal/principal"
	"github.com/martinsuchenak/exemplar/internal/synth"
)

// template is a template email and the people it is about, sender first.
type template struct {
	fixture *fixture.Fixture
	data    []byte
	people  []templatePerson
}

// templatePerson is one person linked to a template, with every role they
// have in it.
type templatePerson struct {
	person synth.Person
	roles  []string
}

// rolePriority orders a template's people, so the sender is swapped first
// and wins where two swaps would replace the same text.
var rolePriority = map[string]int{"from": 0, "sender": 1, "to": 2, "cc": 3, "bcc": 4, "reply-to": 5}

// normaliseTemplates validates a request that names template emails.
func normaliseTemplates(req *Request) error {
	if req.Kind != KindEmails {
		return fmt.Errorf("%w: templates (from) only make emails", apperr.ErrInvalid)
	}
	if len(req.Shapes) > 0 {
		return fmt.Errorf("%w: give shapes or templates (from), not both", apperr.ErrInvalid)
	}
	seen := map[string]bool{}
	ids := make([]string, 0, len(req.From))
	for _, id := range req.From {
		id = strings.TrimSpace(id)
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		ids = append(ids, id)
	}
	if len(ids) == 0 || len(ids) > MaxTemplates {
		return fmt.Errorf("%w: from must name 1-%d template emails", apperr.ErrInvalid, MaxTemplates)
	}
	req.From = ids
	if req.People < 0 || req.People > MaxPeople {
		return fmt.Errorf("%w: people must be 1-%d", apperr.ErrInvalid, MaxPeople)
	}
	return nil
}

// loadTemplates reads each template email the caller may read. Real-derived
// emails must be approved, even for admins: generating copies their wording
// out of quarantine.
func (s *Service) loadTemplates(ctx context.Context, actor principal.Principal, ids []string) ([]*template, error) {
	out := make([]*template, 0, len(ids))
	for _, id := range ids {
		f, err := s.fixtures.Get(ctx, actor, id)
		if err != nil {
			return nil, err
		}
		if f.Kind != fixture.KindEmail || f.Format != "eml" {
			return nil, fmt.Errorf("%w: template %s is not an .eml email", apperr.ErrInvalid, id)
		}
		if f.RealDerived() && f.ReviewState != fixture.ReviewApproved {
			return nil, fmt.Errorf("%w: template %s is real-derived and not approved", apperr.ErrInvalid, id)
		}
		if len(f.People) == 0 {
			return nil, fmt.Errorf("%w: template %s has no linked people to swap", apperr.ErrInvalid, id)
		}
		t := &template{fixture: f}
		if t.people, err = s.templatePeople(ctx, actor, f.People); err != nil {
			return nil, err
		}
		rc, _, err := s.fixtures.Open(ctx, actor, id)
		if err != nil {
			return nil, err
		}
		t.data, err = io.ReadAll(rc)
		_ = rc.Close()
		if err != nil {
			return nil, fmt.Errorf("reading template %s: %w", id, err)
		}
		out = append(out, t)
	}
	return out, nil
}

// templatePeople groups links by person, ordered by their first role.
func (s *Service) templatePeople(ctx context.Context, actor principal.Principal, links []fixture.PersonLink) ([]templatePerson, error) {
	links = append([]fixture.PersonLink(nil), links...)
	sort.SliceStable(links, func(i, j int) bool { return priority(links[i].Role) < priority(links[j].Role) })
	var out []templatePerson
	index := map[string]int{}
	for _, l := range links {
		if i, ok := index[l.PersonID]; ok {
			out[i].roles = append(out[i].roles, l.Role)
			continue
		}
		p, err := s.persons.Get(ctx, actor, l.PersonID)
		if err != nil {
			return nil, err
		}
		index[l.PersonID] = len(out)
		out = append(out, templatePerson{person: p.Person, roles: []string{l.Role}})
	}
	return out, nil
}

func priority(role string) int {
	if p, ok := rolePriority[role]; ok {
		return p
	}
	return len(rolePriority)
}

// sizePool makes sure the request picks enough people for its largest
// template: by default twice as many (at least 10), so emails vary.
func sizePool(req *Request, templates []*template) error {
	need := 0
	for _, t := range templates {
		need = max(need, len(t.people))
	}
	if req.People == 0 {
		req.People = min(MaxPeople, max(10, 2*need))
	}
	if req.People < need {
		return fmt.Errorf("%w: a template has %d people, so people must be at least %d", apperr.ErrInvalid, need, need)
	}
	return nil
}

// buildFromTemplates renders Count emails, cycling through the templates.
func buildFromTemplates(g *synth.Gen, req Request, templates []*template, pool []synth.Person) ([]item, error) {
	prefix := fmt.Sprintf("g%06x", req.Seed&0xffffff)
	items := make([]item, 0, req.Count)
	for i := 0; i < req.Count; i++ {
		t := templates[i%len(templates)]
		swaps := swapPeople(g, t.people, pool)
		r, err := emailtemplate.Render(t.data, swaps, g)
		if err != nil {
			return nil, fmt.Errorf("rendering template %s: %w", t.fixture.ID, err)
		}
		var links []fixture.PersonLink
		for k, sw := range swaps {
			for _, role := range t.people[k].roles {
				links = append(links, fixture.PersonLink{PersonID: sw.To.ID, Role: role})
			}
		}
		slug := synth.Slug(r.Subject)
		if slug == "" {
			slug = "email"
		}
		scenario := req.Scenario
		if scenario == "" {
			scenario = t.fixture.Scenario
		}
		if scenario == "" {
			scenario = "template"
		}
		items = append(items, item{
			name: fmt.Sprintf("%s-%03d-%s.eml", prefix, i+1, slug), format: "eml",
			scenario: scenario, tags: []string{"template"}, people: links, data: r.Data,
			generator:   fmt.Sprintf("template:%s seed=%d", t.fixture.ID, req.Seed),
			ingestBatch: t.fixture.IngestBatch,
		})
	}
	return items, nil
}

// swapPeople chooses a pool person for each template person. People from
// one company in the template get people from one company where the pool
// allows, and different companies stay different. The template's own people
// are avoided while others are free.
func swapPeople(g *synth.Gen, people []templatePerson, pool []synth.Person) []emailtemplate.Swap {
	inTemplate := map[string]bool{}
	for _, tp := range people {
		inTemplate[tp.person.ID] = true
	}
	used := map[string]bool{}
	companies := map[string]string{} // template company → pool company
	taken := map[string]bool{}       // pool companies already standing in
	swaps := make([]emailtemplate.Swap, len(people))
	for i, tp := range people {
		var free, fresh []synth.Person
		for _, p := range pool {
			if used[p.ID] {
				continue
			}
			free = append(free, p)
			if !inTemplate[p.ID] {
				fresh = append(fresh, p)
			}
		}
		if len(fresh) > 0 {
			free = fresh
		}
		var preferred []synth.Person
		target, mapped := companies[tp.person.Company]
		for _, p := range free {
			if (mapped && p.Company == target) || (!mapped && !taken[p.Company]) {
				preferred = append(preferred, p)
			}
		}
		if len(preferred) > 0 {
			free = preferred
		}
		chosen := free[g.Intn(len(free))]
		used[chosen.ID] = true
		if !mapped {
			companies[tp.person.Company] = chosen.Company
			taken[chosen.Company] = true
		}
		swaps[i] = emailtemplate.Swap{From: tp.person, To: chosen}
	}
	return swaps
}
