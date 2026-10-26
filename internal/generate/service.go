// Package generate fills datasets with synthetic fixtures: files, emails and
// records about people from the person registry, and emails rendered from
// template emails (see emailtemplate). It decides nothing about
// access itself: the dataset service authorises the dataset, and every item
// is stored through the fixture service like an upload.
package generate

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/binary"
	"fmt"
	"strings"

	"github.com/martinsuchenak/xdal/pkg/xdal"

	"github.com/martinsuchenak/exemplar/internal/apperr"
	"github.com/martinsuchenak/exemplar/internal/audit"
	"github.com/martinsuchenak/exemplar/internal/dataset"
	"github.com/martinsuchenak/exemplar/internal/fixture"
	"github.com/martinsuchenak/exemplar/internal/person"
	"github.com/martinsuchenak/exemplar/internal/principal"
	"github.com/martinsuchenak/exemplar/internal/synth"
)

// Kind is what to generate.
type Kind string

// Kinds of generated content.
const (
	KindFiles   Kind = "files"
	KindEmails  Kind = "emails"
	KindRecords Kind = "records"
)

const (
	// MaxCount caps the items one request asks for.
	MaxCount = 500
	// MaxPeople caps how many people one request involves.
	MaxPeople = 200
	// MaxTemplates caps the template emails one request names.
	MaxTemplates = 50
	// GeneratorName identifies this generator in fixture provenance.
	GeneratorName = "exemplar-synth/1"
	// contentStream separates the content generator's seed from the one
	// used to pick people.
	contentStream = 0x5eed5eed5eed5eed
)

// Request describes what to generate. Empty lists mean "all of them";
// generation cycles through the list, so Count 6 with two formats gives three
// of each. Seed 0 picks a random seed, returned in the Result. The same seed
// with the same people produces the same content.
type Request struct {
	Kind    Kind                 `json:"kind"`
	Count   int                  `json:"count"`
	Formats []string             `json:"formats,omitempty"`
	Shapes  []synth.Shape        `json:"shapes,omitempty"`
	Schemas []synth.RecordSchema `json:"schemas,omitempty"`
	Profile synth.Profile        `json:"profile,omitempty"`
	// People is how many registry people the content is about; existing
	// people are reused and new ones created when there are too few.
	People int `json:"people,omitempty"`
	// From names template emails (fixture IDs) for kind emails, instead of
	// shapes. Each generated email is one of them with its linked people
	// swapped for registry people; see emailtemplate. Real-derived
	// templates must be approved.
	From     []string `json:"from,omitempty"`
	Seed     uint64   `json:"seed,omitempty"`
	Scenario string   `json:"scenario,omitempty"`
	Tags     []string `json:"tags,omitempty"`
}

// Result is what a request created.
type Result struct {
	Seed     uint64            `json:"seed"`
	Fixtures []fixture.Fixture `json:"fixtures"`
	// PersonIDs are the registry people the content is about.
	PersonIDs []string `json:"person_ids"`
}

// Service generates synthetic fixtures.
type Service struct {
	db       xdal.DBInterface
	datasets *dataset.Service
	fixtures *fixture.Service
	persons  *person.Service
	audit    *audit.Service
	seed     func() uint64
}

// NewService returns a generation service built on the other services.
func NewService(db xdal.DBInterface, datasets *dataset.Service, fixtures *fixture.Service, persons *person.Service, auditService *audit.Service) *Service {
	return &Service{db: db, datasets: datasets, fixtures: fixtures, persons: persons, audit: auditService, seed: randomSeed}
}

// item is one generated piece of content before it is stored.
type item struct {
	name     string
	format   string
	scenario string
	tags     []string
	schema   string
	people   []fixture.PersonLink
	data     []byte
	// generator overrides the request's generator string.
	generator string
	// ingestBatch is the template's batch, so purging it reaches the item.
	ingestBatch string
}

