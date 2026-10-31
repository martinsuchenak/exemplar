// Package scrub anonymises files on request and gives them back, storing
// nothing: the anonymise tool. It runs inside the server for any API key,
// and in the CLI on its own (local mode), with the same rules. See
// docs/anonymisation.md.
package scrub

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"

	"github.com/martinsuchenak/xdal/pkg/xdal"

	"github.com/martinsuchenak/exemplar/internal/anonymise"
	"github.com/martinsuchenak/exemplar/internal/apperr"
	"github.com/martinsuchenak/exemplar/internal/audit"
	"github.com/martinsuchenak/exemplar/internal/person"
	"github.com/martinsuchenak/exemplar/internal/principal"
	"github.com/martinsuchenak/exemplar/internal/runlimit"
	"github.com/paularlott/logger"
)

// MaxFiles caps how many files one run takes.
const MaxFiles = 1000

// DefaultProfile is used when a request names none.
const DefaultProfile = "default"

// Config is the anonymiser setup.
type Config struct {
	// Detector finds names; nil leaves the tool unconfigured.
	Detector anonymise.Detector
	Profiles map[string]*anonymise.Profile
	// Key is the server's key, used only for linked fakes, or locally the
	// key from --anon-key-file. The zero Key means none.
	Key anonymise.Key
}

// Request describes one run.
type Request struct {
	// Profile defaults to "default".
	Profile string `json:"profile"`
	// LinkRegistry makes fakes with the server's key and registry, saving
	// new ones, as ingest does. Server and admin only.
	LinkRegistry bool `json:"link_registry"`
	// Report returns every replacement, original next to fake. Local only:
	// the server never sends original data back.
	Report bool `json:"-"`
}

// FileResult is the outcome for one input file, in the order sent.
type FileResult struct {
	// Name is the file's fake name, unique within the run.
	Name         string           `json:"name,omitempty"`
	Format       anonymise.Format `json:"format,omitempty"`
	Data         []byte           `json:"-"`
	Replacements int              `json:"replacements"`
	Attachments  int              `json:"attachments"`
	// AttachmentsRewritten counts attachments rewritten in place.
	AttachmentsRewritten int `json:"attachments_rewritten"`
	DroppedHeaders       int `json:"dropped_headers"`
	// DroppedParts are a document's parts removed rather than rewritten.
	DroppedParts []string `json:"dropped_parts,omitempty"`
	// Error is set for a file that failed while the others went ahead. On
	// the server a detector failure is only anonymise.DetectorUnavailable.
	Error string `json:"error,omitempty"`
	// Unavailable says the file failed because the detector did, so trying
	// again later may work.
	Unavailable bool `json:"-"`
	// Report is original data (local only).
	Report []anonymise.ReportLine `json:"-"`
}

// Result is the outcome of a run.
type Result struct {
	Profile string `json:"profile"`
	// Linked says the fakes came from the server's key and registry.
	Linked bool         `json:"linked"`
	Files  []FileResult `json:"files"`
}

// Service anonymises files and returns them.
type Service struct {
	cfg   *Config
	local bool
	// The server's registry and audit log; nil locally.
	db       xdal.DBInterface
	persons  *person.Service
	audit    *audit.Service
	maxBytes int64
	// runs caps concurrent anonymise runs across all keys; nil means no
	// cap. The per-key rule below still applies within it.
	runs *runlimit.Limiter
	// log gets why the detector failed, on the server; nil logs nothing.
	log logger.Logger

	mu      sync.Mutex
	running map[string]bool // key IDs with a run in progress
}

// NewService returns the server's anonymise tool. cfg nil, or without a
// detector, leaves it unconfigured. maxBytes caps one run's total input; 0
// means no limit. runs caps concurrent runs across all keys (shared with
// ingest); nil means no cap. log gets why the detector failed; nil logs
// nothing.
func NewService(db xdal.DBInterface, persons *person.Service, auditService *audit.Service, cfg *Config, maxBytes int64, runs *runlimit.Limiter, log logger.Logger) *Service {
	return &Service{cfg: cfg, db: db, persons: persons, audit: auditService, maxBytes: maxBytes, runs: runs, log: log, running: map[string]bool{}}
}

// NewLocal returns the tool for the CLI's local mode: no server, database,
// API key or audit log. The person running it owns the files.
func NewLocal(cfg *Config) *Service {
	return &Service{cfg: cfg, local: true, running: map[string]bool{}}
}

