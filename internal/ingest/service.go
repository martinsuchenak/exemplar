// Package ingest takes real emails, anonymises them and stores the results
// in a quarantine dataset, where they wait for an admin's review. Raw input
// is only ever held in memory for the request; only anonymised output is
// stored, and the dry-run report (which holds the originals) is returned to
// the caller, never stored or logged.
package ingest

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/martinsuchenak/xdal/pkg/xdal"

	"github.com/martinsuchenak/exemplar/internal/anonymise"
	"github.com/martinsuchenak/exemplar/internal/apperr"
	"github.com/martinsuchenak/exemplar/internal/audit"
	"github.com/martinsuchenak/exemplar/internal/dataset"
	"github.com/martinsuchenak/exemplar/internal/fixture"
	"github.com/martinsuchenak/exemplar/internal/person"
	"github.com/martinsuchenak/exemplar/internal/principal"
	"github.com/martinsuchenak/exemplar/internal/runlimit"
)

// Config is the anonymiser setup. A nil Config means ingest is not configured.
type Config struct {
	Key      anonymise.Key
	Detector anonymise.Detector
	Profiles map[string]*anonymise.Profile
}

// File is one raw input file.
type File struct {
	Name string
	Data []byte
}

// Request describes an ingest.
type Request struct {
	// Profile defaults to "default".
	Profile string `json:"profile"`
	// Batch names the ingest batch (for purge); empty generates one.
	Batch string `json:"batch"`
	// DryRun anonymises and reports without storing or creating people.
	DryRun bool `json:"dry_run"`
}

// ReportLine is one replacement. It holds original data.
type ReportLine = anonymise.ReportLine

// FileResult is the outcome for one input file.
type FileResult struct {
	Name         string `json:"name"`
	FixtureID    string `json:"fixture_id,omitempty"`
	FixtureName  string `json:"fixture_name,omitempty"`
	Replacements int    `json:"replacements"`
	Attachments  int    `json:"attachments"`
	// AttachmentsRewritten counts attachments rewritten in place rather
	// than replaced (the profile's attachments = "anonymise").
	AttachmentsRewritten int `json:"attachments_rewritten"`
	DroppedHeaders       int `json:"dropped_headers"`
	// DroppedParts counts a document's parts removed rather than rewritten.
	DroppedParts int          `json:"dropped_parts"`
	Error        string       `json:"error,omitempty"`
	Report       []ReportLine `json:"report,omitempty"`
}

// Result is the outcome of an ingest.
type Result struct {
	Batch   string       `json:"batch"`
	Profile string       `json:"profile"`
	KeyID   string       `json:"key_id"`
	DryRun  bool         `json:"dry_run"`
	Files   []FileResult `json:"files"`
}

const (
	// MaxFiles caps how many files one ingest takes.
	MaxFiles = 1000
	// DefaultProfile is used when a request names none.
	DefaultProfile = "default"
)

var batchRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)

// Service ingests real emails.
type Service struct {
	db       xdal.DBInterface
	datasets *dataset.Service
	fixtures *fixture.Service
	persons  *person.Service
	audit    *audit.Service
	cfg      *Config
	maxBytes int64
	now      func() time.Time
	// runs caps concurrent ingests; nil means no cap.
	runs *runlimit.Limiter
}

// NewService returns an ingest service; cfg nil leaves ingest unconfigured.
// maxBytes caps one request's total input; 0 means no limit. runs caps
// concurrent runs (shared with the anonymise tool); nil means no cap.
func NewService(db xdal.DBInterface, datasets *dataset.Service, fixtures *fixture.Service, persons *person.Service, auditService *audit.Service, cfg *Config, maxBytes int64, runs *runlimit.Limiter) *Service {
	return &Service{db: db, datasets: datasets, fixtures: fixtures, persons: persons, audit: auditService, cfg: cfg, maxBytes: maxBytes, now: time.Now, runs: runs}
}

// Begin claims an ingest slot before the upload is read, so concurrent
// ingests cannot hold the server's memory together over its budget; a
// caller gets apperr.ErrBusy when the server already has its fill (the
// anonymise tool holds the same budget). It first checks what can be checked
// without the files, so a caller that could never ingest takes no slot.
// Release with the returned func.
func (s *Service) Begin(actor principal.Principal) (release func(), err error) {
	if err := s.ready(actor); err != nil {
		return nil, err
	}
	if !s.runs.Acquire() {
		return nil, fmt.Errorf("%w: the server already has enough ingest or anonymise runs in progress; try again later", apperr.ErrBusy)
	}
	return s.runs.Release, nil
}

// ready checks the caller may ingest and the server is set up for it.
func (s *Service) ready(actor principal.Principal) error {
	if !actor.IsAdmin() {
		return fmt.Errorf("%w: ingesting real data needs an admin key", apperr.ErrForbidden)
	}
	if s.cfg == nil || !s.cfg.Key.Valid() || s.cfg.Detector == nil {
		return fmt.Errorf("%w: the anonymiser is not configured on this server (anonymiser.key_file and anonymiser.presidio_url)", apperr.ErrInvalid)
	}
	return nil
}

// MaxBytes is the most input one ingest accepts; 0 means no limit.
func (s *Service) MaxBytes() int64 {
	return s.maxBytes
}

// Profiles lists the profile names, for callers choosing one.
func (s *Service) Profiles() []string {
	if s.cfg == nil {
		return nil
	}
	return anonymise.ProfileNames(s.cfg.Profiles)
}

