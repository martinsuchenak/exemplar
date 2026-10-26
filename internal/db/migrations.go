package db

import (
	"context"
	"embed"
	"fmt"

	"github.com/martinsuchenak/xdal/pkg/migrate"
	"github.com/paularlott/logger"
)

// MigrationNamespace is the history namespace for exemplar's own schema.
const MigrationNamespace = "core"

//go:embed migrations/*.sql
var migrationFS embed.FS

// migrations lists every schema change in order. Each step runs one embedded
// SQL file inside the migrator's transaction.
func migrations() []migrate.Migration {
	return []migrate.Migration{
		{
			Version: "20261026000001",
			Name:    "core",
			Up:      execFile("migrations/20261026000001_core.sql"),
			Down: func(ctx context.Context, tx *migrate.Tx) error {
				return tx.ExecSQL(ctx,
					`DROP TABLE audit_events`,
					`DROP TABLE fixtures`,
					`DROP TABLE blobs`,
					`DROP TABLE datasets`,
					`DROP TABLE api_keys`,
					`DROP TABLE accounts`,
				)
			},
		},
	}
}

func execFile(name string) func(context.Context, *migrate.Tx) error {
	return func(ctx context.Context, tx *migrate.Tx) error {
		script, err := migrationFS.ReadFile(name)
		if err != nil {
			return fmt.Errorf("reading %s: %w", name, err)
		}
		// The SQLite driver runs every statement in a multi-statement script,
		// which keeps trigger bodies (with their own semicolons) intact.
		return tx.ExecSQL(ctx, string(script))
	}
}

// Migrate applies pending migrations.
func Migrate(ctx context.Context, conn *Connection, log logger.Logger) error {
	opts := migrate.Options{Namespace: MigrationNamespace}
	if log != nil {
		opts.Logger = log
	}
	m, err := migrate.New(conn.DB, migrations(), opts)
	if err != nil {
		return fmt.Errorf("preparing migrations: %w", err)
	}
	if _, err := m.Up(ctx); err != nil {
		return fmt.Errorf("running migrations: %w", err)
	}
	return nil
}
