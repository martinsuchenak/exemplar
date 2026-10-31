// Package person is the person registry: the fake people that records,
// generated files and emails refer to, so the same people appear everywhere.
// People are fake by construction, so any key that can read can see them.
package person

import (
	"context"
	"fmt"
	"sort"
	"time"

	"github.com/martinsuchenak/xdal/pkg/xdal"

	"github.com/martinsuchenak/exemplar/internal/apperr"
	"github.com/martinsuchenak/exemplar/internal/audit"
	"github.com/martinsuchenak/exemplar/internal/principal"
	"github.com/martinsuchenak/exemplar/internal/storeutil"
	"github.com/martinsuchenak/exemplar/internal/synth"
)

// Person is a registered fake person.
type Person struct {
	synth.Person
	CreatedBy string    `json:"created_by"`
	CreatedAt time.Time `json:"created_at"`
}

// ListFilter narrows List; Limit defaults to 100 and is capped at 1000.
type ListFilter struct {
	Company string
	Limit   int
	Offset  int
}

// GenerateInput asks for new people. Seed 0 picks a random seed.
type GenerateInput struct {
	Count int    `json:"count"`
	Seed  uint64 `json:"seed"`
}

const (
	defaultListLimit = 100
	maxListLimit     = 1000
	// MaxGenerate caps how many people one call creates.
	MaxGenerate = 1000
)

// Service manages the person registry.
type Service struct {
	db      xdal.DBInterface
	storage *Storage
	audit   *audit.Service
	now     func() time.Time
	seed    func() uint64
}

// NewService returns a person service that records generation in auditService.
func NewService(db xdal.DBInterface, auditService *audit.Service) *Service {
	return &Service{db: db, storage: NewStorage(db.NewQueryBuilder()), audit: auditService, now: time.Now, seed: randomSeed}
}

// Get returns a person to any key that can read.
func (s *Service) Get(ctx context.Context, actor principal.Principal, id string) (*Person, error) {
	if !actor.CanRead() {
		return nil, fmt.Errorf("%w: this key cannot read", apperr.ErrForbidden)
	}
	p, err := s.storage.Get(ctx, s.db, id)
	if err != nil {
		return nil, fmt.Errorf("person %s: %w", id, err)
	}
	return p, nil
}

// List returns people ordered by last then first name.
func (s *Service) List(ctx context.Context, actor principal.Principal, f ListFilter) ([]Person, error) {
	if !actor.CanRead() {
		return nil, fmt.Errorf("%w: this key cannot read", apperr.ErrForbidden)
	}
	if f.Limit <= 0 {
		f.Limit = defaultListLimit
	}
	if f.Limit > maxListLimit {
		f.Limit = maxListLimit
	}
	if f.Offset < 0 {
		f.Offset = 0
	}
	return s.storage.List(ctx, s.db, f)
}

// Generate creates in.Count new people, grouped a few to a company. It needs
// a write key.
func (s *Service) Generate(ctx context.Context, actor principal.Principal, in GenerateInput) ([]Person, uint64, error) {
	if !actor.CanWrite() {
		return nil, 0, fmt.Errorf("%w: generating people needs a write key", apperr.ErrForbidden)
	}
	if in.Count < 1 || in.Count > MaxGenerate {
		return nil, 0, fmt.Errorf("%w: count must be 1-%d", apperr.ErrInvalid, MaxGenerate)
	}
	if in.Seed == 0 {
		in.Seed = s.seed()
	}
	var people []Person
	err := s.db.WithTx(ctx, nil, func(tx *xdal.Tx) error {
		var err error
		people, err = s.create(ctx, tx, actor, synth.New(in.Seed), in.Count)
		if err != nil {
			return err
		}
		return s.audit.Record(ctx, tx, audit.Event{
			AccountID: actor.AccountID, Action: audit.ActionPersonGenerate, TargetType: "person",
			Details: map[string]any{"count": len(people), "seed": in.Seed},
		})
	})
	if err != nil {
		return nil, 0, err
	}
	return people, in.Seed, nil
}

// Pick returns n people for generated content: people already in the
// registry, chosen by g, topped up with new ones when there are too few.
// They come back in ID order, so the same set of people is always in the
// same order. The caller has checked the actor may write; new people are
// attributed to it.
func (s *Service) Pick(ctx context.Context, actor principal.Principal, g *synth.Gen, n int) ([]Person, error) {
	if n < 1 || n > MaxGenerate {
		return nil, fmt.Errorf("%w: people must be 1-%d", apperr.ErrInvalid, MaxGenerate)
	}
	var out []Person
	err := s.db.WithTx(ctx, nil, func(tx *xdal.Tx) error {
		ids, err := s.storage.AllIDs(ctx, tx)
		if err != nil {
			return err
		}
		for len(out) < n && len(ids) > 0 {
			i := g.Intn(len(ids))
			p, err := s.storage.Get(ctx, tx, ids[i])
			if err != nil {
				return err
			}
			out = append(out, *p)
			ids = append(ids[:i], ids[i+1:]...)
		}
		if missing := n - len(out); missing > 0 {
			created, err := s.create(ctx, tx, actor, g, missing)
			if err != nil {
				return err
			}
			out = append(out, created...)
			return s.audit.Record(ctx, tx, audit.Event{
				AccountID: actor.AccountID, Action: audit.ActionPersonGenerate, TargetType: "person",
				Details: map[string]any{"count": len(created), "for": "generated content"},
			})
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

// create inserts n new people from g. Email clashes get a number appended.
func (s *Service) create(ctx context.Context, exec xdal.DBExecutor, actor principal.Principal, g *synth.Gen, n int) ([]Person, error) {
	out := make([]Person, 0, n)
	for _, sp := range g.People(n) {
		sp.ID = storeutil.NewID()
		p := Person{Person: sp, CreatedBy: actor.AccountID, CreatedAt: s.now()}
		if err := s.insertUnique(ctx, exec, &p); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, nil
}

// insertUnique inserts p, numbering its email (jane.doe2@...) on a clash.
func (s *Service) insertUnique(ctx context.Context, exec xdal.DBExecutor, p *Person) error {
	base := p.Email
	for n := 2; ; n++ {
		taken, err := s.storage.EmailTaken(ctx, exec, p.Email)
		if err != nil {
			return err
		}
		if !taken {
			return s.storage.Insert(ctx, exec, p)
		}
		at := len(base) - len(domainPart(base))
		p.Email = fmt.Sprintf("%s%d%s", base[:at], n, base[at:])
	}
}

func domainPart(email string) string {
	for i := len(email) - 1; i >= 0; i-- {
		if email[i] == '@' {
			return email[i:]
		}
	}
	return ""
}
