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
// An import holds it from EnsureContent until the fixtures that use the
// content are inserted. Hold it briefly: while a collection waits for it,
// every other content write waits too.
func (s *Service) HoldContent() (release func()) {
	return s.blobs.Acquire()
}

// StoreContent stores content for fixtures an import inserts later, within
// the fixture size limit. It does not hold off garbage collection, so a slow
// upload never stalls other writes, and it does not register the content,
// so garbage collection (which only removes registered content) leaves it
// alone meanwhile. Then call EnsureContent under HoldContent before
// inserting the fixtures, or AbandonContent if the import fails.
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
	return hash, size, nil
}

// EnsureContent registers content StoreContent stored, after checking it is
// still there. Call it under HoldContent. The content can only be gone if the
// same bytes were already here, used by fixtures deleted (and collected)
// while the import ran.
func (s *Service) EnsureContent(ctx context.Context, hash string, size int64) error {
	f, err := s.blobs.Open(hash)
	if err != nil {
		return fmt.Errorf("%w: content %s was removed while the import ran; run it again", apperr.ErrConflict, hash)
	}
	_ = f.Close()
	return s.blobs.Register(ctx, s.db, hash, size)
}

// AbandonContent registers content a failed import stored, so the next
// garbage collection removes it unless something uses it.
func (s *Service) AbandonContent(ctx context.Context, hash string, size int64) error {
	return s.blobs.Register(ctx, s.db, hash, size)
}

// Import inserts an exported fixture into d inside tx, keeping its ID, name,
// kind, provenance and review state; the caller becomes its creator. Its
// content must already be stored (StoreContent) and its people already here.
// A fixture with the same ID already in d is left alone (added false); one
// with the same ID in another dataset is a conflict.
//
// Real-derived fixtures must be approved and need an admin key: importing one
// trusts the review made on the instance it came from. Its reviewer is kept
// as ImportedReviewer, since it is not an account here. Quarantine datasets
// take only real-derived fixtures, as they do for uploads.
func (s *Service) Import(ctx context.Context, tx *xdal.Tx, actor principal.Principal, d *dataset.Dataset, f Fixture) (added bool, err error) {
	if existing, err := s.storage.Get(ctx, tx, f.ID); err == nil {
		if existing.DatasetID != d.ID {
			return false, fmt.Errorf("%w: fixture %s is already here, in another dataset", apperr.ErrConflict, f.ID)
		}
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
	f.Name = strings.TrimSpace(f.Name)
	if f.ID == "" || len(f.ID) > 64 || !validName(f.Name) ||
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
