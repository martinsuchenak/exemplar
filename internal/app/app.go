// Package app opens the database and blob store and builds the services.
// Every entry point (serve, CLI commands, MCP tools) gets its services from
// here, so they all share the same access rules.
package app

import (
	"context"
	"fmt"

	"github.com/paularlott/logger"

	"github.com/martinsuchenak/exemplar/build"
	"github.com/martinsuchenak/exemplar/internal/account"
	"github.com/martinsuchenak/exemplar/internal/anonymise"
	"github.com/martinsuchenak/exemplar/internal/audit"
	"github.com/martinsuchenak/exemplar/internal/backup"
	"github.com/martinsuchenak/exemplar/internal/blob"
	"github.com/martinsuchenak/exemplar/internal/dataset"
	"github.com/martinsuchenak/exemplar/internal/db"
	"github.com/martinsuchenak/exemplar/internal/exchange"
	"github.com/martinsuchenak/exemplar/internal/fixture"
	"github.com/martinsuchenak/exemplar/internal/generate"
	"github.com/martinsuchenak/exemplar/internal/ingest"
	"github.com/martinsuchenak/exemplar/internal/person"
	"github.com/martinsuchenak/exemplar/internal/recordschema"
	"github.com/martinsuchenak/exemplar/internal/runlimit"
	"github.com/martinsuchenak/exemplar/internal/scrub"
	"github.com/martinsuchenak/exemplar/internal/synth"
)

// Config locates an instance's data.
type Config struct {
	// DBPath is the SQLite database file.
	DBPath string
	// BlobDir holds fixture contents.
	BlobDir string
	// MaxFixtureBytes caps one fixture's content; 0 means no limit.
	MaxFixtureBytes int64
	// MaxIngestBytes caps one ingest request's input; 0 means no limit.
	MaxIngestBytes int64
	// MaxImportBytes caps one import archive; 0 means no limit.
	MaxImportBytes int64
	// MaxConcurrentRuns caps how many ingest and anonymise runs the
	// server holds at once, together, and separately how many record
	// generations; 0 means no limit.
	MaxConcurrentRuns int
	// AnonKey is the anonymisation secret; with PresidioURL (or
	// AnonDetector) it enables ingest of real data, and linked fakes in the
	// anonymise tool.
	AnonKey []byte
	// PresidioURL is the Presidio Analyzer service, e.g.
	// http://localhost:3000. On its own it enables the anonymise tool.
	PresidioURL string
	// AnonDetector replaces Presidio; tests use it.
	AnonDetector anonymise.Detector
	// NoMigrate opens an existing database without migrating it, and fails
	// unless its schema matches this build. For commands that run beside a
	// live server.
	NoMigrate bool
	// AnonProfilesDir holds extra anonymisation profiles (*.toml).
	AnonProfilesDir string
	// RecordSchemasDir holds extra record schemas (*.toml); a file named
	// like a built-in schema replaces it.
	RecordSchemasDir string
}

// App holds the services every entry point uses.
type App struct {
	conn     *db.Connection
	Audit    *audit.Service
	Accounts *account.Service
	Datasets *dataset.Service
	Fixtures *fixture.Service
	Persons  *person.Service
	Generate *generate.Service
	// RecordSchemas adds, changes and removes record schemas through the
	// API, and holds the set Generate uses.
	RecordSchemas *recordschema.Service
	Ingest        *ingest.Service
	// Scrub is the anonymise tool: files in, anonymised files out, nothing
	// stored.
	Scrub    *scrub.Service
	Exchange *exchange.Service
	// Backup is for host commands only; no API reaches it.
	Backup *backup.Service
}

