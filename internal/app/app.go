// Package app opens the database and blob store and builds the services.
// Every entry point (serve, CLI commands, MCP tools) gets its services from
// here, so they all share the same access rules.
package app

import (
	"context"
	"fmt"

	"github.com/paularlott/logger"

	"github.com/martinsuchenak/exemplar/internal/account"
	"github.com/martinsuchenak/exemplar/internal/audit"
	"github.com/martinsuchenak/exemplar/internal/blob"
	"github.com/martinsuchenak/exemplar/internal/dataset"
	"github.com/martinsuchenak/exemplar/internal/db"
	"github.com/martinsuchenak/exemplar/internal/fixture"
)

// Config locates an instance's data.
type Config struct {
	// DBPath is the SQLite database file.
	DBPath string
	// BlobDir holds fixture contents.
	BlobDir string
}

// App holds the services every entry point uses.
type App struct {
	conn     *db.Connection
	Audit    *audit.Service
	Accounts *account.Service
	Datasets *dataset.Service
	Fixtures *fixture.Service
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
	return &App{
		conn:     conn,
		Audit:    auditService,
		Accounts: account.NewService(conn.DB, auditService),
		Datasets: datasets,
		Fixtures: fixture.NewService(conn.DB, blobs, datasets, auditService),
	}, nil
}

// Close closes the database.
func (a *App) Close() error {
	return a.conn.Close()
}
