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
	// AnonKey is the anonymisation secret; with PresidioURL (or
	// AnonDetector) it enables ingest of real emails.
	AnonKey []byte
	// PresidioURL is the Presidio Analyzer service, e.g. http://localhost:3000.
	PresidioURL string
	// AnonDetector replaces Presidio; tests use it.
	AnonDetector anonymise.Detector
	// AnonProfilesDir holds extra anonymisation profiles (*.toml).
	AnonProfilesDir string
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
	Ingest   *ingest.Service
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
	conn, err := db.Open(ctx, log, cfg.DBPath)
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
	ingestCfg, err := anonymiserConfig(cfg)
	if err != nil {
		_ = conn.Close()
		return nil, err
	}
	return &App{
		conn:     conn,
		Audit:    auditService,
		Accounts: account.NewService(conn.DB, auditService),
		Datasets: datasets,
		Fixtures: fixtures,
		Persons:  persons,
		Generate: generate.NewService(conn.DB, datasets, fixtures, persons, auditService),
		Ingest:   ingest.NewService(conn.DB, datasets, fixtures, persons, auditService, ingestCfg, cfg.MaxIngestBytes),
		Exchange: exchange.NewService(conn.DB, datasets, fixtures, persons, auditService, build.Version),
		Backup:   backup.NewService(conn, blobs, auditService, cfg.DBPath, build.Version),
	}, nil
}

// anonymiserConfig builds the ingest setup, or nil when no key or detector is
// configured (ingest then reports that it is not configured).
func anonymiserConfig(cfg Config) (*ingest.Config, error) {
	profiles, err := anonymise.LoadProfiles(cfg.AnonProfilesDir)
	if err != nil {
		return nil, fmt.Errorf("loading anonymisation profiles: %w", err)
	}
	if len(cfg.AnonKey) == 0 {
		return nil, nil
	}
	key, err := anonymise.NewKey(cfg.AnonKey)
	if err != nil {
		return nil, err
	}
	detector := cfg.AnonDetector
	if detector == nil && cfg.PresidioURL != "" {
		detector = anonymise.NewPresidio(cfg.PresidioURL)
	}
	if detector == nil {
		return nil, nil
	}
	return &ingest.Config{Key: key, Detector: detector, Profiles: profiles}, nil
}

// Close closes the database.
func (a *App) Close() error {
	return a.conn.Close()
}
