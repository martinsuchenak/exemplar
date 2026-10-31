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
	"encoding/json"
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
	// GeneratorName identifies this generator in fixture provenance. Version
	// 2 spaces record schemas by the request's schemas (see synth.Record)
	// and names each record's schema file.
	GeneratorName = "exemplar-synth/2"
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
	From []string `json:"from,omitempty"`
	// SchemaFiles are record schema files (TOML text) for this request
	// only, beside the server's; see synth.WithRequestSchemas. With no
	// Schemas named, these are the ones generated.
	SchemaFiles []string `json:"schema_files,omitempty"`
	// Lists replace the schemas' lists of the same name, or give the ones
	// a schema says a request must (request_lists), such as a target
	// system's own status IDs; see synth.CheckRequestLists.
	Lists    Lists    `json:"lists,omitempty"`
	Seed     uint64   `json:"seed,omitempty"`
	Scenario string   `json:"scenario,omitempty"`
	Tags     []string `json:"tags,omitempty"`
}

// Lists are lists by name a request gives (Request.Lists). Decoded from
// JSON, numbers keep their exact digits, so a target system's large IDs
// come back as they were sent.
type Lists map[string][]any

// UnmarshalJSON decodes lists with numbers as json.Number.
func (l *Lists) UnmarshalJSON(data []byte) error {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	var m map[string][]any
	if err := dec.Decode(&m); err != nil {
		return err
	}
	*l = m
	return nil
}

// maxRecordsBytes caps the JSON one request's records come to, checked as
// each is made, so a schema of large records cannot fill memory; see
// synth.MaxRecordValues for one record.
var maxRecordsBytes = 32 << 20

// RecordsResult is records made without storing them; see Records.
type RecordsResult struct {
	Seed    uint64            `json:"seed"`
	Records []json.RawMessage `json:"records"`
}

// Result is what a request created.
type Result struct {
	Seed     uint64            `json:"seed"`
	Fixtures []fixture.Fixture `json:"fixtures"`
	// PersonIDs are the registry people the content is about.
	PersonIDs []string `json:"person_ids"`
}

// SchemaSource gives the record schemas to generate from. The server's
// changes as admins add schemas through the API (recordschema.Service); a
// *synth.SchemaSet is a fixed one.
type SchemaSource interface {
	Current() *synth.SchemaSet
}

// Service generates synthetic fixtures.
type Service struct {
	db       xdal.DBInterface
	datasets *dataset.Service
	fixtures *fixture.Service
	persons  *person.Service
	audit    *audit.Service
	schemas  SchemaSource
	seed     func() uint64
}

// NewService returns a generation service built on the other services,
// generating records from schemas (nil for the built-in ones).
func NewService(db xdal.DBInterface, datasets *dataset.Service, fixtures *fixture.Service, persons *person.Service, auditService *audit.Service, schemas SchemaSource) *Service {
	if schemas == nil {
		schemas = synth.BuiltinSchemas()
	}
	return &Service{db: db, datasets: datasets, fixtures: fixtures, persons: persons, audit: auditService, schemas: schemas, seed: randomSeed}
}

// NewLocal returns a service that only makes records (Records), about
// people made up from the seed, with no database: the CLI's local mode.
func NewLocal(schemas *synth.SchemaSet) *Service {
	if schemas == nil {
		schemas = synth.BuiltinSchemas()
	}
	return &Service{schemas: schemas, seed: randomSeed}
}

