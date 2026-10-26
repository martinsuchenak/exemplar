package fixture

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/martinsuchenak/xdal/pkg/xdal"

	"github.com/martinsuchenak/exemplar/internal/apperr"
	"github.com/martinsuchenak/exemplar/internal/blob"
	"github.com/martinsuchenak/exemplar/internal/dataset"
	"github.com/martinsuchenak/exemplar/internal/principal"
)

// HoldContent stops content garbage collection until release is called.
// An import holds it from storing the first content until the fixtures that
// use it are inserted.
func (s *Service) HoldContent() (release func()) {
	return s.blobs.Acquire()
}

// StoreContent stores content for fixtures an import inserts later, within
// the fixture size limit. Hold HoldContent around it and the inserts.
func (s *Service) StoreContent(ctx context.Context, actor principal.Principal, r io.Reader) (hash string, size int64, err error) {
	if !actor.CanWrite() {
		return "", 0, fmt.Errorf("%w: importing needs a write key", apperr.ErrForbidden)
	}
	if s.maxSize > 0 {
		r = &capReader{r: r, left: s.maxSize}
	}
	hash, size, err = s.blobs.Put(r)
	if errors.Is(err, errTooLarge) {
		return "", 0, fmt.Errorf("%w: fixture content is over %d bytes", apperr.ErrTooLarge, s.maxSize)
	}
	if err != nil {
		return "", 0, err
	}
	if err := s.blobs.Register(ctx, s.db, hash, size); err != nil {
		return "", 0, fmt.Errorf("registering blob: %w", err)
	}
	return hash, size, nil
}

// Import inserts an exported fixture into d inside tx, keeping its ID, name,
// kind, provenance and review state; the caller becomes its creator. Its
// content must already be stored (StoreContent) and its people already here.
// A fixture with the same ID already here is left alone (added false).
//
// Real-derived fixtures must be approved and need an admin key: importing one
// trusts the review made on the instance it came from. Its reviewer is kept
// as ImportedReviewer, since it is not an account here. Quarantine datasets
// take only real-derived fixtures, as they do for uploads.
func (s *Service) Import(ctx context.Context, tx *xdal.Tx, actor principal.Principal, d *dataset.Dataset, f Fixture) (added bool, err error) {
	if _, err := s.storage.Get(ctx, tx, f.ID); err == nil {
		return false, nil
	} else if !errors.Is(err, apperr.ErrNotFound) {
		return false, err
	}
	if err := CheckImport(actor, d, &f); err != nil {
		return false, err
	}
	if d.Frozen() {
		return false, fmt.Errorf("%w: dataset %q is frozen, so %s cannot be added", apperr.ErrConflict, d.Name, f.Name)
	}
	if f.RealDerived() {
		if f.ReviewedBy != "" {
			f.ImportedReviewer = f.ReviewedBy
		}
	} else {
		f.ReviewState, f.ReviewedAt, f.ImportedReviewer = ReviewNotRequired, nil, ""
	}
	f.ReviewedBy = ""
	f.DatasetID, f.CreatedBy = d.ID, actor.AccountID
	f.Tags, f.People = normaliseTags(f.Tags), uniqueLinks(f.People)
	if f.CreatedAt.IsZero() {
		f.CreatedAt = s.now()
	}
	if err := s.storage.Insert(ctx, tx, &f); err != nil {
		if errors.Is(err, apperr.ErrConflict) {
			return false, fmt.Errorf("%w: dataset %q already has a fixture named %q", apperr.ErrConflict, d.Name, f.Name)
		}
		return false, err
	}
	return true, nil
}

// CheckImport reports whether actor may import f into d, and whether f is
// well formed. Import applies it; an import calls it first to report
// fixtures it will skip.
func CheckImport(actor principal.Principal, d *dataset.Dataset, f *Fixture) error {
	name := strings.TrimSpace(f.Name)
	if f.ID == "" || len(f.ID) > 64 || name == "" || len(name) > maxNameLen || strings.ContainsAny(name, `/\`) ||
		!f.Kind.Valid() || f.Format == "" || !f.Source.Valid() || !blob.ValidHash(f.BlobHash) {
		return fmt.Errorf("%w: archive fixture %q is malformed", apperr.ErrInvalid, f.ID)
	}
	if (f.Kind == KindRecord) != (f.RecordSchema != "") {
		return fmt.Errorf("%w: archive fixture %q: only records have a record schema", apperr.ErrInvalid, f.Name)
	}
	for _, l := range f.People {
		if l.PersonID == "" || !roleRe.MatchString(l.Role) {
			return fmt.Errorf("%w: archive fixture %q has a bad person link", apperr.ErrInvalid, f.Name)
		}
	}
	if f.RealDerived() {
		if f.ReviewState != ReviewApproved {
			return fmt.Errorf("%w: real-derived fixture %q is not approved", apperr.ErrInvalid, f.Name)
		}
		if !actor.IsAdmin() {
			return fmt.Errorf("%w: importing real-derived fixtures needs an admin key", apperr.ErrForbidden)
		}
	} else if d.Scope == dataset.ScopeQuarantine {
		return fmt.Errorf("%w: quarantine datasets only hold anonymised fixtures", apperr.ErrInvalid)
	}
	return nil
}
