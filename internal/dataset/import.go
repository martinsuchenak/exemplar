package dataset

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/martinsuchenak/xdal/pkg/xdal"

	"github.com/martinsuchenak/exemplar/internal/apperr"
	"github.com/martinsuchenak/exemplar/internal/principal"
	"github.com/martinsuchenak/exemplar/internal/storeutil"
)

// CheckImport reports whether actor may import d: the rules Import applies,
// without writing, so an import can fail before it stores any content.
func (s *Service) CheckImport(ctx context.Context, exec xdal.DBExecutor, actor principal.Principal, d Dataset) error {
	if exec == nil {
		exec = s.db
	}
	if !actor.CanWrite() {
		return fmt.Errorf("%w: importing needs a write key", apperr.ErrForbidden)
	}
	if !storeutil.ValidID(d.ID) || !nameRe.MatchString(strings.TrimSpace(d.Name)) || d.Version < 1 {
		return fmt.Errorf("%w: archive dataset %q has a bad id, name or version", apperr.ErrInvalid, d.ID)
	}
	if !d.Scope.Valid() {
		return fmt.Errorf("%w: archive dataset %q has an unknown scope", apperr.ErrInvalid, d.Name)
	}
	if d.Scope != ScopeWorkspace && !actor.IsAdmin() {
		return fmt.Errorf("%w: importing %s datasets needs an admin key", apperr.ErrForbidden, d.Scope)
	}
	existing, err := s.storage.Get(ctx, exec, d.ID)
	if errors.Is(err, apperr.ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	if !canRead(actor, existing) || !canChange(actor, existing) {
		return fmt.Errorf("%w: dataset %s is already here and this key cannot change it", apperr.ErrForbidden, d.ID)
	}
	if existing.Scope != d.Scope {
		return fmt.Errorf("%w: dataset %s is already here as a %s dataset", apperr.ErrConflict, d.ID, existing.Scope)
	}
	return nil
}

// Import adds an exported dataset inside tx, keeping its ID, name, version,
// description and tags; the caller becomes its owner and a parent that is
// not here is dropped. It is added unfrozen, so its fixtures can follow;
// call FreezeImported afterwards. A dataset with the same ID already here is
// returned with created false. CheckImport's rules apply.
func (s *Service) Import(ctx context.Context, tx *xdal.Tx, actor principal.Principal, d Dataset) (*Dataset, bool, error) {
	if err := s.CheckImport(ctx, tx, actor, d); err != nil {
		return nil, false, err
	}
	existing, err := s.storage.Get(ctx, tx, d.ID)
	if err == nil {
		return existing, false, nil
	}
	if !errors.Is(err, apperr.ErrNotFound) {
		return nil, false, err
	}
	if d.ParentID != "" {
		if _, err := s.storage.Get(ctx, tx, d.ParentID); errors.Is(err, apperr.ErrNotFound) {
			d.ParentID = ""
		} else if err != nil {
			return nil, false, err
		}
	}
	d.Name = strings.TrimSpace(d.Name)
	d.OwnerAccountID = actor.AccountID
	d.Tags = normaliseTags(d.Tags)
	d.FrozenAt = nil
	d.UpdatedAt = s.now()
	if d.CreatedAt.IsZero() {
		d.CreatedAt = d.UpdatedAt
	}
	if err := s.storage.Insert(ctx, tx, &d); err != nil {
		if errors.Is(err, apperr.ErrConflict) {
			return nil, false, fmt.Errorf("%w: a %s dataset named %q (version %d) already exists here", apperr.ErrConflict, d.Scope, d.Name, d.Version)
		}
		return nil, false, err
	}
	return &d, true, nil
}

// FreezeImported freezes a dataset Import created, at the time it was frozen
// on the instance it came from.
func (s *Service) FreezeImported(ctx context.Context, tx *xdal.Tx, id string, at time.Time) error {
	return s.storage.SetFrozen(ctx, tx, id, at)
}