// Records makes records as Generate does, but about people made up from the
// seed instead of the registry's, and returns them instead of storing them.
// It reads and writes nothing, so any caller may: everything in the records
// is fake by construction. People are made a few to a company, as the
// registry makes them.
func (s *Service) Records(ctx context.Context, actor principal.Principal, req Request) (*RecordsResult, error) {
	if req.Kind != KindRecords {
		return nil, fmt.Errorf("%w: only records are made without a server", apperr.ErrInvalid)
	}
	set, err := s.normalise(&req)
	if err != nil {
		return nil, err
	}
	if req.Seed == 0 {
		req.Seed = s.seed()
	}
	// People come from their own stream, as Generate picks them from one.
	pg := synth.New(req.Seed)
	people := pg.People(req.People)
	for i := range people {
		people[i].ID = pg.ID()
	}
	items, err := s.build(synth.New(req.Seed^contentStream), req, people, set)
	if err != nil {
		return nil, err
	}
	res := &RecordsResult{Seed: req.Seed, Records: make([]json.RawMessage, len(items))}
	for i, it := range items {
		res.Records[i] = it.data
	}
	return res, nil
}

// Schemas lists the record schemas this server generates, for any key that
// can read, with any added through the API that did not load (their
// Problem set).
func (s *Service) Schemas(actor principal.Principal) ([]*synth.Schema, error) {
	if !actor.CanRead() {
		return nil, fmt.Errorf("%w: this key cannot read", apperr.ErrForbidden)
	}
	return s.schemas.Current().List(), nil
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
	set, err := s.normalise(&req)
	if err != nil {
		return nil, err
	}
	if _, err := s.datasets.Authorize(ctx, nil, actor, datasetID, dataset.AccessWrite); err != nil {
		return nil, err
	}
	var templates []*template
	if len(req.From) > 0 {
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
		items, err = s.build(g, req, people, set)
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
	if len(req.SchemaFiles) > 0 {
		details["schema_files"] = len(req.SchemaFiles)
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

// normalise validates req and fills in defaults. It returns the record
// schemas the request can use: the server's as they are now, plus any it
// sends. The request uses that set throughout, whatever changes meanwhile.
func (s *Service) normalise(req *Request) (*synth.SchemaSet, error) {
	base := s.schemas.Current()
	if req.Count < 1 || req.Count > MaxCount {
		return nil, fmt.Errorf("%w: count must be 1-%d", apperr.ErrInvalid, MaxCount)
	}
	if req.Profile == "" {
		req.Profile = synth.ProfileInsurance
	}
	if !req.Profile.Valid() {
		return nil, fmt.Errorf("%w: profile must be insurance or manufacturing", apperr.ErrInvalid)
	}
	if req.Kind != KindRecords && (len(req.SchemaFiles) > 0 || len(req.Lists) > 0) {
		return nil, fmt.Errorf("%w: schema files and lists are for records", apperr.ErrInvalid)
	}
	if len(req.From) > 0 {
		return base, normaliseTemplates(req)
	}
	if req.People == 0 {
		req.People = min(10, max(2, req.Count))
	}
	if req.People < 1 || req.People > MaxPeople {
		return nil, fmt.Errorf("%w: people must be 1-%d", apperr.ErrInvalid, MaxPeople)
	}
	set := base
	switch req.Kind {
	case KindFiles:
		if len(req.Formats) == 0 {
			req.Formats = synth.FileFormats()
		}
		for _, f := range req.Formats {
			if !synth.IsFileFormat(f) {
				return nil, fmt.Errorf("%w: unknown file format %q (use %s)", apperr.ErrInvalid, f, strings.Join(synth.FileFormats(), ", "))
			}
		}
	case KindEmails:
		if req.People < 2 {
			return nil, fmt.Errorf("%w: emails need at least 2 people", apperr.ErrInvalid)
		}
		if len(req.Shapes) == 0 {
			req.Shapes = synth.Shapes
		}
		for _, sh := range req.Shapes {
			if !sh.Valid() {
				return nil, fmt.Errorf("%w: unknown email shape %q (use %s)", apperr.ErrInvalid, sh, strings.Join(synth.ShapeNames(), ", "))
			}
		}
	case KindRecords:
		var sent []synth.RecordSchema
		if len(req.SchemaFiles) > 0 {
			var err error
			if set, sent, err = synth.WithRequestSchemas(base, req.SchemaFiles); err != nil {
				return nil, fmt.Errorf("%w: %v", apperr.ErrInvalid, err)
			}
		}
		if len(req.Schemas) == 0 {
			req.Schemas = sent
		}
		if len(req.Schemas) == 0 {
			if req.Schemas = defaultSchemas(set, req.Lists); len(req.Schemas) == 0 {
				return nil, fmt.Errorf("%w: every record schema needs lists the request does not give; name the schemas (schemas) and give their lists (lists)", apperr.ErrInvalid)
			}
		}
		var used []*synth.Schema
		for _, name := range req.Schemas {
			sc, ok := set.Get(name)
			if !ok {
				names := make([]string, 0)
				for _, n := range set.Names() {
					names = append(names, string(n))
				}
				return nil, fmt.Errorf("%w: unknown record schema %q (use %s)", apperr.ErrInvalid, name, strings.Join(names, ", "))
			}
			used = append(used, sc)
		}
		if err := synth.CheckRequestLists(used, req.Lists); err != nil {
			return nil, fmt.Errorf("%w: %v", apperr.ErrInvalid, err)
		}
	default:
		return nil, fmt.Errorf("%w: kind must be files, emails or records", apperr.ErrInvalid)
	}
	return set, nil
}

// build generates every item. Names start with a short form of the seed, so
// runs with different seeds never clash in one dataset.
func (s *Service) build(g *synth.Gen, req Request, people []synth.Person, set *synth.SchemaSet) ([]item, error) {
	prefix := fmt.Sprintf("g%06x", req.Seed&0xffffff)
	var items []item
	size := 0
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
			slot := i % len(req.Schemas)
			schema, _ := set.Get(req.Schemas[slot])
			// The i-th item is record i/len(Schemas) of its schema, which is
			// spaced through the people by its slot in the request.
			r, err := g.Record(schema, synth.RecordInput{Profile: req.Profile, People: people, Slot: slot, Slots: len(req.Schemas), Lists: req.Lists}, i/len(req.Schemas))
			if err != nil {
				// The request's people do not fit the schema, such as
				// company records about people who have no company.
				return nil, fmt.Errorf("%w: %v", apperr.ErrInvalid, err)
			}
			if size += len(r.Data); size > maxRecordsBytes {
				return nil, fmt.Errorf("%w: the records come to over %d MiB after %d of %d; ask for fewer or smaller ones", apperr.ErrTooLarge, maxRecordsBytes>>20, i+1, req.Count)
			}
			links := make([]fixture.PersonLink, len(r.People))
			for k, pr := range r.People {
				links[k] = fixture.PersonLink{PersonID: pr.PersonID, Role: pr.Role}
			}
			items = append(items, item{
				name: n + "-" + string(schema.Name) + "-" + synth.Slug(r.Title) + ".json", format: "json",
				scenario: req.Scenario, schema: string(schema.Name), people: links, data: r.Data,
				generator: fmt.Sprintf("%s schema=%s@%s seed=%d", GeneratorName, schema.Name, schema.Hash, req.Seed),
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

// defaultSchemas are the schemas a request that names none generates: every
// one whose request lists it gives, so a schema that needs a target
// system's lists (an API or directory one, say) does not fail every request
// that is not about it. A schema the request gives any of its lists for is
// kept too, so a request meant for it but short of a list is told which
// list is missing.
func defaultSchemas(set *synth.SchemaSet, lists Lists) []synth.RecordSchema {
	var out []synth.RecordSchema
	for _, n := range set.Names() {
		sc, _ := set.Get(n)
		given, meant := true, false
		for _, l := range sc.RequestLists {
			if _, ok := lists[l]; !ok {
				given = false
			}
		}
		for _, l := range sc.Lists {
			if _, ok := lists[l]; ok {
				meant = true
			}
		}
		if given || meant {
			out = append(out, n)
		}
	}
	return out
}
