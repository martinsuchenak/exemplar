package fixture

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/martinsuchenak/xdal/pkg/xdal"

	"github.com/martinsuchenak/exemplar/internal/apperr"
	"github.com/martinsuchenak/exemplar/internal/storeutil"
)

var columns = []string{
	"id", "dataset_id", "name", "kind", "format", "mime_type", "blob_hash", "size", "scenario", "tags",
	"source", "generator", "anon_profile", "anon_key_id", "ingest_batch", "review_state", "reviewed_by",
	"reviewed_at", "created_by", "created_at",
}

// Storage holds the SQL for fixtures. Only the fixture service calls it.
type Storage struct {
	qb *xdal.QueryBuilder
}

// NewStorage returns storage that builds queries with qb.
func NewStorage(qb *xdal.QueryBuilder) *Storage {
	return &Storage{qb: qb}
}

// Insert inserts f; a taken name in the dataset returns apperr.ErrConflict.
func (s *Storage) Insert(ctx context.Context, exec xdal.DBExecutor, f *Fixture) error {
	query, args, err := s.qb.Insert("fixtures").
		Set("id", f.ID).
		Set("dataset_id", f.DatasetID).
		Set("name", f.Name).
		Set("kind", string(f.Kind)).
		Set("format", f.Format).
		Set("mime_type", f.MimeType).
		Set("blob_hash", f.BlobHash).
		Set("size", f.Size).
		Set("scenario", f.Scenario).
		Set("tags", storeutil.EncodeStrings(f.Tags)).
		Set("source", string(f.Source)).
		Set("generator", f.Generator).
		Set("anon_profile", f.AnonProfile).
		Set("anon_key_id", f.AnonKeyID).
		Set("ingest_batch", f.IngestBatch).
		Set("review_state", string(f.ReviewState)).
		Set("reviewed_by", nullString(f.ReviewedBy)).
		Set("reviewed_at", storeutil.TimeArg(f.ReviewedAt)).
		Set("created_by", f.CreatedBy).
		Set("created_at", storeutil.FormatTime(f.CreatedAt)).
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

// Get returns a fixture by id.
func (s *Storage) Get(ctx context.Context, exec xdal.DBExecutor, id string) (*Fixture, error) {
	query, args, err := s.qb.Select(columns...).From("fixtures").Where("id = ?", id).Build()
	if err != nil {
		return nil, err
	}
	f, err := scan(exec.QueryRow(ctx, query, args...))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, apperr.ErrNotFound
	}
	return f, err
}

// List returns a dataset's fixtures matching lf, ordered by name.
func (s *Storage) List(ctx context.Context, exec xdal.DBExecutor, datasetID string, lf ListFilter) ([]Fixture, error) {
	q := s.qb.Select(columns...).From("fixtures").Where("dataset_id = ?", datasetID)
	if lf.Kind != "" {
		q = q.Where("kind = ?", string(lf.Kind))
	}
	if lf.Format != "" {
		q = q.Where("format = ?", lf.Format)
	}
	if lf.Scenario != "" {
		q = q.Where("scenario = ?", lf.Scenario)
	}
	if lf.ReviewState != "" {
		q = q.Where("review_state = ?", string(lf.ReviewState))
	}
	if lf.Tag != "" {
		q = q.Where("EXISTS (SELECT 1 FROM json_each(fixtures.tags) WHERE json_each.value = ?)", lf.Tag)
	}
	query, args, err := q.OrderBy("name").Limit(int64(lf.Limit)).Offset(int64(lf.Offset)).Build()
	if err != nil {
		return nil, err
	}
	return s.query(ctx, exec, query, args)
}

func (s *Storage) query(ctx context.Context, exec xdal.DBExecutor, query string, args []interface{}) ([]Fixture, error) {
	rows, err := exec.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := []Fixture{}
	for rows.Next() {
		f, err := scan(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *f)
	}
	return out, rows.Err()
}

// ListAll returns every fixture in a dataset, ordered by name.
func (s *Storage) ListAll(ctx context.Context, exec xdal.DBExecutor, datasetID string) ([]Fixture, error) {
	query, args, err := s.qb.Select(columns...).From("fixtures").Where("dataset_id = ?", datasetID).OrderBy("name").Build()
	if err != nil {
		return nil, err
	}
	return s.query(ctx, exec, query, args)
}

// DeleteBatch removes every fixture from an ingest batch and returns how many.
func (s *Storage) DeleteBatch(ctx context.Context, exec xdal.DBExecutor, batch string) (int64, error) {
	query, args, err := s.qb.Delete("fixtures").Where("ingest_batch = ?", batch).Build()
	if err != nil {
		return 0, err
	}
	res, err := exec.Exec(ctx, query, args...)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// Delete removes a fixture row; its blob is collected separately.
func (s *Storage) Delete(ctx context.Context, exec xdal.DBExecutor, id string) error {
	query, args, err := s.qb.Delete("fixtures").Where("id = ?", id).Build()
	if err != nil {
		return err
	}
	_, err = exec.Exec(ctx, query, args...)
	return err
}

// SetReview records a review decision.
func (s *Storage) SetReview(ctx context.Context, exec xdal.DBExecutor, id string, state ReviewState, by string, at time.Time) error {
	query, args, err := s.qb.Update("fixtures").
		Set("review_state", string(state)).
		Set("reviewed_by", by).
		Set("reviewed_at", storeutil.FormatTime(at)).
		Where("id = ?", id).
		Build()
	if err != nil {
		return err
	}
	_, err = exec.Exec(ctx, query, args...)
	return err
}

func nullString(s string) interface{} {
	if s == "" {
		return nil
	}
	return s
}

type scanner interface {
	Scan(dest ...interface{}) error
}

func scan(row scanner) (*Fixture, error) {
	var f Fixture
	var kind, tags, source, review, created string
	var reviewedBy, reviewedAt sql.NullString
	if err := row.Scan(
		&f.ID, &f.DatasetID, &f.Name, &kind, &f.Format, &f.MimeType, &f.BlobHash, &f.Size, &f.Scenario, &tags,
		&source, &f.Generator, &f.AnonProfile, &f.AnonKeyID, &f.IngestBatch, &review, &reviewedBy,
		&reviewedAt, &f.CreatedBy, &created,
	); err != nil {
		return nil, err
	}
	f.Kind, f.Source, f.ReviewState = Kind(kind), Source(source), ReviewState(review)
	f.Tags = storeutil.DecodeStrings(tags)
	f.ReviewedBy = reviewedBy.String
	f.ReviewedAt = storeutil.NullTime(reviewedAt)
	f.CreatedAt = storeutil.ParseTime(created)
	return &f, nil
}
