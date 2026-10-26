// Package fixture manages the items in a dataset: their metadata, their
// content (through the blob store) and the review state of real-derived data.
// Dataset access is always decided by the dataset service.
package fixture

import (
	"context"
	"errors"
	"fmt"
	"io"
	"mime"
	"path"
	"strings"
	"time"

	"github.com/martinsuchenak/xdal/pkg/xdal"

	"github.com/martinsuchenak/exemplar/internal/apperr"
	"github.com/martinsuchenak/exemplar/internal/audit"
	"github.com/martinsuchenak/exemplar/internal/blob"
	"github.com/martinsuchenak/exemplar/internal/dataset"
	"github.com/martinsuchenak/exemplar/internal/principal"
	"github.com/martinsuchenak/exemplar/internal/storeutil"
)

// Kind is what a fixture represents.
type Kind string

const (
	KindEmail  Kind = "email"
	KindFile   Kind = "file"
	KindRecord Kind = "record"
)

// Valid reports whether k is a known kind.
func (k Kind) Valid() bool {
	return k == KindEmail || k == KindFile || k == KindRecord
}

// Source says where content came from. Only anonymised content is derived
// from real data, so only it goes through quarantine and review.
type Source string

const (
	SourceSynthetic    Source = "synthetic"
	SourceUploaded     Source = "uploaded"
	SourcePublicCorpus Source = "public-corpus"
	SourceAnonymised   Source = "anonymised"
)

// Valid reports whether s is a known source.
func (s Source) Valid() bool {
	return s == SourceSynthetic || s == SourceUploaded || s == SourcePublicCorpus || s == SourceAnonymised
}

// ReviewState tracks the review of real-derived fixtures; others are not-required.
type ReviewState string

const (
	ReviewNotRequired ReviewState = "not-required"
	ReviewUnreviewed  ReviewState = "unreviewed"
	ReviewApproved    ReviewState = "approved"
	ReviewRejected    ReviewState = "rejected"
)

// Valid reports whether r is a known review state.
func (r ReviewState) Valid() bool {
	return r == ReviewNotRequired || r == ReviewUnreviewed || r == ReviewApproved || r == ReviewRejected
}

// Fixture is one item in a dataset; its content is a blob.
type Fixture struct {
	ID          string      `json:"id"`
	DatasetID   string      `json:"dataset_id"`
	Name        string      `json:"name"`
	Kind        Kind        `json:"kind"`
	Format      string      `json:"format"`
	MimeType    string      `json:"mime_type"`
	BlobHash    string      `json:"blob_hash"`
	Size        int64       `json:"size"`
	Scenario    string      `json:"scenario"`
	Tags        []string    `json:"tags"`
	Source      Source      `json:"source"`
	Generator   string      `json:"generator,omitempty"`
	AnonProfile string      `json:"anon_profile,omitempty"`
	AnonKeyID   string      `json:"anon_key_id,omitempty"`
	IngestBatch string      `json:"ingest_batch,omitempty"`
	ReviewState ReviewState `json:"review_state"`
	ReviewedBy  string      `json:"reviewed_by,omitempty"`
	ReviewedAt  *time.Time  `json:"reviewed_at,omitempty"`
	CreatedBy   string      `json:"created_by"`
	CreatedAt   time.Time   `json:"created_at"`
}

// RealDerived reports whether the fixture came from real data.
func (f *Fixture) RealDerived() bool {
	return f.Source == SourceAnonymised
}

// Shareable reports whether the fixture may be copied out of its dataset:
// anything not real-derived, and real-derived fixtures once approved.
func (f *Fixture) Shareable() bool {
	return !f.RealDerived() || f.ReviewState == ReviewApproved
}

// CreateInput describes a new fixture. Format, Kind and MimeType are inferred
// from Name when empty; Source defaults to uploaded.
type CreateInput struct {
	DatasetID   string
	Name        string
	Kind        Kind
	Format      string
	MimeType    string
	Scenario    string
	Tags        []string
	Source      Source
	Generator   string
	AnonProfile string
	AnonKeyID   string
	IngestBatch string
	Content     io.Reader
}

// ListFilter narrows List; empty fields match everything. Limit defaults to
// 100 and is capped at 1000.
type ListFilter struct {
	Kind        Kind
	Format      string
	Scenario    string
	Tag         string
	ReviewState ReviewState
	Limit       int
	Offset      int
}

const (
	maxNameLen     = 255
	defaultListLim = 100
	maxListLimit   = 1000
)

// Service manages fixtures and their content.
type Service struct {
	db       xdal.DBInterface
	storage  *Storage
	blobs    *blob.Store
	datasets *dataset.Service
	audit    *audit.Service
	maxSize  int64
	now      func() time.Time
}

