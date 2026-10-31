package recordschema

import (
	"context"
	"database/sql"
	"errors"

	"github.com/martinsuchenak/xdal/pkg/xdal"

	"github.com/martinsuchenak/exemplar/internal/apperr"
	"github.com/martinsuchenak/exemplar/internal/storeutil"
)

var columns = []string{"name", "version", "definition", "created_at", "created_by", "updated_at", "updated_by"}

// Storage holds the SQL for API record schemas. Only this package's service
// calls it.
type Storage struct {
	qb *xdal.QueryBuilder
}

// NewStorage returns storage that builds queries with qb.
func NewStorage(qb *xdal.QueryBuilder) *Storage {
	return &Storage{qb: qb}
}

// Insert inserts r; a taken name returns apperr.ErrConflict.
func (s *Storage) Insert(ctx context.Context, exec xdal.DBExecutor, r *Row) error {
	query, args, err := s.qb.Insert("record_schemas").
		Set("name", r.Name).
		Set("version", r.Version).
		Set("definition", r.Definition).
		Set("created_at", storeutil.FormatTime(r.CreatedAt)).
		Set("created_by", r.CreatedBy).
		Set("updated_at", storeutil.FormatTime(r.UpdatedAt)).
		Set("updated_by", r.UpdatedBy).
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

// Get returns the row called name.
func (s *Storage) Get(ctx context.Context, exec xdal.DBExecutor, name string) (*Row, error) {
	query, args, err := s.qb.Select(columns...).From("record_schemas").Where("name = ?", name).Build()
	if err != nil {
		return nil, err
	}
	r, err := scan(exec.QueryRow(ctx, query, args...))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, apperr.ErrNotFound
	}
	return r, err
}

// List returns every row, by name.
func (s *Storage) List(ctx context.Context, exec xdal.DBExecutor) ([]Row, error) {
	query, args, err := s.qb.Select(columns...).From("record_schemas").OrderBy("name").Build()
	if err != nil {
		return nil, err
	}
	rows, err := exec.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := []Row{}
	for rows.Next() {
		r, err := scan(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *r)
	}
	return out, rows.Err()
}

// Update writes r's definition, version and who changed it.
func (s *Storage) Update(ctx context.Context, exec xdal.DBExecutor, r *Row) error {
	query, args, err := s.qb.Update("record_schemas").
		Set("version", r.Version).
		Set("definition", r.Definition).
		Set("updated_at", storeutil.FormatTime(r.UpdatedAt)).
		Set("updated_by", r.UpdatedBy).
		Where("name = ?", r.Name).
		Build()
	if err != nil {
		return err
	}
	_, err = exec.Exec(ctx, query, args...)
	return err
}

// Delete removes the row called name.
func (s *Storage) Delete(ctx context.Context, exec xdal.DBExecutor, name string) error {
	query, args, err := s.qb.Delete("record_schemas").Where("name = ?", name).Build()
	if err != nil {
		return err
	}
	_, err = exec.Exec(ctx, query, args...)
	return err
}

type scanner interface {
	Scan(dest ...interface{}) error
}

func scan(row scanner) (*Row, error) {
	var r Row
	var created, updated string
	if err := row.Scan(&r.Name, &r.Version, &r.Definition, &created, &r.CreatedBy, &updated, &r.UpdatedBy); err != nil {
		return nil, err
	}
	r.CreatedAt = storeutil.ParseTime(created)
	r.UpdatedAt = storeutil.ParseTime(updated)
	return &r, nil
}
