package dataset

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/martinsuchenak/xdal/pkg/xdal"

	"github.com/martinsuchenak/exemplar/internal/apperr"
	"github.com/martinsuchenak/exemplar/internal/storeutil"
)

var columns = []string{"id", "name", "description", "scope", "owner_account_id", "parent_id", "version", "tags", "frozen_at", "created_at", "updated_at"}

// visibility limits List to what a caller may read; the service decides it.
type visibility struct {
	accountID string
	all       bool
	// noQuarantine leaves quarantine out, even when all is set.
	noQuarantine bool
}

// Storage holds the SQL for datasets. Only the dataset service calls it.
type Storage struct {
	qb *xdal.QueryBuilder
}

// NewStorage returns storage that builds queries with qb.
func NewStorage(qb *xdal.QueryBuilder) *Storage {
	return &Storage{qb: qb}
}

// Insert inserts d; a taken name returns apperr.ErrConflict.
func (s *Storage) Insert(ctx context.Context, exec xdal.DBExecutor, d *Dataset) error {
	var parent interface{}
	if d.ParentID != "" {
		parent = d.ParentID
	}
	query, args, err := s.qb.Insert("datasets").
		Set("id", d.ID).
		Set("name", d.Name).
		Set("description", d.Description).
		Set("scope", string(d.Scope)).
		Set("owner_account_id", d.OwnerAccountID).
		Set("parent_id", parent).
		Set("version", d.Version).
		Set("tags", storeutil.EncodeStrings(d.Tags)).
		Set("frozen_at", storeutil.TimeArg(d.FrozenAt)).
		Set("created_at", storeutil.FormatTime(d.CreatedAt)).
		Set("updated_at", storeutil.FormatTime(d.UpdatedAt)).
		Build()
	if err != nil {
		return err
	}
	_, err = exec.Exec(ctx, query, args...)
	if storeutil.IsUniqueViolation(err) {
		return apperr.ErrConflict
	}
	return err
}

// Get returns a dataset by id.
func (s *Storage) Get(ctx context.Context, exec xdal.DBExecutor, id string) (*Dataset, error) {
	query, args, err := s.qb.Select(columns...).From("datasets").Where("id = ?", id).Build()
	if err != nil {
		return nil, err
	}
	d, err := scan(exec.QueryRow(ctx, query, args...))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, apperr.ErrNotFound
	}
	return d, err
}

// List returns datasets matching f that v allows.
func (s *Storage) List(ctx context.Context, exec xdal.DBExecutor, f ListFilter, v visibility) ([]Dataset, error) {
	q := s.qb.Select(columns...).From("datasets")
	if !v.all {
		q = q.WhereGroup(func(g *xdal.WhereGroup) {
			g.Where("scope = ?", string(ScopeGlobal))
			g.OrWhere("(scope = ? AND owner_account_id = ?)", string(ScopeWorkspace), v.accountID)
		})
	}
	if v.noQuarantine {
		q = q.Where("scope <> ?", string(ScopeQuarantine))
	}
	if f.Scope != "" {
		q = q.Where("scope = ?", string(f.Scope))
	}
	if f.Name != "" {
		q = q.Where("name = ?", f.Name)
	}
	query, args, err := q.OrderBy("scope", "name", "version").Build()
	if err != nil {
		return nil, err
	}
	rows, err := exec.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := []Dataset{}
	for rows.Next() {
		d, err := scan(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *d)
	}
	return out, rows.Err()
}

// MaxGlobalVersion returns the highest version of the global dataset name, or 0.
func (s *Storage) MaxGlobalVersion(ctx context.Context, exec xdal.DBExecutor, name string) (int, error) {
	query, args, err := s.qb.Select("COALESCE(MAX(version), 0)").From("datasets").
		Where("scope = ?", string(ScopeGlobal)).
		Where("name = ?", name).
		Build()
	if err != nil {
		return 0, err
	}
	var v int
	err = exec.QueryRow(ctx, query, args...).Scan(&v)
	return v, err
}

// SetFrozen marks a dataset frozen at at.
func (s *Storage) SetFrozen(ctx context.Context, exec xdal.DBExecutor, id string, at time.Time) error {
	query, args, err := s.qb.Update("datasets").
		Set("frozen_at", storeutil.FormatTime(at)).
		Set("updated_at", storeutil.FormatTime(at)).
		Where("id = ?", id).
		Build()
	if err != nil {
		return err
	}
	_, err = exec.Exec(ctx, query, args...)
	return err
}

// CountFixtures counts a dataset's fixtures.
func (s *Storage) CountFixtures(ctx context.Context, exec xdal.DBExecutor, id string) (int, error) {
	query, args, err := s.qb.Select("COUNT(*)").From("fixtures").Where("dataset_id = ?", id).Build()
	if err != nil {
		return 0, err
	}
	var n int
	err = exec.QueryRow(ctx, query, args...).Scan(&n)
	return n, err
}

// Delete removes a dataset; its fixtures go with it (ON DELETE CASCADE).
func (s *Storage) Delete(ctx context.Context, exec xdal.DBExecutor, id string) error {
	query, args, err := s.qb.Delete("datasets").Where("id = ?", id).Build()
	if err != nil {
		return err
	}
	_, err = exec.Exec(ctx, query, args...)
	return err
}

type scanner interface {
	Scan(dest ...interface{}) error
}

func scan(row scanner) (*Dataset, error) {
	var d Dataset
	var scope, tags, created, updated string
	var parent, frozen sql.NullString
	if err := row.Scan(&d.ID, &d.Name, &d.Description, &scope, &d.OwnerAccountID, &parent, &d.Version, &tags, &frozen, &created, &updated); err != nil {
		return nil, err
	}
	d.Scope = Scope(scope)
	d.ParentID = parent.String
	d.Tags = storeutil.DecodeStrings(tags)
	d.FrozenAt = storeutil.NullTime(frozen)
	d.CreatedAt = storeutil.ParseTime(created)
	d.UpdatedAt = storeutil.ParseTime(updated)
	return &d, nil
}