// NewService returns a fixture service. Dataset access is delegated to
// datasets. maxSize caps a fixture's content in bytes; 0 means no limit.
func NewService(db xdal.DBInterface, blobs *blob.Store, datasets *dataset.Service, auditService *audit.Service, maxSize int64) *Service {
	return &Service{
		db:       db,
		storage:  NewStorage(db.NewQueryBuilder()),
		blobs:    blobs,
		datasets: datasets,
		audit:    auditService,
		maxSize:  maxSize,
		now:      time.Now,
	}
}

// Create stores the content and adds the fixture to a dataset the caller may
// change. Anonymised (real-derived) content may only enter quarantine, where
// it waits for an admin's review; quarantine accepts nothing else.
func (s *Service) Create(ctx context.Context, actor principal.Principal, in CreateInput) (*Fixture, error) {
	f, err := s.prepare(actor, in)
	if err != nil {
		return nil, err
	}
	if in.Content == nil {
		return nil, fmt.Errorf("%w: content is required", apperr.ErrInvalid)
	}
	// Check access before writing any bytes; the transaction checks again.
	d, err := s.datasets.Authorize(ctx, s.db, actor, in.DatasetID, dataset.AccessWrite)
	if err != nil {
		return nil, err
	}
	if err := checkSourceFits(f.Source, d); err != nil {
		return nil, err
	}

	content := in.Content
	if s.maxSize > 0 {
		content = &capReader{r: content, left: s.maxSize}
	}

	release := s.blobs.Acquire()
	defer release()
	f.BlobHash, f.Size, err = s.blobs.Put(content)
	if errors.Is(err, errTooLarge) {
		return nil, fmt.Errorf("%w: fixture content is over %d bytes", apperr.ErrTooLarge, s.maxSize)
	}
	if err != nil {
		return nil, err
	}
	// Registered on its own, so a failed insert below leaves a row that
	// garbage collection will clean up rather than an untracked file.
	if err := s.blobs.Register(ctx, s.db, f.BlobHash, f.Size); err != nil {
		return nil, fmt.Errorf("registering blob: %w", err)
	}

	err = s.db.WithTx(ctx, nil, func(tx *xdal.Tx) error {
		if _, err := s.datasets.Authorize(ctx, tx, actor, in.DatasetID, dataset.AccessWrite); err != nil {
			return err
		}
		if err := s.storage.Insert(ctx, tx, f); err != nil {
			if errors.Is(err, apperr.ErrConflict) {
				return fmt.Errorf("%w: dataset already has a fixture named %q", apperr.ErrConflict, f.Name)
			}
			return err
		}
		return s.audit.Record(ctx, tx, audit.Event{
			AccountID: actor.AccountID, Action: audit.ActionFixtureCreate, TargetType: "fixture", TargetID: f.ID,
			Details: map[string]any{"dataset_id": f.DatasetID, "name": f.Name, "source": f.Source, "blob_hash": f.BlobHash},
		})
	})
	if err != nil {
		return nil, err
	}
	return f, nil
}

// Get returns a fixture whose dataset the caller can read.
func (s *Service) Get(ctx context.Context, actor principal.Principal, id string) (*Fixture, error) {
	f, err := s.storage.Get(ctx, s.db, id)
	if err != nil {
		return nil, fmt.Errorf("fixture %s: %w", id, apperr.ErrNotFound)
	}
	if _, err := s.datasets.Authorize(ctx, s.db, actor, f.DatasetID, dataset.AccessRead); err != nil {
		return nil, fmt.Errorf("fixture %s: %w", id, apperr.ErrNotFound)
	}
	return f, nil
}

// List returns a readable dataset's fixtures matching lf, ordered by name.
func (s *Service) List(ctx context.Context, actor principal.Principal, datasetID string, lf ListFilter) ([]Fixture, error) {
	if _, err := s.datasets.Authorize(ctx, s.db, actor, datasetID, dataset.AccessRead); err != nil {
		return nil, err
	}
	if lf.Kind != "" && !lf.Kind.Valid() {
		return nil, fmt.Errorf("%w: unknown kind %q", apperr.ErrInvalid, lf.Kind)
	}
	if lf.ReviewState != "" && !lf.ReviewState.Valid() {
		return nil, fmt.Errorf("%w: unknown review state %q", apperr.ErrInvalid, lf.ReviewState)
	}
	if lf.Limit <= 0 {
		lf.Limit = defaultListLim
	}
	if lf.Limit > maxListLimit {
		lf.Limit = maxListLimit
	}
	if lf.Offset < 0 {
		lf.Offset = 0
	}
	return s.storage.List(ctx, s.db, datasetID, lf)
}

