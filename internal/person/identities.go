package person

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sync"

	"github.com/martinsuchenak/xdal/pkg/xdal"

	"github.com/martinsuchenak/exemplar/internal/apperr"
	"github.com/martinsuchenak/exemplar/internal/principal"
	"github.com/martinsuchenak/exemplar/internal/storeutil"
	"github.com/martinsuchenak/exemplar/internal/synth"
)

// Identities resolves real identities to registry people and real name
// tokens to fake ones during anonymisation. Callers pass keyed hashes of the
// real values (the anonymiser computes them), so neither table ever holds a
// real name or address.
//
// With persist false (a dry run), lookups read the database but new people and
// tokens live only in memory, so a dry run leaves no trace.
type Identities struct {
	s       *Service
	actor   principal.Principal
	keyID   string
	persist bool

	mu     sync.Mutex
	tokens map[string]string
	byHash map[string]synth.Person
}

// Identities returns a resolver acting for actor. Only admin keys may use it,
// since it links real identities to people.
func (s *Service) Identities(actor principal.Principal, keyID string, persist bool) (*Identities, error) {
	if !actor.IsAdmin() {
		return nil, fmt.Errorf("%w: resolving identities needs an admin key", apperr.ErrForbidden)
	}
	if keyID == "" {
		return nil, fmt.Errorf("%w: a key id is required", apperr.ErrInvalid)
	}
	return &Identities{s: s, actor: actor, keyID: keyID, persist: persist, tokens: map[string]string{}, byHash: map[string]synth.Person{}}, nil
}

// Token returns the fake token stored for hash, or stores fake for it.
func (r *Identities) Token(ctx context.Context, hash, fake string) (string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if v, ok := r.tokens[hash]; ok {
		return v, nil
	}
	v, err := r.s.storage.Token(ctx, r.s.db, hash)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return "", err
	}
	if err == nil {
		r.tokens[hash] = v
		return v, nil
	}
	if r.persist {
		if err := r.s.storage.InsertToken(ctx, r.s.db, hash, fake, r.keyID, r.s.now()); err != nil {
			return "", err
		}
	}
	r.tokens[hash] = fake
	return fake, nil
}

// LookupToken returns the fake token stored for hash, if any.
func (r *Identities) LookupToken(ctx context.Context, hash string) (string, bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if v, ok := r.tokens[hash]; ok {
		return v, true, nil
	}
	v, err := r.s.storage.Token(ctx, r.s.db, hash)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	r.tokens[hash] = v
	return v, true, nil
}

// Person returns the registry person any of hashes already points to, or
// creates one from p. Every hash is then linked to that person.
func (r *Identities) Person(ctx context.Context, hashes []string, p synth.Person) (synth.Person, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, h := range hashes {
		if found, ok := r.byHash[h]; ok {
			return r.link(ctx, hashes, found)
		}
	}
	var found *Person
	err := r.s.db.WithTx(ctx, nil, func(tx *xdal.Tx) error {
		for _, h := range hashes {
			id, err := r.s.storage.IdentityPerson(ctx, tx, h)
			if errors.Is(err, sql.ErrNoRows) {
				continue
			}
			if err != nil {
				return err
			}
			found, err = r.s.storage.Get(ctx, tx, id)
			return err
		}
		if !r.persist {
			return nil
		}
		p.ID = storeutil.NewID()
		created := Person{Person: p, CreatedBy: r.actor.AccountID, CreatedAt: r.s.now()}
		if err := r.s.insertUnique(ctx, tx, &created); err != nil {
			return err
		}
		found = &created
		return nil
	})
	if err != nil {
		return synth.Person{}, err
	}
	if found == nil {
		// Dry run: a person that would be created; its ID stays empty.
		return r.link(ctx, hashes, p)
	}
	return r.link(ctx, hashes, found.Person)
}

// link records every hash for p (in memory, and in the database when
// persisting and p is stored). Hashes that already point somewhere stay put.
func (r *Identities) link(ctx context.Context, hashes []string, p synth.Person) (synth.Person, error) {
	for _, h := range hashes {
		if _, ok := r.byHash[h]; ok {
			continue
		}
		r.byHash[h] = p
		if r.persist && p.ID != "" {
			if err := r.s.storage.InsertIdentity(ctx, r.s.db, h, p.ID, r.keyID, r.s.now()); err != nil {
				return synth.Person{}, err
			}
		}
	}
	return p, nil
}
