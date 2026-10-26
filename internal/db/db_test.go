package db

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/martinsuchenak/xdal/pkg/migrate"
	"github.com/paularlott/logger"
)

func openTemp(t *testing.T) (*Connection, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "data", "exemplar.db")
	conn, err := Open(context.Background(), nil, path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return conn, path
}

func TestOpenCreatesPrivateFileAndMigrates(t *testing.T) {
	conn, path := openTemp(t)

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("database file mode = %o, want 600", perm)
	}

	for _, table := range []string{"accounts", "api_keys", "datasets", "blobs", "fixtures", "audit_events", "persons", "fixture_persons", "identity_keys", "name_tokens"} {
		var n int
		err := conn.SQL.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = ?`, table).Scan(&n)
		if err != nil || n != 1 {
			t.Errorf("table %s missing (n=%d, err=%v)", table, n, err)
		}
	}
}

func TestOpenTwiceIsIdempotent(t *testing.T) {
	_, path := openTemp(t)
	conn, err := Open(context.Background(), nil, path)
	if err != nil {
		t.Fatalf("second Open: %v", err)
	}
	_ = conn.Close()
}

func TestAuditEventsAreAppendOnly(t *testing.T) {
	conn, _ := openTemp(t)
	if _, err := conn.SQL.Exec(`INSERT INTO audit_events (id, at, action) VALUES ('e1', '2026-10-26T00:00:00Z', 'test')`); err != nil {
		t.Fatalf("insert: %v", err)
	}
	if _, err := conn.SQL.Exec(`UPDATE audit_events SET action = 'changed'`); err == nil {
		t.Error("update of audit_events succeeded, want error")
	}
	if _, err := conn.SQL.Exec(`DELETE FROM audit_events`); err == nil {
		t.Error("delete from audit_events succeeded, want error")
	}
}

func TestForeignKeysEnforced(t *testing.T) {
	conn, _ := openTemp(t)
	_, err := conn.SQL.Exec(`INSERT INTO api_keys (id, account_id, name, prefix, hash, scopes, created_at)
		VALUES ('k1', 'missing', 'n', 'p', 'h', 'read', '2026-10-26T00:00:00Z')`)
	if err == nil {
		t.Error("insert with unknown account succeeded, want foreign key error")
	}
}

func TestOpenFailsWhenParentIsAFile(t *testing.T) {
	dir := t.TempDir()
	blocker := filepath.Join(dir, "file")
	if err := os.WriteFile(blocker, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(context.Background(), nil, filepath.Join(blocker, "x.db")); err == nil {
		t.Error("Open under a file succeeded")
	}
}

func TestOpenWithLogger(t *testing.T) {
	conn, err := Open(context.Background(), logger.NewNullLogger(), filepath.Join(t.TempDir(), "x.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	_ = conn.Close()
}

func TestMigrationsRollBack(t *testing.T) {
	conn, _ := openTemp(t)
	ctx := context.Background()
	m, err := migrate.New(conn.DB, migrations(), migrate.Options{Namespace: MigrationNamespace})
	if err != nil {
		t.Fatalf("migrator: %v", err)
	}
	if _, err := m.Down(ctx, 4); err != nil {
		t.Fatalf("down: %v", err)
	}
	var n int
	if err := conn.SQL.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = 'accounts'`).Scan(&n); err != nil || n != 0 {
		t.Errorf("accounts table still present after rollback (n=%d, err=%v)", n, err)
	}
	if err := Migrate(ctx, conn, nil); err != nil {
		t.Fatalf("migrate up again: %v", err)
	}
}

func TestSchemaVersions(t *testing.T) {
	conn, _ := openTemp(t)
	v, err := AppliedVersion(context.Background(), conn)
	if err != nil || v != LatestVersion() || v == "" {
		t.Errorf("applied %q, latest %q, %v", v, LatestVersion(), err)
	}
}
