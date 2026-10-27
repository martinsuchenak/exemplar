package person

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/martinsuchenak/xdal/pkg/xdal"

	"github.com/martinsuchenak/exemplar/internal/apperr"
	"github.com/martinsuchenak/exemplar/internal/principal"
	"github.com/martinsuchenak/exemplar/internal/storeutil"
)

// CheckImport reports whether an exported person may be imported: an ID
// that is safe in API paths, a name, and a .example address. Imports check
// every person in an archive with it, linked to a fixture or not, so a bad
// record is refused rather than silently left out.
func CheckImport(p Person) error {
	if !storeutil.ValidID(p.ID) || p.FirstName == "" || !strings.HasSuffix(strings.ToLower(p.Email), ".example") || !strings.Contains(p.Email, "@") {
		return fmt.Errorf("%w: archive person %q needs an id, a name and a .example address", apperr.ErrInvalid, p.ID)
	}
	return nil
}

// Import adds an exported person inside tx and returns the ID they have
// here. A person with the same ID is already here: that ID. A person with the
// same email is already here: their ID, since registry emails identify people
// (two instances generating with one seed make the same people). Otherwise
// the person is added with their ID, attributed to actor. Addresses must be
// on a .example domain, like every registry address.
func (s *Service) Import(ctx context.Context, tx *xdal.Tx, actor principal.Principal, p Person) (id string, added bool, err error) {
	if !actor.CanWrite() {
		return "", false, fmt.Errorf("%w: importing people needs a write key", apperr.ErrForbidden)
	}
	if err := CheckImport(p); err != nil {
		return "", false, err
	}
	if _, err := s.storage.Get(ctx, tx, p.ID); err == nil {
		return p.ID, false, nil
	} else if !errors.Is(err, apperr.ErrNotFound) {
		return "", false, err
	}
	existing, err := s.storage.IDByEmail(ctx, tx, p.Email)
	if err == nil {
		return existing, false, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return "", false, err
	}
	p.CreatedBy = actor.AccountID
	if p.CreatedAt.IsZero() {
		p.CreatedAt = s.now()
	}
	if err := s.storage.Insert(ctx, tx, &p); err != nil {
		return "", false, err
	}
	return p.ID, true, nil
}