// Ingest anonymises files and, unless it is a dry run, stores them in a
// quarantine dataset as unreviewed fixtures. Admin only. One file failing does
// not stop the others; its result carries the error.
func (s *Service) Ingest(ctx context.Context, actor principal.Principal, datasetID string, req Request, files []File) (*Result, error) {
	if err := s.ready(actor); err != nil {
		return nil, err
	}
	if req.Profile == "" {
		req.Profile = DefaultProfile
	}
	profile, ok := s.cfg.Profiles[req.Profile]
	if !ok {
		return nil, fmt.Errorf("%w: unknown profile %q (have %s)", apperr.ErrInvalid, req.Profile, strings.Join(s.Profiles(), ", "))
	}
	if req.Batch == "" {
		req.Batch = newBatchID(s.now())
	}
	if !batchRe.MatchString(req.Batch) {
		return nil, fmt.Errorf("%w: batch must be 1-64 letters, digits, '.', '_' or '-'", apperr.ErrInvalid)
	}
	if len(files) == 0 || len(files) > MaxFiles {
		return nil, fmt.Errorf("%w: send 1-%d files", apperr.ErrInvalid, MaxFiles)
	}
	if s.maxBytes > 0 {
		var total int64
		for _, f := range files {
			total += int64(len(f.Data))
		}
		if total > s.maxBytes {
			return nil, fmt.Errorf("%w: the files are over %d bytes in total", apperr.ErrTooLarge, s.maxBytes)
		}
	}
	d, err := s.datasets.Authorize(ctx, nil, actor, datasetID, dataset.AccessWrite)
	if err != nil {
		return nil, err
	}
	if d.Scope != dataset.ScopeQuarantine {
		return nil, fmt.Errorf("%w: real data can only be ingested into a quarantine dataset", apperr.ErrInvalid)
	}

	registry, err := s.persons.Identities(actor, s.cfg.Key.ID, !req.DryRun)
	if err != nil {
		return nil, err
	}
	engine, err := anonymise.New(s.cfg.Key, profile, s.cfg.Detector, registry)
	if err != nil {
		return nil, err
	}

	res := &Result{Batch: req.Batch, Profile: profile.Name, KeyID: s.cfg.Key.ID, DryRun: req.DryRun}
	var batch []anonymise.File
	for _, f := range files {
		if _, err := anonymise.FormatOf(f.Name); err != nil {
			res.Files = append(res.Files, FileResult{Name: f.Name, Error: err.Error()})
			continue
		}
		batch = append(batch, anonymise.File{Name: f.Name, Data: f.Data})
	}
	// Learn every sender and recipient first, so names from one email's
	// headers are found in all of them. An email that cannot be read (over
	// the nesting or part limits, say) fails on its own.
	prepared := engine.Prepare(ctx, batch)

	stored, n := 0, 0
	for i, f := range batch {
		n++
		fr := FileResult{Name: f.Name}
		if err := prepared[i]; err != nil {
			fr.Error = anonymise.PublicError(err)
			if !errors.Is(err, anonymise.ErrDetector) {
				fr.Error = "reading the headers: " + fr.Error
			}
			res.Files = append(res.Files, fr)
			continue
		}
		out, err := engine.Anonymise(ctx, f)
		if err != nil {
			// A detector failure's detail (its URL, host and network)
			// stays on the server, unlogged.
			fr.Error = anonymise.PublicError(err)
			res.Files = append(res.Files, fr)
			continue
		}
		fr.Replacements, fr.Attachments, fr.DroppedHeaders, fr.DroppedParts = len(out.Replacements), out.Attachments, len(out.DroppedHeaders), len(out.DroppedParts)
		fr.AttachmentsRewritten = out.AttachmentsRewritten
		if req.DryRun {
			fr.Report = out.Report()
			res.Files = append(res.Files, fr)
			continue
		}
		links := make([]fixture.PersonLink, len(out.People))
		for i, p := range out.People {
			links[i] = fixture.PersonLink{PersonID: p.PersonID, Role: p.Role}
		}
		fr.FixtureName = fmt.Sprintf("%s-%03d%s", req.Batch, n, anonymise.Extension(f.Name))
		fx, err := s.fixtures.Create(ctx, actor, fixture.CreateInput{
			DatasetID:   datasetID,
			Name:        fr.FixtureName,
			Source:      fixture.SourceAnonymised,
			AnonProfile: profile.Name,
			AnonKeyID:   s.cfg.Key.ID,
			IngestBatch: req.Batch,
			People:      links,
			Content:     bytes.NewReader(out.Data),
		})
		if err != nil {
			fr.FixtureName, fr.Error = "", storeError(err)
		} else {
			fr.FixtureID = fx.ID
			stored++
		}
		res.Files = append(res.Files, fr)
	}

	err = s.db.WithTx(ctx, nil, func(tx *xdal.Tx) error {
		return s.audit.Record(ctx, tx, audit.Event{
			AccountID: actor.AccountID, Action: audit.ActionFixtureIngest, TargetType: "dataset", TargetID: datasetID,
			Details: map[string]any{
				"batch": req.Batch, "profile": profile.Name, "key_id": s.cfg.Key.ID, "dry_run": req.DryRun,
				"files": len(files), "stored": stored, "failed": failed(res),
			},
		})
	})
	if err != nil {
		return nil, err
	}
	return res, nil
}

// storeError keeps a storage failure's message without internal details.
func storeError(err error) string {
	for _, kind := range []error{apperr.ErrConflict, apperr.ErrInvalid, apperr.ErrTooLarge, apperr.ErrForbidden} {
		if errors.Is(err, kind) {
			return err.Error()
		}
	}
	return "storing the fixture failed"
}

func failed(r *Result) int {
	n := 0
	for _, f := range r.Files {
		if f.Error != "" {
			n++
		}
	}
	return n
}

func newBatchID(now time.Time) string {
	var b [3]byte
	_, _ = rand.Read(b[:])
	return "ingest-" + now.UTC().Format("20060102-150405") + "-" + hex.EncodeToString(b[:])
}
