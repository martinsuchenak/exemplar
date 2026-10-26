// Package dataset manages datasets and decides who may read or change them.
// Other services (fixtures) ask this service for access instead of checking
// datasets themselves.
package dataset

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/martinsuchenak/xdal/pkg/xdal"

	"github.com/martinsuchenak/exemplar/internal/apperr"
	"github.com/martinsuchenak/exemplar/internal/audit"
	"github.com/martinsuchenak/exemplar/internal/blob"
	"github.com/martinsuchenak/exemplar/internal/principal"
	"github.com/martinsuchenak/exemplar/internal/storeutil"
)

// Scope decides visibility: global datasets are readable by every key,
// workspaces by their owner, quarantine (unreviewed real-derived data) by admins.
type Scope string

const (
	ScopeGlobal     Scope = "global"
	ScopeWorkspace  Scope = "workspace"
	ScopeQuarantine Scope = "quarantine"
)

// Valid reports whether s is a known scope.
func (s Scope) Valid() bool {
	return s == ScopeGlobal || s == ScopeWorkspace || s == ScopeQuarantine
}

// Dataset groups fixtures. It is the unit that is shared, frozen and versioned.
type Dataset struct {
	ID             string     `json:"id"`
	Name           string     `json:"name"`
	Description    string     `json:"description"`
	Scope          Scope      `json:"scope"`
	OwnerAccountID string     `json:"owner_account_id"`
	ParentID       string     `json:"parent_id,omitempty"`
	Version        int        `json:"version"`
	Tags           []string   `json:"tags"`
	FrozenAt       *time.Time `json:"frozen_at,omitempty"`
	CreatedAt      time.Time  `json:"created_at"`
	UpdatedAt      time.Time  `json:"updated_at"`
}

// Frozen reports whether the dataset's contents are read-only.
func (d *Dataset) Frozen() bool {
	return d.FrozenAt != nil
}

// CreateInput describes a new dataset. Scope defaults to workspace.
type CreateInput struct {
	Name        string   `json:"name"`
	Description string   `json:"description"`
	Scope       Scope    `json:"scope"`
	Tags        []string `json:"tags"`
}

// ListFilter narrows List; empty fields match everything.
type ListFilter struct {
	Scope Scope
	Name  string
}

// Access is what a caller wants to do with a dataset.
type Access int

const (
	AccessRead Access = iota
	// AccessWrite changes the dataset's contents, so it also needs the dataset unfrozen.
	AccessWrite
	// AccessDelete removes the whole dataset, frozen or not.
	AccessDelete
)

var nameRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9 ._-]{0,127}$`)

// Service manages datasets and decides who may use them.
type Service struct {
	db      xdal.DBInterface
	storage *Storage
	blobs   *blob.Store
	audit   *audit.Service
	now     func() time.Time
}

// NewService returns a dataset service. blobs is used to collect contents
// left unused when a dataset is deleted.
func NewService(db xdal.DBInterface, blobs *blob.Store, auditService *audit.Service) *Service {
	return &Service{db: db, storage: NewStorage(db.NewQueryBuilder()), blobs: blobs, audit: auditService, now: time.Now}
}

// Create makes a dataset owned by the caller. Global and quarantine datasets
// need an admin key; anyone with a write key can create workspaces.
func (s *Service) Create(ctx context.Context, actor principal.Principal, in CreateInput) (*Dataset, error) {
	in.Name = strings.TrimSpace(in.Name)
	if in.Scope == "" {
		in.Scope = ScopeWorkspace
	}
	if !nameRe.MatchString(in.Name) {
		return nil, fmt.Errorf("%w: dataset name must be 1-128 letters, digits, spaces, '.', '_' or '-'", apperr.ErrInvalid)
	}
	if !in.Scope.Valid() {
		return nil, fmt.Errorf("%w: scope must be global, workspace or quarantine", apperr.ErrInvalid)
	}
	if !actor.CanWrite() {
		return nil, fmt.Errorf("%w: creating datasets needs a write key", apperr.ErrForbidden)
	}
	if in.Scope != ScopeWorkspace && !actor.IsAdmin() {
		return nil, fmt.Errorf("%w: %s datasets need an admin key", apperr.ErrForbidden, in.Scope)
	}

	now := s.now()
	d := &Dataset{
		ID:             storeutil.NewID(),
		Name:           in.Name,
		Description:    in.Description,
		Scope:          in.Scope,
		OwnerAccountID: actor.AccountID,
		Version:        1,
		Tags:           normaliseTags(in.Tags),
		CreatedAt:      now,
		UpdatedAt:      now,
	}
	err := s.db.WithTx(ctx, nil, func(tx *xdal.Tx) error {
		if err := s.storage.Insert(ctx, tx, d); err != nil {
			if errors.Is(err, apperr.ErrConflict) {
				return fmt.Errorf("%w: a %s dataset named %q already exists", apperr.ErrConflict, d.Scope, d.Name)
			}
			return err
		}
		return s.audit.Record(ctx, tx, audit.Event{
			AccountID: actor.AccountID, Action: audit.ActionDatasetCreate, TargetType: "dataset", TargetID: d.ID,
			Details: map[string]any{"name": d.Name, "scope": d.Scope},
		})
	})
	if err != nil {
		return nil, err
	}
	return d, nil
}

// Get returns a dataset the caller can read.
func (s *Service) Get(ctx context.Context, actor principal.Principal, id string) (*Dataset, error) {
	return s.Authorize(ctx, s.db, actor, id, AccessRead)
}

// Authorize loads a dataset and checks the caller may use it as asked. A
// dataset the caller cannot read is reported as not found. exec lets other
// services check inside their own transaction; nil uses the database directly.
func (s *Service) Authorize(ctx context.Context, exec xdal.DBExecutor, actor principal.Principal, id string, access Access) (*Dataset, error) {
	if exec == nil {
		exec = s.db
	}
	d, err := s.storage.Get(ctx, exec, id)
	if err != nil || !canRead(actor, d) {
		return nil, fmt.Errorf("dataset %s: %w", id, apperr.ErrNotFound)
	}
	if access == AccessRead {
		return d, nil
	}
	if !canChange(actor, d) {
		return nil, fmt.Errorf("%w: this key cannot change dataset %q", apperr.ErrForbidden, d.Name)
	}
	if access == AccessWrite && d.Frozen() {
		return nil, fmt.Errorf("%w: dataset %q is frozen", apperr.ErrConflict, d.Name)
	}
	return d, nil
}

// List returns the datasets the caller can read.
func (s *Service) List(ctx context.Context, actor principal.Principal, f ListFilter) ([]Dataset, error) {
	if !actor.CanRead() {
		return nil, fmt.Errorf("%w: this key cannot read", apperr.ErrForbidden)
	}
	if f.Scope != "" && !f.Scope.Valid() {
		return nil, fmt.Errorf("%w: unknown scope %q", apperr.ErrInvalid, f.Scope)
	}
	return s.storage.List(ctx, s.db, f, visibility{accountID: actor.AccountID, all: actor.IsAdmin()})
}

// Freeze makes a dataset's contents read-only. Freezing twice is a no-op.
func (s *Service) Freeze(ctx context.Context, actor principal.Principal, id string) (*Dataset, error) {
	var d *Dataset
	err := s.db.WithTx(ctx, nil, func(tx *xdal.Tx) error {
		var err error
		d, err = s.Authorize(ctx, tx, actor, id, AccessDelete)
		if err != nil || d.Frozen() {
			return err
		}
		now := s.now()
		if err := s.storage.SetFrozen(ctx, tx, id, now); err != nil {
			return err
		}
		d.FrozenAt, d.UpdatedAt = &now, now
		return s.audit.Record(ctx, tx, audit.Event{
			AccountID: actor.AccountID, Action: audit.ActionDatasetFreeze, TargetType: "dataset", TargetID: id,
		})
	})
	if err != nil {
		return nil, err
	}
	return d, nil
}

// Delete removes a dataset with all its fixtures, then any blobs nothing else uses.
func (s *Service) Delete(ctx context.Context, actor principal.Principal, id string) error {
	err := s.db.WithTx(ctx, nil, func(tx *xdal.Tx) error {
		d, err := s.Authorize(ctx, tx, actor, id, AccessDelete)
		if err != nil {
			return err
		}
		fixtures, err := s.storage.CountFixtures(ctx, tx, id)
		if err != nil {
			return err
		}
		if err := s.storage.Delete(ctx, tx, id); err != nil {
			return err
		}
		return s.audit.Record(ctx, tx, audit.Event{
			AccountID: actor.AccountID, Action: audit.ActionDatasetDelete, TargetType: "dataset", TargetID: id,
			Details: map[string]any{"name": d.Name, "scope": d.Scope, "fixtures": fixtures},
		})
	})
	if err != nil {
		return err
	}
	_, err = s.blobs.Collect(ctx, s.db)
	return err
}

func canRead(p principal.Principal, d *Dataset) bool {
	if !p.CanRead() {
		return false
	}
	switch d.Scope {
	case ScopeGlobal:
		return true
	case ScopeWorkspace:
		return d.OwnerAccountID == p.AccountID || p.IsAdmin()
	default:
		return p.IsAdmin()
	}
}

func canChange(p principal.Principal, d *Dataset) bool {
	if !p.CanWrite() {
		return false
	}
	if d.Scope == ScopeWorkspace {
		return d.OwnerAccountID == p.AccountID || p.IsAdmin()
	}
	return p.IsAdmin()
}

func normaliseTags(tags []string) []string {
	out := make([]string, 0, len(tags))
	seen := map[string]bool{}
	for _, t := range tags {
		t = strings.ToLower(strings.TrimSpace(t))
		if t != "" && !seen[t] {
			seen[t] = true
			out = append(out, t)
		}
	}
	return out
}
