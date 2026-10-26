package blob

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/martinsuchenak/exemplar/internal/apperr"
	"github.com/martinsuchenak/exemplar/internal/db"
)

func newStore(t *testing.T) (*Store, *db.Connection) {
	t.Helper()
	dir := t.TempDir()
	conn, err := db.Open(context.Background(), nil, filepath.Join(dir, "exemplar.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	s, err := NewStore(filepath.Join(dir, "blobs"), conn.DB.NewQueryBuilder())
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	return s, conn
}

func TestPutOpenAndDedupe(t *testing.T) {
	s, _ := newStore(t)
	h1, n, err := s.Put(strings.NewReader("hello"))
	if err != nil || n != 5 || !ValidHash(h1) {
		t.Fatalf("put = %s, %d, %v", h1, n, err)
	}
	h2, _, err := s.Put(strings.NewReader("hello"))
	if err != nil || h2 != h1 {
		t.Errorf("same content hash = %s, want %s (%v)", h2, h1, err)
	}
	f, err := s.Open(h1)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	b, _ := io.ReadAll(f)
	_ = f.Close()
	if string(b) != "hello" {
		t.Errorf("content = %q", b)
	}
	info, _ := os.Stat(s.path(h1))
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("blob mode = %o, want 600", perm)
	}
	if entries, _ := os.ReadDir(filepath.Join(s.root, "tmp")); len(entries) != 0 {
		t.Errorf("temp files left behind: %d", len(entries))
	}
}

func TestOpenRejectsBadHashes(t *testing.T) {
	s, _ := newStore(t)
	if _, err := s.Open("../../etc/passwd"); !errors.Is(err, apperr.ErrInvalid) {
		t.Errorf("traversal err = %v, want ErrInvalid", err)
	}
	if _, err := s.Open(strings.Repeat("a", 64)); !errors.Is(err, apperr.ErrNotFound) {
		t.Errorf("missing err = %v, want ErrNotFound", err)
	}
}

func TestCollectRemovesUnreferenced(t *testing.T) {
	s, conn := newStore(t)
	ctx := context.Background()
	hash, size, err := s.Put(strings.NewReader("orphan"))
	if err != nil {
		t.Fatalf("put: %v", err)
	}
	if err := s.Register(ctx, conn.DB, hash, size); err != nil {
		t.Fatalf("register: %v", err)
	}
	if err := s.Register(ctx, conn.DB, hash, size); err != nil {
		t.Fatalf("register twice: %v", err)
	}
	n, err := s.Collect(ctx, conn.DB)
	if err != nil || n != 1 {
		t.Fatalf("collect = %d, %v; want 1", n, err)
	}
	if _, err := os.Stat(s.path(hash)); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("blob file still exists after collect: %v", err)
	}
}

type failingReader struct{}

func (failingReader) Read([]byte) (int, error) { return 0, errors.New("read failed") }

func TestPutCleansUpOnReadError(t *testing.T) {
	s, _ := newStore(t)
	if _, _, err := s.Put(failingReader{}); err == nil {
		t.Fatal("Put with a failing reader succeeded")
	}
	if entries, _ := os.ReadDir(filepath.Join(s.root, "tmp")); len(entries) != 0 {
		t.Errorf("temp file left behind after a failed Put: %d", len(entries))
	}
}

func TestNewStoreFailsOnFile(t *testing.T) {
	dir := t.TempDir()
	blocker := filepath.Join(dir, "blobs")
	if err := os.WriteFile(blocker, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := NewStore(blocker, nil); err == nil {
		t.Error("NewStore on a file succeeded")
	}
}

func TestCollectKeepsReferencedBlobs(t *testing.T) {
	s, conn := newStore(t)
	ctx := context.Background()
	hash, size, err := s.Put(strings.NewReader("kept"))
	if err != nil {
		t.Fatalf("put: %v", err)
	}
	if err := s.Register(ctx, conn.DB, hash, size); err != nil {
		t.Fatalf("register: %v", err)
	}
	// A fixture row referencing the blob keeps it alive.
	stmts := []string{
		`INSERT INTO accounts (id, name, kind, role, created_at) VALUES ('a', 'a', 'developer', 'admin', 't')`,
		`INSERT INTO datasets (id, name, scope, owner_account_id, created_at, updated_at) VALUES ('d', 'd', 'workspace', 'a', 't', 't')`,
		`INSERT INTO fixtures (id, dataset_id, name, kind, format, mime_type, blob_hash, size, source, review_state, created_by, created_at)
		 VALUES ('f', 'd', 'x.txt', 'file', 'txt', 'text/plain', '` + hash + `', 4, 'uploaded', 'not-required', 'a', 't')`,
	}
	for _, q := range stmts {
		if _, err := conn.SQL.Exec(q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	if n, err := s.Collect(ctx, conn.DB); err != nil || n != 0 {
		t.Errorf("collect = %d, %v; want 0", n, err)
	}
	if _, err := os.Stat(s.path(hash)); err != nil {
		t.Errorf("referenced blob removed: %v", err)
	}
}
