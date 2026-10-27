// Package scrub anonymises files on request and gives them back, storing
// nothing: the anonymise tool. It runs inside the server for any API key,
// and in the CLI on its own (local mode), with the same rules. See
// docs/anonymisation.md.
package scrub

import (
	"context"
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
	Name           string           `json:"name,omitempty"`
	Format         anonymise.Format `json:"format,omitempty"`
	Data           []byte           `json:"-"`
	Replacements   int              `json:"replacements"`
	Attachments    int              `json:"attachments"`
	DroppedHeaders int              `json:"dropped_headers"`
	// Error is set for a file that failed while the others went ahead.
	Error string `json:"error,omitempty"`
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

	mu      sync.Mutex
	running map[string]bool // key IDs with a run in progress
}

// NewService returns the server's anonymise tool. cfg nil, or without a
// detector, leaves it unconfigured. maxBytes caps one run's total input; 0
// means no limit.
func NewService(db xdal.DBInterface, persons *person.Service, auditService *audit.Service, cfg *Config, maxBytes int64) *Service {
	return &Service{cfg: cfg, db: db, persons: persons, audit: auditService, maxBytes: maxBytes, running: map[string]bool{}}
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
	if !s.local && !actor.CanRead() {
		return nil, fmt.Errorf("%w: anonymising needs a key that can read", apperr.ErrForbidden)
	}
	if s.cfg == nil || s.cfg.Detector == nil {
		if s.local {
			return nil, fmt.Errorf("%w: no detector: give --presidio-url, or --patterns-only to run without Presidio", apperr.ErrInvalid)
		}
		return nil, fmt.Errorf("%w: the anonymiser is not configured on this server (anonymiser.presidio_url)", apperr.ErrInvalid)
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
	if !s.local {
		if !s.start(actor.KeyID) {
			return nil, fmt.Errorf("%w: this key already has an anonymise run in progress", apperr.ErrBusy)
		}
		defer s.done(actor.KeyID)
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
			fr.Error, failed = err.Error(), failed+1
			continue
		}
		out, err := engine.Anonymise(ctx, f)
		if err != nil {
			fr.Error, failed = err.Error(), failed+1
			continue
		}
		fr.Name, fr.Format, fr.Data = unique(names, out.Name), out.Format, out.Data
		fr.Replacements, fr.Attachments, fr.DroppedHeaders = len(out.Replacements), out.Attachments, len(out.DroppedHeaders)
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
