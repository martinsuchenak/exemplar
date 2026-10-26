// Package db opens the SQLite database and runs its migrations.
package db

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"time"

	xdaldb "github.com/martinsuchenak/xdal/pkg/sqlite"
	"github.com/martinsuchenak/xdal/pkg/xdal"
	"github.com/paularlott/logger"
	_ "modernc.org/sqlite"
)

// maxOpenConns allows the migration lock its own connection next to the one
// running migrations; xdal's migrator refuses a single-connection pool.
const maxOpenConns = 4

// Connection exposes the database as raw *sql.DB (SQL) and through xdal (DB).
type Connection struct {
	SQL *sql.DB
	DB  xdal.DBInterface
}

// Open opens (creating if needed) the SQLite database at path and runs pending
// migrations. The file is created with owner-only permissions because it holds
// API key hashes and the audit log.
func Open(ctx context.Context, log logger.Logger, path string) (*Connection, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("creating database directory: %w", err)
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDONLY, 0o600)
	if err != nil {
		return nil, fmt.Errorf("creating database file: %w", err)
	}
	_ = f.Close()
	conn, err := connect(ctx, log, path)
	if err != nil {
		return nil, err
	}
	if err := Migrate(ctx, conn, log); err != nil {
		_ = conn.Close()
		return nil, err
	}
	if log != nil {
		log.Info("opened database", "path", path)
	}
	return conn, nil
}

// OpenCurrent opens an existing database without migrating it, and fails
// unless its schema is exactly this build's. Commands that run beside a
// live server (backup) use it, so a newer binary never changes the schema
// under an older running server.
func OpenCurrent(ctx context.Context, log logger.Logger, path string) (*Connection, error) {
	if _, err := os.Stat(path); err != nil {
		return nil, fmt.Errorf("opening database: %w", err)
	}
	conn, err := connect(ctx, log, path)
	if err != nil {
		return nil, err
	}
	applied, err := AppliedVersion(ctx, conn)
	if err != nil {
		_ = conn.Close()
		return nil, err
	}
	if applied != LatestVersion() {
		_ = conn.Close()
		return nil, fmt.Errorf("the database schema is %q but this build expects %q: use the build the server runs (or restart the server on this one first)", applied, LatestVersion())
	}
	return conn, nil
}

// connect opens the SQLite database at path with exemplar's settings.
func connect(ctx context.Context, log logger.Logger, path string) (*Connection, error) {
	// WAL with synchronous(NORMAL) is SQLite's recommended pairing; _txlock=immediate
	// takes the write lock at BEGIN so concurrent writers wait instead of failing.
	// busy_timeout must exceed the longest single transaction (a large import
	// commits hundreds of thousands of rows in one), or concurrent writers
	// fail with SQLITE_BUSY instead of waiting.
	dsn := path + "?_pragma=foreign_keys(1)&_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)&_pragma=busy_timeout(60000)&_pragma=recursive_triggers(1)&_txlock=immediate"
	sqlDB, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("opening database: %w", err)
	}
	sqlDB.SetMaxOpenConns(maxOpenConns)
	sqlDB.SetMaxIdleConns(maxOpenConns)
	sqlDB.SetConnMaxLifetime(time.Hour)

	if err := sqlDB.PingContext(ctx); err != nil {
		_ = sqlDB.Close()
		return nil, fmt.Errorf("pinging database: %w", err)
	}

	return &Connection{SQL: sqlDB, DB: wrapXDALDB(sqlDB, log)}, nil
}

// Close closes the database.
func (c *Connection) Close() error {
	return c.SQL.Close()
}

func wrapXDALDB(sqlDB *sql.DB, log logger.Logger) xdal.DBInterface {
	if log == nil {
		return xdaldb.NewSQLiteDB(sqlDB, nil)
	}
	return xdaldb.NewSQLiteDB(sqlDB, log)
}
