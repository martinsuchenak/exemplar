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
		{
			Version: "20261027000001",
			Name:    "people",
			Up:      execFile("migrations/20261027000001_people.sql"),
			Down: func(ctx context.Context, tx *migrate.Tx) error {
				return tx.ExecSQL(ctx,
					`DROP TABLE fixture_persons`,
					`ALTER TABLE fixtures DROP COLUMN record_schema`,
					`DROP TABLE persons`,
				)
			},
		},
		{
			Version: "20261028000001",
			Name:    "identities",
			Up:      execFile("migrations/20261028000001_identities.sql"),
			Down: func(ctx context.Context, tx *migrate.Tx) error {
				return tx.ExecSQL(ctx, `DROP TABLE name_tokens`, `DROP TABLE identity_keys`)
			},
		},
		{
			Version: "20261030000001",
			Name:    "imported_reviewer",
			Up:      execFile("migrations/20261030000001_imported_reviewer.sql"),
			Down: func(ctx context.Context, tx *migrate.Tx) error {
				return tx.ExecSQL(ctx, `ALTER TABLE fixtures DROP COLUMN imported_reviewer`)
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

// LatestVersion is the newest schema version this binary knows.
func LatestVersion() string {
	all := migrations()
	return all[len(all)-1].Version
}

// AppliedVersion is the newest schema version applied to conn's database, or
// "" for an empty one.
func AppliedVersion(ctx context.Context, conn *Connection) (string, error) {
	m, err := migrate.New(conn.DB, migrations(), migrate.Options{Namespace: MigrationNamespace})
	if err != nil {
		return "", err
	}
	status, err := m.Status(ctx)
	if err != nil {
		return "", err
	}
	v := ""
	for _, s := range status {
		if s.Applied && s.Version > v {
			v = s.Version
		}
	}
	return v, nil
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
