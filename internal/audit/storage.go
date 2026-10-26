package audit

import (
	"context"
	"encoding/json"

	"github.com/martinsuchenak/xdal/pkg/xdal"

	"github.com/martinsuchenak/exemplar/internal/storeutil"
)

const table = "audit_events"

var columns = []string{"seq", "id", "at", "account_id", "action", "target_type", "target_id", "details"}

// Storage holds the SQL for audit_events. Only the audit service calls it.
type Storage struct {
	qb *xdal.QueryBuilder
}

// NewStorage returns storage that builds queries with qb.
func NewStorage(qb *xdal.QueryBuilder) *Storage {
	return &Storage{qb: qb}
}

// Insert appends e.
func (s *Storage) Insert(ctx context.Context, exec xdal.DBExecutor, e *Event) error {
	details, err := json.Marshal(e.Details)
	if err != nil {
		return err
	}
	query, args, err := s.qb.Insert(table).
		Set("id", e.ID).
		Set("at", storeutil.FormatTime(e.At)).
		Set("account_id", e.AccountID).
		Set("action", e.Action).
		Set("target_type", e.TargetType).
		Set("target_id", e.TargetID).
		Set("details", string(details)).
		Build()
	if err != nil {
		return err
	}
	_, err = exec.Exec(ctx, query, args...)
	return err
}

// List returns events matching f in seq order.
func (s *Storage) List(ctx context.Context, exec xdal.DBExecutor, f Filter) ([]Event, error) {
	q := s.qb.Select(columns...).From(table).Where("seq > ?", f.AfterSeq)
	if f.AccountID != "" {
		q = q.Where("account_id = ?", f.AccountID)
	}
	if f.Action != "" {
		q = q.Where("action = ?", f.Action)
	}
	if f.TargetType != "" {
		q = q.Where("target_type = ?", f.TargetType)
	}
	if f.TargetID != "" {
		q = q.Where("target_id = ?", f.TargetID)
	}
	query, args, err := q.OrderBy("seq").Limit(int64(f.Limit)).Build()
	if err != nil {
		return nil, err
	}
	rows, err := exec.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	events := []Event{}
	for rows.Next() {
		var e Event
		var at, details string
		if err := rows.Scan(&e.Seq, &e.ID, &at, &e.AccountID, &e.Action, &e.TargetType, &e.TargetID, &details); err != nil {
			return nil, err
		}
		e.At = storeutil.ParseTime(at)
		if err := json.Unmarshal([]byte(details), &e.Details); err != nil {
			e.Details = map[string]any{"raw": details}
		}
		events = append(events, e)
	}
	return events, rows.Err()
}
