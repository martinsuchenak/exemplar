// Package audit records security-relevant actions in the append-only
// audit_events table. Other services record events inside their own
// transactions, so an action and its audit entry commit together.
package audit

import (
	"context"
	"fmt"
	"time"

	"github.com/martinsuchenak/xdal/pkg/xdal"

	"github.com/martinsuchenak/exemplar/internal/apperr"
	"github.com/martinsuchenak/exemplar/internal/principal"
	"github.com/martinsuchenak/exemplar/internal/storeutil"
)

// Actions recorded by the services.
const (
	ActionBootstrap       = "account.bootstrap"
	ActionRecover         = "account.recover"
	ActionAccountCreate   = "account.create"
	ActionAccountDisable  = "account.disable"
	ActionKeyCreate       = "key.create"
	ActionKeyRevoke       = "key.revoke"
	ActionDatasetCreate   = "dataset.create"
	ActionDatasetFreeze   = "dataset.freeze"
	ActionDatasetDelete   = "dataset.delete"
	ActionDatasetFork     = "dataset.fork"
	ActionDatasetPromote  = "dataset.promote"
	ActionFixtureCreate   = "fixture.create"
	ActionFixtureDelete   = "fixture.delete"
	ActionFixtureDownload = "fixture.download"
	ActionFixtureApprove  = "fixture.approve"
	ActionFixtureReject   = "fixture.reject"
	ActionFixturePurge    = "fixture.purge"
	ActionFixtureGenerate = "fixture.generate"
	ActionPersonGenerate  = "person.generate"
)

// Event is one audit log entry. Seq orders events and pages through them.
type Event struct {
	Seq        int64          `json:"seq"`
	ID         string         `json:"id"`
	At         time.Time      `json:"at"`
	AccountID  string         `json:"account_id"`
	Action     string         `json:"action"`
	TargetType string         `json:"target_type"`
	TargetID   string         `json:"target_id"`
	Details    map[string]any `json:"details"`
}

// Filter narrows List. AfterSeq pages through the log in order.
type Filter struct {
	AccountID  string
	Action     string
	TargetType string
	TargetID   string
	AfterSeq   int64
	Limit      int
}

const maxListLimit = 1000

// Service records and lists audit events.
type Service struct {
	db      xdal.DBInterface
	storage *Storage
	now     func() time.Time
}

// NewService returns an audit service on db.
func NewService(db xdal.DBInterface) *Service {
	return &Service{db: db, storage: NewStorage(db.NewQueryBuilder()), now: time.Now}
}

// Record appends an event using exec, normally the caller's transaction.
func (s *Service) Record(ctx context.Context, exec xdal.DBExecutor, e Event) error {
	e.ID = storeutil.NewID()
	e.At = s.now()
	if e.Details == nil {
		e.Details = map[string]any{}
	}
	if err := s.storage.Insert(ctx, exec, &e); err != nil {
		return fmt.Errorf("recording audit event %s: %w", e.Action, err)
	}
	return nil
}

// List returns events in order. Only admins may read the audit log.
func (s *Service) List(ctx context.Context, actor principal.Principal, f Filter) ([]Event, error) {
	if !actor.IsAdmin() {
		return nil, fmt.Errorf("%w: reading the audit log needs an admin key", apperr.ErrForbidden)
	}
	if f.Limit <= 0 || f.Limit > maxListLimit {
		f.Limit = maxListLimit
	}
	return s.storage.List(ctx, s.db, f)
}
