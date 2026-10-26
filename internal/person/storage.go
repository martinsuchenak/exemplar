package person

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/binary"
	"errors"

	"github.com/martinsuchenak/xdal/pkg/xdal"

	"github.com/martinsuchenak/exemplar/internal/apperr"
	"github.com/martinsuchenak/exemplar/internal/storeutil"
)

var columns = []string{
	"id", "first_name", "last_name", "email", "phone", "job_title", "company",
	"street", "suburb", "state", "postcode", "country", "created_by", "created_at",
}

// Storage holds the SQL for persons. Only the person service calls it.
type Storage struct {
	qb *xdal.QueryBuilder
}

// NewStorage returns storage that builds queries with qb.
func NewStorage(qb *xdal.QueryBuilder) *Storage {
	return &Storage{qb: qb}
}

// Insert inserts p.
func (s *Storage) Insert(ctx context.Context, exec xdal.DBExecutor, p *Person) error {
	query, args, err := s.qb.Insert("persons").
		Set("id", p.ID).
		Set("first_name", p.FirstName).
		Set("last_name", p.LastName).
		Set("email", p.Email).
		Set("phone", p.Phone).
		Set("job_title", p.JobTitle).
		Set("company", p.Company).
		Set("street", p.Address.Street).
		Set("suburb", p.Address.Suburb).
		Set("state", p.Address.State).
		Set("postcode", p.Address.Postcode).
		Set("country", p.Address.Country).
		Set("created_by", p.CreatedBy).
		Set("created_at", storeutil.FormatTime(p.CreatedAt)).
		Build()
	if err != nil {
		return err
	}
	_, err = exec.Exec(ctx, query, args...)
	return err
}

// Get returns a person by id.
func (s *Storage) Get(ctx context.Context, exec xdal.DBExecutor, id string) (*Person, error) {
	query, args, err := s.qb.Select(columns...).From("persons").Where("id = ?", id).Build()
	if err != nil {
		return nil, err
	}
	p, err := scan(exec.QueryRow(ctx, query, args...))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, apperr.ErrNotFound
	}
	return p, err
}

// List returns people matching f, ordered by last then first name.
func (s *Storage) List(ctx context.Context, exec xdal.DBExecutor, f ListFilter) ([]Person, error) {
	q := s.qb.Select(columns...).From("persons")
	if f.Company != "" {
		q = q.Where("company = ?", f.Company)
	}
	query, args, err := q.OrderBy("last_name", "first_name", "id").Limit(int64(f.Limit)).Offset(int64(f.Offset)).Build()
	if err != nil {
		return nil, err
	}
	rows, err := exec.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := []Person{}
	for rows.Next() {
		p, err := scan(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *p)
	}
	return out, rows.Err()
}

// AllIDs returns every person ID in creation order.
func (s *Storage) AllIDs(ctx context.Context, exec xdal.DBExecutor) ([]string, error) {
	query, args, err := s.qb.Select("id").From("persons").OrderBy("id").Build()
	if err != nil {
		return nil, err
	}
	rows, err := exec.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// EmailTaken reports whether a person already has email.
func (s *Storage) EmailTaken(ctx context.Context, exec xdal.DBExecutor, email string) (bool, error) {
	query, args, err := s.qb.Select("COUNT(*)").From("persons").Where("email = ?", email).Build()
	if err != nil {
		return false, err
	}
	var n int
	err = exec.QueryRow(ctx, query, args...).Scan(&n)
	return n > 0, err
}

type scanner interface {
	Scan(dest ...interface{}) error
}

func scan(row scanner) (*Person, error) {
	var p Person
	var created string
	if err := row.Scan(&p.ID, &p.FirstName, &p.LastName, &p.Email, &p.Phone, &p.JobTitle, &p.Company,
		&p.Address.Street, &p.Address.Suburb, &p.Address.State, &p.Address.Postcode, &p.Address.Country,
		&p.CreatedBy, &created); err != nil {
		return nil, err
	}
	p.CreatedAt = storeutil.ParseTime(created)
	return &p, nil
}

// randomSeed returns a non-zero seed from crypto/rand.
func randomSeed() uint64 {
	var b [8]byte
	_, _ = rand.Read(b[:])
	if v := binary.LittleEndian.Uint64(b[:]); v != 0 {
		return v
	}
	return 1
}