// Open returns the fixture's content. Real-derived content is only served
// once approved (admins may read it in quarantine to review it), and every
// read of it is audit-logged.
func (s *Service) Open(ctx context.Context, actor principal.Principal, id string) (io.ReadCloser, *Fixture, error) {
	f, err := s.Get(ctx, actor, id)
	if err != nil {
		return nil, nil, err
	}
	if f.RealDerived() {
		if f.ReviewState != ReviewApproved && !actor.IsAdmin() {
			return nil, nil, fmt.Errorf("%w: fixture %s has not been approved", apperr.ErrForbidden, id)
		}
		err := s.db.WithTx(ctx, nil, func(tx *xdal.Tx) error {
			return s.audit.Record(ctx, tx, audit.Event{
				AccountID: actor.AccountID, Action: audit.ActionFixtureDownload, TargetType: "fixture", TargetID: f.ID,
				Details: map[string]any{"dataset_id": f.DatasetID, "review_state": f.ReviewState},
			})
		})
		if err != nil {
			return nil, nil, err
		}
	}
	r, err := s.blobs.Open(f.BlobHash)
	if err != nil {
		return nil, nil, err
	}
	return r, f, nil
}

// Delete removes a fixture from a dataset the caller may change, then any
// blob nothing else uses.
func (s *Service) Delete(ctx context.Context, actor principal.Principal, id string) error {
	err := s.db.WithTx(ctx, nil, func(tx *xdal.Tx) error {
		f, err := s.storage.Get(ctx, tx, id)
		if err != nil {
			return fmt.Errorf("fixture %s: %w", id, apperr.ErrNotFound)
		}
		if _, err := s.datasets.Authorize(ctx, tx, actor, f.DatasetID, dataset.AccessWrite); err != nil {
			return err
		}
		if err := s.storage.Delete(ctx, tx, id); err != nil {
			return err
		}
		return s.audit.Record(ctx, tx, audit.Event{
			AccountID: actor.AccountID, Action: audit.ActionFixtureDelete, TargetType: "fixture", TargetID: id,
			Details: map[string]any{"dataset_id": f.DatasetID, "name": f.Name},
		})
	})
	if err != nil {
		return err
	}
	_, err = s.blobs.Collect(ctx, s.db)
	return err
}

// Review approves or rejects an unreviewed real-derived fixture. Admin only.
func (s *Service) Review(ctx context.Context, actor principal.Principal, id string, approve bool) (*Fixture, error) {
	if !actor.IsAdmin() {
		return nil, fmt.Errorf("%w: reviewing fixtures needs an admin key", apperr.ErrForbidden)
	}
	var f *Fixture
	err := s.db.WithTx(ctx, nil, func(tx *xdal.Tx) error {
		var err error
		f, err = s.storage.Get(ctx, tx, id)
		if err != nil {
			return fmt.Errorf("fixture %s: %w", id, apperr.ErrNotFound)
		}
		if f.ReviewState != ReviewUnreviewed {
			return fmt.Errorf("%w: fixture %s is %s, not unreviewed", apperr.ErrConflict, id, f.ReviewState)
		}
		if _, err := s.datasets.Authorize(ctx, tx, actor, f.DatasetID, dataset.AccessWrite); err != nil {
			return err
		}
		state, action := ReviewApproved, audit.ActionFixtureApprove
		if !approve {
			state, action = ReviewRejected, audit.ActionFixtureReject
		}
		now := s.now()
		if err := s.storage.SetReview(ctx, tx, id, state, actor.AccountID, now); err != nil {
			return err
		}
		f.ReviewState, f.ReviewedBy, f.ReviewedAt = state, actor.AccountID, &now
		return s.audit.Record(ctx, tx, audit.Event{
			AccountID: actor.AccountID, Action: action, TargetType: "fixture", TargetID: id,
			Details: map[string]any{"dataset_id": f.DatasetID},
		})
	})
	if err != nil {
		return nil, err
	}
	return f, nil
}

// CopyShareable copies srcID's fixtures into dstID inside exec's transaction,
// keeping their content, provenance and review state. Real-derived fixtures
// that are not approved are skipped. It implements dataset.FixtureCopier;
// the dataset service has already checked access to both datasets.
func (s *Service) CopyShareable(ctx context.Context, exec xdal.DBExecutor, srcID, dstID, actorID string) (copied, skipped int, err error) {
	fixtures, err := s.storage.ListAll(ctx, exec, srcID)
	if err != nil {
		return 0, 0, err
	}
	now := s.now()
	for _, f := range fixtures {
		if !f.Shareable() {
			skipped++
			continue
		}
		f.ID, f.DatasetID, f.CreatedBy, f.CreatedAt = storeutil.NewID(), dstID, actorID, now
		if err := s.storage.Insert(ctx, exec, &f); err != nil {
			return 0, 0, fmt.Errorf("copying fixture %s: %w", f.Name, err)
		}
		copied++
	}
	return copied, skipped, nil
}