// MaxBytes is the most input one run accepts; 0 means no limit.
func (s *Service) MaxBytes() int64 {
	return s.maxBytes
}

// Anonymise anonymises files as one batch (a person found in one file is
// found in all of them) and returns them in order. On the server any key
// that can read may call it; fakes come from a key made for this run alone
// unless an admin asks for linked fakes. One file failing does not stop the
// others; its result carries the error. Nothing is stored or logged.
func (s *Service) Anonymise(ctx context.Context, actor principal.Principal, req Request, files []anonymise.File) (*Result, error) {
	run, err := s.Reserve(actor)
	if err != nil {
		return nil, err
	}
	defer run.Close()
	return run.Anonymise(ctx, req, files)
}

// Run is a key's one anonymise run, claimed before its files are read.
type Run struct {
	s      *Service
	actor  principal.Principal
	held   bool // the run holds a global slot as well as its key's
	closed bool
}

// Reserve claims the caller's run before its upload is read, so that one
// key cannot hold several uploads in memory at once: a key with a run in
// progress gets apperr.ErrBusy, and so does any caller when the server
// already holds its fill of runs (max_concurrent_runs). It checks what can
// be checked without the files. Close the run when done.
func (s *Service) Reserve(actor principal.Principal) (*Run, error) {
	if !s.local && !actor.CanRead() {
		return nil, fmt.Errorf("%w: anonymising needs a key that can read", apperr.ErrForbidden)
	}
	if s.cfg == nil || s.cfg.Detector == nil {
		if s.local {
			return nil, fmt.Errorf("%w: no detector: give --presidio-url, or --patterns-only to run without Presidio", apperr.ErrInvalid)
		}
		return nil, fmt.Errorf("%w: the anonymiser is not configured on this server (anonymiser.presidio_url)", apperr.ErrInvalid)
	}
	if !s.local {
		if !s.start(actor.KeyID) {
			return nil, fmt.Errorf("%w: this key already has an anonymise run in progress", apperr.ErrBusy)
		}
		if !s.runs.Acquire() {
			s.done(actor.KeyID)
			return nil, fmt.Errorf("%w: the server already has enough ingest or anonymise runs in progress; try again later", apperr.ErrBusy)
		}
	}
	return &Run{s: s, actor: actor, held: !s.local}, nil
}

// Close ends the run, letting the key start another. It may be called more
// than once.
func (r *Run) Close() {
	if r.closed {
		return
	}
	r.closed = true
	if r.held {
		r.s.done(r.actor.KeyID)
		r.s.runs.Release()
	}
}