// Generate creates synthetic fixtures in a dataset the caller may change.
// Items are stored one by one; if one fails, the ones before it remain and
// the error says how many were created.
func (s *Service) Generate(ctx context.Context, actor principal.Principal, datasetID string, req Request) (*Result, error) {
	if !actor.CanWrite() {
		return nil, fmt.Errorf("%w: generating needs a write key", apperr.ErrForbidden)
	}
	if err := normalise(&req); err != nil {
		return nil, err
	}
	if _, err := s.datasets.Authorize(ctx, nil, actor, datasetID, dataset.AccessWrite); err != nil {
		return nil, err
	}
	var templates []*template
	if len(req.From) > 0 {
		var err error
		if templates, err = s.loadTemplates(ctx, actor, req.From); err != nil {
			return nil, err
		}
		if err := sizePool(&req, templates); err != nil {
			return nil, err
		}
	}
	if req.Seed == 0 {
		req.Seed = s.seed()
	}
	// People are picked from their own stream, so the content stream (and so
	// the output) is the same for the same seed and the same people, however
	// the registry got there.
	registered, err := s.persons.Pick(ctx, actor, synth.New(req.Seed), req.People)
	if err != nil {
		return nil, err
	}
	g := synth.New(req.Seed ^ contentStream)
	people := make([]synth.Person, len(registered))
	res := &Result{Seed: req.Seed, Fixtures: []fixture.Fixture{}}
	for i, p := range registered {
		people[i] = p.Person
		res.PersonIDs = append(res.PersonIDs, p.ID)
	}

	var items []item
	if templates != nil {
		items, err = buildFromTemplates(g, req, templates, people)
	} else {
		items, err = build(g, req, people)
	}
	if err != nil {
		return nil, err
	}
	for _, it := range items {
		generator := it.generator
		if generator == "" {
			generator = fmt.Sprintf("%s seed=%d", GeneratorName, req.Seed)
		}
		f, err := s.fixtures.Create(ctx, actor, fixture.CreateInput{
			DatasetID:    datasetID,
			Name:         it.name,
			Format:       it.format,
			Scenario:     it.scenario,
			Tags:         append(append([]string{}, req.Tags...), it.tags...),
			Source:       fixture.SourceSynthetic,
			Generator:    generator,
			RecordSchema: it.schema,
			IngestBatch:  it.ingestBatch,
			People:       it.people,
			Content:      bytes.NewReader(it.data),
		})
		if err != nil {
			return nil, fmt.Errorf("created %d of %d fixtures: %w", len(res.Fixtures), len(items), err)
		}
		res.Fixtures = append(res.Fixtures, *f)
	}

	details := map[string]any{"kind": req.Kind, "fixtures": len(res.Fixtures), "seed": req.Seed, "people": len(res.PersonIDs)}
	if len(req.From) > 0 {
		details["from"] = req.From
	}
	err = s.db.WithTx(ctx, nil, func(tx *xdal.Tx) error {
		return s.audit.Record(ctx, tx, audit.Event{
			AccountID: actor.AccountID, Action: audit.ActionFixtureGenerate, TargetType: "dataset", TargetID: datasetID,
			Details: details,
		})
	})
	if err != nil {
		return nil, err
	}
	return res, nil
}

// normalise validates req and fills in defaults.
func normalise(req *Request) error {
	if req.Count < 1 || req.Count > MaxCount {
		return fmt.Errorf("%w: count must be 1-%d", apperr.ErrInvalid, MaxCount)
	}
	if req.Profile == "" {
		req.Profile = synth.ProfileInsurance
	}
	if !req.Profile.Valid() {
		return fmt.Errorf("%w: profile must be insurance or manufacturing", apperr.ErrInvalid)
	}
	if len(req.From) > 0 {
		return normaliseTemplates(req)
	}
	if req.People == 0 {
		req.People = min(10, max(2, req.Count))
	}
	if req.People < 1 || req.People > MaxPeople {
		return fmt.Errorf("%w: people must be 1-%d", apperr.ErrInvalid, MaxPeople)
	}
	switch req.Kind {
	case KindFiles:
		if len(req.Formats) == 0 {
			req.Formats = synth.FileFormats()
		}
		for _, f := range req.Formats {
			if !synth.IsFileFormat(f) {
				return fmt.Errorf("%w: unknown file format %q (use %s)", apperr.ErrInvalid, f, strings.Join(synth.FileFormats(), ", "))
			}
		}
	case KindEmails:
		if req.People < 2 {
			return fmt.Errorf("%w: emails need at least 2 people", apperr.ErrInvalid)
		}
		if len(req.Shapes) == 0 {
			req.Shapes = synth.Shapes
		}
		for _, sh := range req.Shapes {
			if !sh.Valid() {
				return fmt.Errorf("%w: unknown email shape %q (use %s)", apperr.ErrInvalid, sh, strings.Join(synth.ShapeNames(), ", "))
			}
		}
	case KindRecords:
		if len(req.Schemas) == 0 {
			req.Schemas = synth.Schemas
		}
		for _, sc := range req.Schemas {
			if !sc.Valid() {
				return fmt.Errorf("%w: unknown record schema %q", apperr.ErrInvalid, sc)
			}
		}
	default:
		return fmt.Errorf("%w: kind must be files, emails or records", apperr.ErrInvalid)
	}
	return nil
}