// PurgeBatch deletes every fixture from an ingest batch, in every dataset it
// was copied to, frozen or not, then any blob nothing else uses. It is how
// real-derived data is removed on request. Admin only.
func (s *Service) PurgeBatch(ctx context.Context, actor principal.Principal, batch string) (int64, error) {
	if !actor.IsAdmin() {
		return 0, fmt.Errorf("%w: purging needs an admin key", apperr.ErrForbidden)
	}
	batch = strings.TrimSpace(batch)
	if batch == "" {
		return 0, fmt.Errorf("%w: ingest batch is required", apperr.ErrInvalid)
	}
	var n int64
	err := s.db.WithTx(ctx, nil, func(tx *xdal.Tx) error {
		var err error
		n, err = s.storage.DeleteBatch(ctx, tx, batch)
		if err != nil {
			return err
		}
		return s.audit.Record(ctx, tx, audit.Event{
			AccountID: actor.AccountID, Action: audit.ActionFixturePurge, TargetType: "ingest_batch", TargetID: batch,
			Details: map[string]any{"fixtures": n},
		})
	})
	if err != nil {
		return 0, err
	}
	if _, err := s.blobs.Collect(ctx, s.db); err != nil {
		return n, err
	}
	return n, nil
}

// prepare validates the input and fills in inferred fields.
func (s *Service) prepare(actor principal.Principal, in CreateInput) (*Fixture, error) {
	name := strings.TrimSpace(in.Name)
	if name == "" || len(name) > maxNameLen || strings.ContainsAny(name, `/\`) || name == "." || name == ".." {
		return nil, fmt.Errorf("%w: fixture name must be 1-%d characters without slashes", apperr.ErrInvalid, maxNameLen)
	}
	format := strings.ToLower(strings.TrimPrefix(in.Format, "."))
	if format == "" {
		format = strings.ToLower(strings.TrimPrefix(path.Ext(name), "."))
	}
	if format == "" {
		return nil, fmt.Errorf("%w: format is required when the name has no extension", apperr.ErrInvalid)
	}
	kind := in.Kind
	if kind == "" {
		kind = KindFile
		if format == "eml" || format == "msg" {
			kind = KindEmail
		}
	}
	if !kind.Valid() {
		return nil, fmt.Errorf("%w: kind must be email, file or record", apperr.ErrInvalid)
	}
	source := in.Source
	if source == "" {
		source = SourceUploaded
	}
	if !source.Valid() {
		return nil, fmt.Errorf("%w: unknown source %q", apperr.ErrInvalid, source)
	}
	review := ReviewNotRequired
	if source == SourceAnonymised {
		review = ReviewUnreviewed
	}
	mimeType := in.MimeType
	if mimeType == "" {
		mimeType = mimeFor(format)
	}
	return &Fixture{
		ID:          storeutil.NewID(),
		DatasetID:   in.DatasetID,
		Name:        name,
		Kind:        kind,
		Format:      format,
		MimeType:    mimeType,
		Scenario:    strings.TrimSpace(in.Scenario),
		Tags:        normaliseTags(in.Tags),
		Source:      source,
		Generator:   in.Generator,
		AnonProfile: in.AnonProfile,
		AnonKeyID:   in.AnonKeyID,
		IngestBatch: in.IngestBatch,
		ReviewState: review,
		CreatedBy:   actor.AccountID,
		CreatedAt:   s.now(),
	}, nil
}

func checkSourceFits(source Source, d *dataset.Dataset) error {
	quarantine := d.Scope == dataset.ScopeQuarantine
	if source == SourceAnonymised && !quarantine {
		return fmt.Errorf("%w: anonymised fixtures must be added to a quarantine dataset", apperr.ErrInvalid)
	}
	if source != SourceAnonymised && quarantine {
		return fmt.Errorf("%w: quarantine datasets only hold anonymised fixtures", apperr.ErrInvalid)
	}
	return nil
}

func mimeFor(format string) string {
	switch format {
	case "eml":
		return "message/rfc822"
	case "msg":
		return "application/vnd.ms-outlook"
	}
	if t := mime.TypeByExtension("." + format); t != "" {
		return t
	}
	return "application/octet-stream"
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

var errTooLarge = errors.New("content over size limit")

// capReader passes through at most left bytes and fails if the source has
// more, so an oversized upload stops before it is stored.
type capReader struct {
	r    io.Reader
	left int64
}

// Read implements io.Reader.
func (c *capReader) Read(p []byte) (int, error) {
	if c.left <= 0 {
		var probe [1]byte
		n, err := c.r.Read(probe[:])
		if n > 0 {
			return 0, errTooLarge
		}
		return 0, err
	}
	if int64(len(p)) > c.left {
		p = p[:c.left]
	}
	n, err := c.r.Read(p)
	c.left -= int64(n)
	return n, err
}
