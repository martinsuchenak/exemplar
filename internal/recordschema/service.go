// Package recordschema keeps the record schemas admins add through the API.
// They live in the database, beside the built-in schemas and the server's
// --record-schemas-dir files, which come first: an API schema never takes
// their names. Every change is checked against the whole set before it is
// stored, and takes effect when it commits, with no restart; requests
// already running finish on the set they started with.
package recordschema

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/martinsuchenak/xdal/pkg/xdal"
	"github.com/paularlott/logger"

	"github.com/martinsuchenak/exemplar/internal/apperr"
	"github.com/martinsuchenak/exemplar/internal/audit"
	"github.com/martinsuchenak/exemplar/internal/principal"
	"github.com/martinsuchenak/exemplar/internal/synth"
)

// Row is an API schema as the database holds it.
type Row struct {
	Name       string
	Version    int
	Definition string
	CreatedAt  time.Time
	CreatedBy  string
	UpdatedAt  time.Time
	UpdatedBy  string
}

// Definition is one schema with its file, whatever its source.
type Definition struct {
	Name   synth.RecordSchema `json:"name"`
	Source string             `json:"source"`
	// Version counts an API schema's changes, from 1.
	Version int    `json:"version,omitempty"`
	Hash    string `json:"hash,omitempty"`
	// Problem says why an API schema is not loaded.
	Problem   string     `json:"problem,omitempty"`
	CreatedAt *time.Time `json:"created_at,omitempty"`
	CreatedBy string     `json:"created_by,omitempty"`
	UpdatedAt *time.Time `json:"updated_at,omitempty"`
	UpdatedBy string     `json:"updated_by,omitempty"`
	// TOML is the schema file as it was given.
	TOML string `json:"toml"`
}

// Service adds, changes and removes API schemas, and holds the set the
// server generates from.
type Service struct {
	db      xdal.DBInterface
	storage *Storage
	audit   *audit.Service
	// base is the built-in schemas and the server's files.
	base *synth.SchemaSet
	log  logger.Logger
	now  func() time.Time
	// mu makes changes one at a time, so each is checked against the set
	// the one before it left.
	mu      sync.Mutex
	current atomic.Pointer[synth.SchemaSet]
}

// NewService returns a service on db whose set starts as base; Load adds
// the stored schemas. log may be nil.
func NewService(db xdal.DBInterface, auditService *audit.Service, base *synth.SchemaSet, log logger.Logger) *Service {
	s := &Service{db: db, storage: NewStorage(db.NewQueryBuilder()), audit: auditService, base: base, log: log, now: time.Now}
	s.current.Store(base)
	return s
}

// Load reads the stored schemas into the set. One that no longer loads (a
// file in the server's directory took its name, or a newer schema format
// left it behind) is left out with a warning and listed with its problem,
// so the server still starts and an admin can fix it.
func (s *Service) Load(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	rows, err := s.storage.List(ctx, s.db)
	if err != nil {
		return fmt.Errorf("reading record schemas: %w", err)
	}
	set := synth.WithStoredSchemas(s.base, stored(rows))
	for _, sc := range set.List() {
		if sc.Problem != "" && s.log != nil {
			s.log.Warn("record schema added through the API is not loaded; update or remove it", "schema", sc.Name, "error", sc.Problem)
		}
	}
	s.current.Store(set)
	return nil
}

// Current returns the set to generate from now.
func (s *Service) Current() *synth.SchemaSet {
	return s.current.Load()
}

// Get returns a schema and its file, for any key that can read, except
// that the server's own files (--record-schemas-dir) are shown to admin
// keys only: they are the deployment's, and may hold what only its admins
// should see. The built-in files are public. With apiOnly it returns the
// API schema of that name even when a server file has taken the name, so
// an admin can read it back before removing it.
func (s *Service) Get(ctx context.Context, actor principal.Principal, name string, apiOnly bool) (*Definition, error) {
	if !actor.CanRead() {
		return nil, fmt.Errorf("%w: this key cannot read", apperr.ErrForbidden)
	}
	set := s.Current()
	if sc, ok := set.Get(synth.RecordSchema(name)); ok && sc.Source != synth.SourceAPI && !apiOnly {
		if sc.Source == synth.SourceFile && !actor.IsAdmin() {
			return nil, fmt.Errorf("%w: the server's own schema files are shown to admin keys only", apperr.ErrForbidden)
		}
		return &Definition{Name: sc.Name, Source: sc.Source, Hash: sc.Hash, TOML: sc.Text()}, nil
	}
	r, err := s.storage.Get(ctx, s.db, name)
	if errors.Is(err, apperr.ErrNotFound) {
		return nil, fmt.Errorf("%w: no record schema %q added through the API", apperr.ErrNotFound, name)
	}
	if err != nil {
		return nil, err
	}
	return definition(r, set), nil
}