// Anonymise runs the reserved run; see Service.Anonymise.
func (r *Run) Anonymise(ctx context.Context, req Request, files []anonymise.File) (*Result, error) {
	s, actor := r.s, r.actor
	if r.closed {
		return nil, errors.New("the anonymise run is closed")
	}
	if req.Profile == "" {
		req.Profile = DefaultProfile
	}
	profile, ok := s.cfg.Profiles[req.Profile]
	if !ok {
		return nil, fmt.Errorf("%w: unknown profile %q (have %s)", apperr.ErrInvalid, req.Profile, strings.Join(anonymise.ProfileNames(s.cfg.Profiles), ", "))
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
	if req.Report && !s.local {
		return nil, fmt.Errorf("%w: the server never returns original data; use ingest --dry-run, or anonymise --local", apperr.ErrInvalid)
	}
	if req.LinkRegistry {
		switch {
		case s.local:
			return nil, fmt.Errorf("%w: linked fakes need the server's registry; drop --local", apperr.ErrInvalid)
		case !actor.IsAdmin():
			return nil, fmt.Errorf("%w: linked fakes need an admin key", apperr.ErrForbidden)
		case !s.cfg.Key.Valid():
			return nil, fmt.Errorf("%w: linked fakes need the server's anonymisation key, and none is configured", apperr.ErrInvalid)
		}
	}

	engine, err := s.engine(actor, req, profile)
	if err != nil {
		return nil, err
	}
	res := &Result{Profile: profile.Name, Linked: req.LinkRegistry, Files: make([]FileResult, len(files))}
	prepared := engine.Prepare(ctx, files)
	names := map[string]bool{}
	formats := map[anonymise.Format]bool{}
	failed := 0
	for i, f := range files {
		fr := &res.Files[i]
		if err := prepared[i]; err != nil {
			s.fail(fr, err)
			failed++
			continue
		}
		out, err := engine.Anonymise(ctx, f)
		if err != nil {
			s.fail(fr, err)
			failed++
			continue
		}
		fr.Name, fr.Format, fr.Data = unique(names, out.Name), out.Format, out.Data
		fr.Replacements, fr.Attachments, fr.DroppedHeaders, fr.DroppedParts = len(out.Replacements), out.Attachments, len(out.DroppedHeaders), out.DroppedParts
		fr.AttachmentsRewritten = out.AttachmentsRewritten
		if req.Report {
			fr.Report = out.Report()
		}
		formats[out.Format] = true
	}
	if s.local {
		return res, nil
	}
	err = s.db.WithTx(ctx, nil, func(tx *xdal.Tx) error {
		details := map[string]any{
			"profile": profile.Name, "linked": req.LinkRegistry, "files": len(files), "failed": failed, "formats": sortedFormats(formats),
		}
		if req.LinkRegistry {
			details["key_id"] = s.cfg.Key.ID
		}
		return s.audit.Record(ctx, tx, audit.Event{
			AccountID: actor.AccountID, Action: audit.ActionFileAnonymise, TargetType: "account", TargetID: actor.AccountID, Details: details,
		})
	})
	if err != nil {
		return nil, err
	}
	return res, nil
}

// fail records why a file failed. A detector error's detail names the
// detector's URL, host and network: the server's callers get only
// anonymise.DetectorUnavailable, and the server logs what failed (never
// the file, its name or what Presidio said back), while locally the person
// running the tool sees it whole.
func (s *Service) fail(fr *FileResult, err error) {
	fr.Unavailable = errors.Is(err, anonymise.ErrDetector)
	if s.local {
		fr.Error = err.Error()
		return
	}
	fr.Error = anonymise.PublicError(err)
	if fr.Unavailable && s.log != nil {
		s.log.Warn("anonymise: the detector failed", "error", anonymise.DetectorFailure(err))
	}
}

// engine builds the run's engine. Linked fakes use the server's key and
// registry. Otherwise the registry lives for the run, and the key is made
// for it too, so its fakes match nothing in the library and cannot show
// whether a real name is there; locally a key file, if given, keeps fakes
// the same across runs.
func (s *Service) engine(actor principal.Principal, req Request, profile *anonymise.Profile) (*anonymise.Engine, error) {
	if req.LinkRegistry {
		registry, err := s.persons.Identities(actor, s.cfg.Key.ID, true)
		if err != nil {
			return nil, err
		}
		return anonymise.New(s.cfg.Key, profile, s.cfg.Detector, registry)
	}
	key := s.cfg.Key
	if !s.local || !key.Valid() {
		var err error
		if key, err = anonymise.RandomKey(); err != nil {
			return nil, err
		}
	}
	return anonymise.New(key, profile, s.cfg.Detector, anonymise.NewMemoryRegistry())
}

func (s *Service) start(keyID string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.running[keyID] {
		return false
	}
	s.running[keyID] = true
	return true
}

func (s *Service) done(keyID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.running, keyID)
}

// unique returns name, or name with -2, -3… before its extension when an
// earlier file of the run already has it (two inputs of the same name).
func unique(seen map[string]bool, name string) string {
	ext := anonymise.Extension(name)
	base := strings.TrimSuffix(name, ext)
	out := name
	for n := 2; seen[out]; n++ {
		out = fmt.Sprintf("%s-%d%s", base, n, ext)
	}
	seen[out] = true
	return out
}

func sortedFormats(set map[anonymise.Format]bool) []string {
	out := make([]string, 0, len(set))
	for f := range set {
		out = append(out, string(f))
	}
	sort.Strings(out)
	return out
}

// Checker compares files with their anonymised copies, for a reviewer, or
// a pipeline, to act on. It runs where both files are, the CLI's machine:
// it reads nothing stored and logs nothing, so any caller may use it.
type Checker struct{}

// Unchanged lists what anonymised holds exactly as original did
// (anonymise.Unchanged). Nothing is stored or logged.
func (Checker) Unchanged(ctx context.Context, _ principal.Principal, original, anonymised anonymise.File) ([]anonymise.Kept, error) {
	return anonymise.Unchanged(ctx, original, anonymised)
}