// build generates every item. Names start with a short form of the seed, so
// runs with different seeds never clash in one dataset.
func build(g *synth.Gen, req Request, people []synth.Person) ([]item, error) {
	prefix := fmt.Sprintf("g%06x", req.Seed&0xffffff)
	var items []item
	for i := 0; i < req.Count; i++ {
		n := fmt.Sprintf("%s-%03d", prefix, i+1)
		switch req.Kind {
		case KindFiles:
			format := req.Formats[i%len(req.Formats)]
			group := pickGroup(g, people, 3)
			f, err := g.File(format, synth.FileInput{Profile: req.Profile, People: group})
			if err != nil {
				return nil, err
			}
			links := []fixture.PersonLink{{PersonID: group[0].ID, Role: "author"}}
			for _, p := range group[1:] {
				links = append(links, fixture.PersonLink{PersonID: p.ID, Role: "mentioned"})
			}
			items = append(items, item{
				name: n + "-" + synth.Slug(f.Title) + "." + format, format: format,
				scenario: req.Scenario, people: links, data: f.Data,
			})
		case KindEmails:
			shape := req.Shapes[i%len(req.Shapes)]
			emails, err := g.Emails(synth.EmailInput{Profile: req.Profile, Shape: shape, People: pickGroup(g, people, 4)})
			if err != nil {
				return nil, err
			}
			scenario, tags := string(shape), []string(nil)
			if req.Scenario != "" {
				scenario, tags = req.Scenario, []string{"shape-" + string(shape)}
			}
			for j, e := range emails {
				name := n + "-" + synth.Slug(e.Subject) + ".eml"
				if len(emails) > 1 {
					name = fmt.Sprintf("%s-%d-%s.eml", n, j+1, synth.Slug(e.Subject))
				}
				items = append(items, item{
					name: name, format: "eml", scenario: scenario, tags: tags,
					people: emailLinks(e), data: e.Data,
				})
			}
		case KindRecords:
			schema := req.Schemas[i%len(req.Schemas)]
			// The i-th item is record i/len(Schemas) of its schema.
			r, err := g.Record(schema, synth.RecordInput{Profile: req.Profile, People: people}, i/len(req.Schemas))
			if err != nil {
				return nil, err
			}
			links := make([]fixture.PersonLink, len(r.People))
			for k, pr := range r.People {
				links[k] = fixture.PersonLink{PersonID: pr.PersonID, Role: pr.Role}
			}
			items = append(items, item{
				name: n + "-" + string(schema) + "-" + synth.Slug(r.Title) + ".json", format: "json",
				scenario: req.Scenario, schema: string(schema), people: links, data: r.Data,
			})
		}
	}
	return items, nil
}

// pickGroup returns up to n distinct people, starting at a random one.
func pickGroup(g *synth.Gen, people []synth.Person, n int) []synth.Person {
	n = min(n, len(people))
	start := g.Intn(len(people))
	out := make([]synth.Person, n)
	for i := range out {
		out[i] = people[(start+i)%len(people)]
	}
	return out
}

// emailLinks links an email to the registry people in it. Senders that are
// not registry people (a bounce's mailer-daemon, a newsletter) are left out.
func emailLinks(e synth.Email) []fixture.PersonLink {
	var links []fixture.PersonLink
	add := func(p synth.Person, role string) {
		if p.ID != "" {
			links = append(links, fixture.PersonLink{PersonID: p.ID, Role: role})
		}
	}
	add(e.From, "from")
	for _, p := range e.To {
		add(p, "to")
	}
	for _, p := range e.Cc {
		add(p, "cc")
	}
	return links
}

func randomSeed() uint64 {
	var b [8]byte
	_, _ = rand.Read(b[:])
	if v := binary.LittleEndian.Uint64(b[:]); v != 0 {
		return v
	}
	return 1
}