// Changes are made one at a time (mu), and only this service writes the
// table, so each reads the rows, works out and checks the set they make,
// and only then opens a transaction to write the row and its audit event:
// parsing the schemas never holds SQLite's one write lock.

// Create adds a schema. Only admins may: every key generates from it.
func (s *Service) Create(ctx context.Context, actor principal.Principal, text string) (*Definition, error) {
	if !actor.IsAdmin() {
		return nil, fmt.Errorf("%w: adding record schemas needs an admin key", apperr.ErrForbidden)
	}
	name, err := fileName(text)
	if err != nil {
		return nil, err
	}
	if sc, ok := s.base.Get(name); ok {
		return nil, fmt.Errorf("%w: %q is a %s schema; give yours another name", apperr.ErrConflict, name, sc.Source)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	rows, err := s.storage.List(ctx, s.db)
	if err != nil {
		return nil, err
	}
	if find(rows, string(name)) >= 0 {
		return nil, fmt.Errorf("%w: record schema %q already exists; update it", apperr.ErrConflict, name)
	}
	now := s.now()
	r := &Row{Name: string(name), Version: 1, Definition: text, CreatedAt: now, CreatedBy: actor.AccountID, UpdatedAt: now, UpdatedBy: actor.AccountID}
	next, err := s.next(append(rows, *r), r.Name)
	if err != nil {
		return nil, err
	}
	err = s.db.WithTx(ctx, nil, func(tx *xdal.Tx) error {
		if err := s.storage.Insert(ctx, tx, r); err != nil {
			if errors.Is(err, apperr.ErrConflict) {
				return fmt.Errorf("%w: record schema %q already exists; update it", apperr.ErrConflict, name)
			}
			return err
		}
		return s.record(ctx, tx, actor, audit.ActionRecordSchemaCreate, r, next, "")
	})
	if err != nil {
		return nil, err
	}
	s.current.Store(next)
	return definition(r, next), nil
}

// Update replaces a schema's file. A schema keeps its name; ifVersion,
// unless 0, must be its current version, so two admins cannot overwrite
// each other's change unseen. The same file again changes nothing.
func (s *Service) Update(ctx context.Context, actor principal.Principal, name, text string, ifVersion int) (*Definition, error) {
	if !actor.IsAdmin() {
		return nil, fmt.Errorf("%w: changing record schemas needs an admin key", apperr.ErrForbidden)
	}
	named, err := fileName(text)
	if err != nil {
		return nil, err
	}
	if string(named) != name {
		return nil, fmt.Errorf("%w: the file names %q, not %q; a schema keeps its name (add it as a new one)", apperr.ErrInvalid, named, name)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	rows, i, err := s.stored(ctx, name)
	if err != nil {
		return nil, err
	}
	r := rows[i]
	if ifVersion != 0 && ifVersion != r.Version {
		return nil, fmt.Errorf("%w: record schema %q is at version %d, not %d: it changed since you read it", apperr.ErrConflict, name, r.Version, ifVersion)
	}
	if r.Definition == text {
		return definition(&r, s.Current()), nil
	}
	previous := s.apiHash(name)
	r.Version++
	r.Definition, r.UpdatedAt, r.UpdatedBy = text, s.now(), actor.AccountID
	rows[i] = r
	next, err := s.next(rows, name)
	if err != nil {
		return nil, err
	}
	err = s.db.WithTx(ctx, nil, func(tx *xdal.Tx) error {
		if err := s.storage.Update(ctx, tx, &r); err != nil {
			return err
		}
		return s.record(ctx, tx, actor, audit.ActionRecordSchemaUpdate, &r, next, previous)
	})
	if err != nil {
		return nil, err
	}
	s.current.Store(next)
	return definition(&r, next), nil
}

// Delete removes a schema, unless another uses its catalogue. Fixtures
// already made from it keep its name and hash.
func (s *Service) Delete(ctx context.Context, actor principal.Principal, name string) error {
	if !actor.IsAdmin() {
		return fmt.Errorf("%w: removing record schemas needs an admin key", apperr.ErrForbidden)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	rows, i, err := s.stored(ctx, name)
	if err != nil {
		return err
	}
	r := rows[i]
	next, err := s.next(append(rows[:i:i], rows[i+1:]...), name)
	if err != nil {
		return err
	}
	hash := s.apiHash(name)
	err = s.db.WithTx(ctx, nil, func(tx *xdal.Tx) error {
		if err := s.storage.Delete(ctx, tx, name); err != nil {
			return err
		}
		return s.audit.Record(ctx, tx, audit.Event{
			AccountID: actor.AccountID, Action: audit.ActionRecordSchemaDelete, TargetType: "record_schema", TargetID: name,
			Details: map[string]any{"version": r.Version, "hash": hash},
		})
	})
	if err != nil {
		return err
	}
	s.current.Store(next)
	return nil
}

// stored returns every row and where the API schema called name is among
// them, saying so when name is one of the server's own instead.
func (s *Service) stored(ctx context.Context, name string) ([]Row, int, error) {
	rows, err := s.storage.List(ctx, s.db)
	if err != nil {
		return nil, 0, err
	}
	if i := find(rows, name); i >= 0 {
		return rows, i, nil
	}
	if sc, ok := s.base.Get(synth.RecordSchema(name)); ok {
		return nil, 0, fmt.Errorf("%w: %q is a %s schema; only schemas added through the API can be changed or removed", apperr.ErrConflict, name, sc.Source)
	}
	return nil, 0, fmt.Errorf("%w: no record schema %q added through the API", apperr.ErrNotFound, name)
}

// next makes the set rows give, and refuses the change if the schema it is
// about does not load, or if a schema that loaded before no longer does
// (one using the catalogue it changes or removes).
func (s *Service) next(rows []Row, target string) (*synth.SchemaSet, error) {
	if len(rows) > synth.MaxStoredSchemas {
		return nil, fmt.Errorf("%w: at most %d record schemas can be added through the API", apperr.ErrInvalid, synth.MaxStoredSchemas)
	}
	next := synth.WithStoredSchemas(s.base, stored(rows))
	if p := next.Problem(synth.RecordSchema(target)); p != "" {
		return nil, fmt.Errorf("%w: record schema %s: %s", apperr.ErrInvalid, target, p)
	}
	for _, sc := range s.Current().List() {
		if sc.Source != synth.SourceAPI || sc.Problem != "" || string(sc.Name) == target {
			continue
		}
		if p := next.Problem(sc.Name); p != "" {
			return nil, fmt.Errorf("%w: this change breaks record schema %s: %s", apperr.ErrInvalid, sc.Name, p)
		}
	}
	return next, nil
}

// apiHash is the hash of the loaded API schema called name, or "".
func (s *Service) apiHash(name string) string {
	if sc, ok := s.Current().Get(synth.RecordSchema(name)); ok && sc.Source == synth.SourceAPI {
		return sc.Hash
	}
	return ""
}

func find(rows []Row, name string) int {
	for i, r := range rows {
		if r.Name == name {
			return i
		}
	}
	return -1
}

// record writes the audit event for a create or update: the version and
// the file's hash (and the one it replaced), never the file itself.
func (s *Service) record(ctx context.Context, tx xdal.DBExecutor, actor principal.Principal, action string, r *Row, next *synth.SchemaSet, previous string) error {
	details := map[string]any{"version": r.Version}
	if sc, ok := next.Get(synth.RecordSchema(r.Name)); ok {
		details["hash"] = sc.Hash
	}
	if previous != "" {
		details["previous_hash"] = previous
	}
	return s.audit.Record(ctx, tx, audit.Event{
		AccountID: actor.AccountID, Action: action, TargetType: "record_schema", TargetID: r.Name, Details: details,
	})
}

// fileName checks a file's size and reads its name; the file is checked
// in full, catalogues included, with the rest of the set (next), so it is
// parsed once.
func fileName(text string) (synth.RecordSchema, error) {
	if len(text) > synth.MaxStoredSchemaBytes {
		return "", fmt.Errorf("%w: a record schema file is at most %d bytes", apperr.ErrTooLarge, synth.MaxStoredSchemaBytes)
	}
	name, err := synth.SchemaFileName(text)
	if err != nil {
		return "", fmt.Errorf("%w: %v", apperr.ErrInvalid, err)
	}
	return name, nil
}

func stored(rows []Row) []synth.StoredSchema {
	out := make([]synth.StoredSchema, len(rows))
	for i, r := range rows {
		out[i] = synth.StoredSchema{Name: synth.RecordSchema(r.Name), Version: r.Version, Text: r.Definition}
	}
	return out
}

func definition(r *Row, set *synth.SchemaSet) *Definition {
	created, updated := r.CreatedAt, r.UpdatedAt
	d := &Definition{
		Name: synth.RecordSchema(r.Name), Source: synth.SourceAPI, Version: r.Version, TOML: r.Definition,
		CreatedAt: &created, CreatedBy: r.CreatedBy, UpdatedAt: &updated, UpdatedBy: r.UpdatedBy,
	}
	if sc, ok := set.Get(d.Name); ok && sc.Source == synth.SourceAPI {
		d.Hash = sc.Hash
	} else {
		d.Problem = set.Problem(d.Name)
	}
	return d
}
