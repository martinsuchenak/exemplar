package backup

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"testing"

	"github.com/paularlott/logger"

	"github.com/martinsuchenak/exemplar/internal/db"
)

// The snapshot is private from the moment it exists: it is created mode 600
// and VACUUM INTO fills that file rather than making its own.
func TestSnapshotIsPrivate(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "exemplar.db")
	conn, err := db.Open(ctx, logger.NewNullLogger(), path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	s := &Service{conn: conn, dbPath: path}

	snap, err := s.snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Remove(snap) })
	info, err := os.Stat(snap)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 || info.Size() == 0 {
		t.Errorf("snapshot mode %v, size %d; want 600 and a database", info.Mode().Perm(), info.Size())
	}
	ro, err := sql.Open("sqlite", "file:"+snap+"?mode=ro")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ro.Close() }()
	var n int
	if err := ro.QueryRowContext(ctx, "SELECT COUNT(*) FROM accounts").Scan(&n); err != nil {
		t.Errorf("reading the snapshot: %v", err)
	}
}