// Open opens the database (running migrations) and blob store and builds the
// services. log may be nil.
func Open(ctx context.Context, log logger.Logger, cfg Config) (*App, error) {
	if cfg.DBPath == "" || cfg.BlobDir == "" {
		return nil, fmt.Errorf("database path and blob directory are required")
	}
	open := db.Open
	if cfg.NoMigrate {
		open = db.OpenCurrent
	}
	conn, err := open(ctx, log, cfg.DBPath)
	if err != nil {
		return nil, err
	}
	blobs, err := blob.NewStore(cfg.BlobDir, conn.DB.NewQueryBuilder())
	if err != nil {
		_ = conn.Close()
		return nil, err
	}
	auditService := audit.NewService(conn.DB)
	datasets := dataset.NewService(conn.DB, blobs, auditService)
	fixtures := fixture.NewService(conn.DB, blobs, datasets, auditService, cfg.MaxFixtureBytes)
	datasets.UseFixtures(fixtures)
	persons := person.NewService(conn.DB, auditService)
	schemas, replaced, err := synth.LoadSchemas(cfg.RecordSchemasDir)
	if err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("loading record schemas: %w", err)
	}
	if log != nil {
		for _, name := range replaced {
			log.Info("record schema file replaces the built-in one", "schema", name, "dir", cfg.RecordSchemasDir)
		}
	}
	recordSchemas := recordschema.NewService(conn.DB, auditService, schemas, log)
	if err := recordSchemas.Load(ctx); err != nil {
		_ = conn.Close()
		return nil, err
	}
	ingestCfg, scrubCfg, err := anonymiserConfig(cfg)
	if err != nil {
		_ = conn.Close()
		return nil, err
	}
	runs := runlimit.New(cfg.MaxConcurrentRuns)
	return &App{
		conn:     conn,
		Audit:    auditService,
		Accounts: account.NewService(conn.DB, auditService),
		Datasets: datasets,
		Fixtures: fixtures,
		Persons:  persons,
		// Record generations get a cap of their own, the same size: sharing
		// the anonymise runs' would leave generation busy for as long as a
		// long anonymise batch runs.
		Generate:      generate.NewService(conn.DB, datasets, fixtures, persons, auditService, recordSchemas, runlimit.New(cfg.MaxConcurrentRuns)),
		RecordSchemas: recordSchemas,
		Ingest:        ingest.NewService(conn.DB, datasets, fixtures, persons, auditService, ingestCfg, cfg.MaxIngestBytes, runs, log),
		Scrub:         scrub.NewService(conn.DB, persons, auditService, scrubCfg, cfg.MaxIngestBytes, runs, log),
		Exchange:      exchange.NewService(conn.DB, datasets, fixtures, persons, auditService, build.Version, cfg.MaxImportBytes),
		Backup:        backup.NewService(conn, blobs, auditService, cfg.DBPath, build.Version),
	}, nil
}

// anonymiserConfig builds the ingest setup, nil unless both a key and a
// detector are configured (ingest then reports that it is not configured),
// and the anonymise tool's, which needs only a detector: its fakes come from
// a key made for each run, and the server's key only for linked fakes.
func anonymiserConfig(cfg Config) (*ingest.Config, *scrub.Config, error) {
	profiles, err := anonymise.LoadProfiles(cfg.AnonProfilesDir)
	if err != nil {
		return nil, nil, fmt.Errorf("loading anonymisation profiles: %w", err)
	}
	var key anonymise.Key
	if len(cfg.AnonKey) > 0 {
		if key, err = anonymise.NewKey(cfg.AnonKey); err != nil {
			return nil, nil, err
		}
	}
	detector := cfg.AnonDetector
	if detector == nil && cfg.PresidioURL != "" {
		detector = anonymise.NewPresidio(cfg.PresidioURL)
	}
	scrubCfg := &scrub.Config{Detector: detector, Profiles: profiles, Key: key}
	if !key.Valid() || detector == nil {
		return nil, scrubCfg, nil
	}
	return &ingest.Config{Key: key, Detector: detector, Profiles: profiles}, scrubCfg, nil
}

// LocalConfig sets up the anonymise tool on its own, for the CLI's local
// mode.
type LocalConfig struct {
	// Key keeps fakes the same across runs; empty makes one per run.
	Key []byte
	// PresidioURL is the Presidio Analyzer service.
	PresidioURL string
	// PatternsOnly runs without Presidio: the patterns and known names only.
	PatternsOnly bool
	// Detector replaces Presidio; tests use it.
	Detector anonymise.Detector
	// ProfilesDir holds extra anonymisation profiles (*.toml).
	ProfilesDir string
}

// LocalAnonymiser returns the anonymise tool without a database or server.
func LocalAnonymiser(cfg LocalConfig) (*scrub.Service, error) {
	if cfg.PatternsOnly && cfg.PresidioURL != "" {
		return nil, fmt.Errorf("give either a Presidio URL or patterns only, not both")
	}
	_, scrubCfg, err := anonymiserConfig(Config{AnonKey: cfg.Key, PresidioURL: cfg.PresidioURL, AnonDetector: cfg.Detector, AnonProfilesDir: cfg.ProfilesDir})
	if err != nil {
		return nil, err
	}
	if cfg.PatternsOnly && scrubCfg.Detector == nil {
		scrubCfg.Detector = anonymise.PatternsOnly{}
	}
	return scrub.NewLocal(scrubCfg), nil
}

// LocalGenerator returns record generation without a database or server,
// for the CLI's local mode: the built-in schemas plus every *.toml file in
// schemasDir (empty for none).
func LocalGenerator(schemasDir string) (*generate.Service, error) {
	schemas, _, err := synth.LoadSchemas(schemasDir)
	if err != nil {
		return nil, err
	}
	return generate.NewLocal(schemas), nil
}

// Checker returns the anonymise tool's check, which needs no database,
// key or detector.
func Checker() *scrub.Checker {
	return &scrub.Checker{}
}

// Close closes the database.
func (a *App) Close() error {
	return a.conn.Close()
}
