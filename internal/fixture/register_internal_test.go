package fixture

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/martinsuchenak/exemplar/internal/account"
	"github.com/martinsuchenak/exemplar/internal/audit"
	"github.com/martinsuchenak/exemplar/internal/blob"
	"github.com/martinsuchenak/exemplar/internal/dataset"
	"github.com/martinsuchenak/exemplar/internal/db"
)

// TestFailedRegistrationLeavesNoFile is vuln-0007: when registering stored
// content fails (SQLITE_BUSY behind a long import, here forced with a
// trigger), the file must not stay behind, since garbage collection only
// finds files through their rows.
func TestFailedRegistrationLeavesNoFile(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	conn, err := db.Open(ctx, nil, filepath.Join(dir, "x.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	blobs, err := blob.NewStore(filepath.Join(dir, "blobs"), conn.DB.NewQueryBuilder())
	if err != nil {
		t.Fatal(err)
	}
	auditService := audit.NewService(conn.DB)
	accounts := account.NewService(conn.DB, auditService)
	_, issued, err := accounts.Bootstrap(ctx, account.CreateAccountInput{Name: "root"})
	if err != nil {
		t.Fatal(err)
	}
	admin, err := accounts.Authenticate(ctx, issued.Secret)
	if err != nil {
		t.Fatal(err)
	}
	datasets := dataset.NewService(conn.DB, blobs, auditService)
	s := NewService(conn.DB, blobs, datasets, auditService, 0)
	d, err := datasets.Create(ctx, admin, dataset.CreateInput{Name: "w"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := conn.SQL.ExecContext(ctx, `CREATE TRIGGER fail_register BEFORE INSERT ON blobs BEGIN SELECT RAISE(ABORT, 'database is locked'); END`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Create(ctx, admin, CreateInput{DatasetID: d.ID, Name: "a.txt", Content: strings.NewReader("leaky")}); err == nil {
		t.Fatal("create succeeded with registration failing")
	}
	var files []string
	_ = filepath.WalkDir(filepath.Join(dir, "blobs", "objects"), func(p string, e os.DirEntry, err error) error {
		if err == nil && !e.IsDir() {
			files = append(files, p)
		}
		return nil
	})
	if len(files) != 0 {
		t.Errorf("orphaned files after a failed registration: %v", files)
	}
}
