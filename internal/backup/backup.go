// Package backup copies a whole instance into an archive (see archive) and
// restores one. It is for the operator on the host, like bootstrap: it takes
// no principal, and REST, CLI remote commands and MCP never reach it.
//
// A backup holds the SQLite database as a consistent snapshot (VACUUM INTO,
// safe while the server runs) and every content blob the snapshot's fixtures
// use. The anonymisation key is a separate secret and is never included.
package backup

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/martinsuchenak/exemplar/internal/archive"
	"github.com/martinsuchenak/exemplar/internal/audit"
	"github.com/martinsuchenak/exemplar/internal/blob"
	"github.com/martinsuchenak/exemplar/internal/db"
)

// Service backs up the instance it was built for.
type Service struct {
	conn       *db.Connection
	blobs      *blob.Store
	audit      *audit.Service
	dbPath     string
	appVersion string
	now        func() time.Time
}

// NewService returns a backup service for the database at dbPath (open as
// conn) and the blob store.
func NewService(conn *db.Connection, blobs *blob.Store, auditService *audit.Service, dbPath, appVersion string) *Service {
	return &Service{conn: conn, blobs: blobs, audit: auditService, dbPath: dbPath, appVersion: appVersion, now: time.Now}
}

// Summary describes a backup or restore.
type Summary struct {
	// CreatedAt is when the backup was taken.
	CreatedAt     time.Time `json:"created_at"`
	SchemaVersion string    `json:"schema_version"`
	Fixtures      int       `json:"fixtures"`
	Blobs         int       `json:"blobs"`
	Bytes         int64     `json:"bytes"`
	// MovedAside lists what a restore with overwrite moved out of the way.
	MovedAside []string `json:"moved_aside,omitempty"`
}

// Backup writes a backup archive to w and records instance.backup in the
// audit log. Blob garbage collection, in this process and in a running
// server, waits until it is done.
func (s *Service) Backup(ctx context.Context, w io.Writer) (*Summary, error) {
	release, err := s.blobs.HoldForBackup()
	if err != nil {
		return nil, err
	}
	defer release()

	version, err := db.AppliedVersion(ctx, s.conn)
	if err != nil {
		return nil, err
	}
	snapshot, err := s.snapshot(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = os.Remove(snapshot) }()

	hashes, fixtures, err := snapshotBlobs(ctx, snapshot)
	if err != nil {
		return nil, err
	}
	sum := &Summary{CreatedAt: s.now().UTC(), SchemaVersion: version, Fixtures: fixtures, Blobs: len(hashes)}
	aw, err := archive.NewWriter(w, archive.Manifest{
		Kind: archive.KindBackup, CreatedAt: sum.CreatedAt, AppVersion: s.appVersion, SchemaVersion: version,
		Counts: map[string]int{"fixtures": fixtures, "blobs": len(hashes)},
	})
	if err != nil {
		return nil, err
	}
	if err := writeFile(snapshot, aw.Database); err != nil {
		return nil, err
	}
	for _, h := range hashes {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		f, err := s.blobs.Open(h)
		if err != nil {
			return nil, fmt.Errorf("content %s is missing from the blob store: %w", h, err)
		}
		info, err := f.Stat()
		if err == nil {
			err = aw.Blob(h, info.Size(), f)
			sum.Bytes += info.Size()
		}
		_ = f.Close()
		if err != nil {
			return nil, err
		}
	}
	if err := aw.Close(); err != nil {
		return nil, err
	}
	err = s.audit.Record(ctx, s.conn.DB, audit.Event{
		Action: audit.ActionBackup, TargetType: "instance",
		Details: map[string]any{"schema_version": version, "fixtures": fixtures, "blobs": len(hashes), "bytes": sum.Bytes},
	})
	if err != nil {
		return nil, err
	}
	return sum, nil
}

// snapshot writes a consistent copy of the database next to it (same disk,
// same permissions) and returns its path. The file is created empty with
// mode 600 first and VACUUM INTO fills it (it accepts an empty file), so the
// copy is never readable by anyone else, not even while it is written.
func (s *Service) snapshot(ctx context.Context) (string, error) {
	f, err := os.CreateTemp(filepath.Dir(s.dbPath), ".backup-*.db")
	if err != nil {
		return "", fmt.Errorf("creating snapshot file: %w", err)
	}
	path := f.Name()
	_ = f.Close()
	if _, err := s.conn.SQL.ExecContext(ctx, "VACUUM INTO ?", path); err != nil {
		_ = os.Remove(path)
		return "", fmt.Errorf("snapshotting the database: %w", err)
	}
	return path, nil
}

// snapshotBlobs lists the blobs the snapshot's fixtures use, and counts the
// fixtures.
func snapshotBlobs(ctx context.Context, path string) ([]string, int, error) {
	conn, err := sql.Open("sqlite", "file:"+path+"?mode=ro")
	if err != nil {
		return nil, 0, err
	}
	defer func() { _ = conn.Close() }()
	var fixtures int
	if err := conn.QueryRowContext(ctx, "SELECT COUNT(*) FROM fixtures").Scan(&fixtures); err != nil {
		return nil, 0, fmt.Errorf("reading the snapshot: %w", err)
	}
	hashes, err := distinctHashes(ctx, conn)
	return hashes, fixtures, err
}

func distinctHashes(ctx context.Context, conn *sql.DB) ([]string, error) {
	rows, err := conn.QueryContext(ctx, "SELECT DISTINCT blob_hash FROM fixtures ORDER BY blob_hash")
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []string
	for rows.Next() {
		var h string
		if err := rows.Scan(&h); err != nil {
			return nil, err
		}
		out = append(out, h)
	}
	return out, rows.Err()
}

func writeFile(path string, write func(int64, io.Reader) error) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	info, err := f.Stat()
	if err != nil {
		return err
	}
	return write(info.Size(), f)
}

// Target is where a restore puts the instance.
type Target struct {
	DBPath  string
	BlobDir string
}

// ErrNotEmpty means the target already holds an instance and overwrite was
// not asked for.
var ErrNotEmpty = errors.New("the target already holds an instance")

// Restore reads a backup archive from r into t. The server must be stopped.
// Everything is unpacked and checked beside the target first (hashes,
// schema version, database integrity, every fixture's content present); only
// then is it moved into place. An existing instance is refused unless
// overwrite is set, and then moved aside (to *.pre-restore-<time>), never
// deleted. Older backups are migrated to this build's schema; newer ones are
// refused.
func Restore(ctx context.Context, r io.Reader, t Target, overwrite bool) (*Summary, error) {
	if t.DBPath == "" || t.BlobDir == "" {
		return nil, errors.New("database path and blob directory are required")
	}
	exists, err := holdsInstance(t)
	if err != nil {
		return nil, err
	}
	if exists && !overwrite {
		return nil, fmt.Errorf("%w (%s, %s); stop the server and pass --overwrite to replace it", ErrNotEmpty, t.DBPath, t.BlobDir)
	}

	ar, err := archive.NewReader(r)
	if err != nil {
		return nil, err
	}
	m := ar.Manifest()
	if m.Kind != archive.KindBackup {
		return nil, fmt.Errorf("this is an %s archive; load it with exemplar import", m.Kind)
	}
	if m.SchemaVersion > db.LatestVersion() {
		return nil, fmt.Errorf("the backup's schema %s is newer than this build's %s; restore it with a newer exemplar", m.SchemaVersion, db.LatestVersion())
	}

	stamp := time.Now().UTC().Format("20060102-150405")
	if err := os.MkdirAll(filepath.Dir(t.DBPath), 0o700); err != nil {
		return nil, err
	}
	dbStage, err := os.MkdirTemp(filepath.Dir(t.DBPath), ".restore-")
	if err != nil {
		return nil, err
	}
	blobStage := strings.TrimRight(t.BlobDir, string(filepath.Separator)) + ".restore-" + stamp
	swapped := false
	defer func() {
		_ = os.RemoveAll(dbStage)
		if !swapped {
			_ = os.RemoveAll(blobStage)
		}
	}()

	sum := &Summary{CreatedAt: m.CreatedAt, SchemaVersion: m.SchemaVersion}
	stagedDB := filepath.Join(dbStage, "exemplar.db")
	if err := unpack(ctx, ar, stagedDB, blobStage, sum); err != nil {
		return nil, err
	}
	if sum.Blobs != m.Counts["blobs"] {
		return nil, fmt.Errorf("the backup holds %d contents, its manifest says %d (truncated?)", sum.Blobs, m.Counts["blobs"])
	}
	if err := check(ctx, stagedDB, blobStage, sum); err != nil {
		return nil, err
	}

	var moved [][2]string
	// undo puts back what was moved, so a failed swap leaves the old
	// instance where it was.
	undo := func() {
		_ = os.Remove(t.DBPath)
		_ = os.RemoveAll(t.BlobDir)
		for i := len(moved) - 1; i >= 0; i-- {
			_ = os.Rename(moved[i][1], moved[i][0])
		}
	}
	if !exists {
		if err := removeEmptyBlobDir(t.BlobDir); err != nil {
			return nil, err
		}
		// Empty leftovers of a server that never wrote anything; SQLite
		// would read them as the restored database's log.
		for _, p := range []string{t.DBPath + "-wal", t.DBPath + "-shm"} {
			if err := os.Remove(p); err != nil && !errors.Is(err, os.ErrNotExist) {
				return nil, err
			}
		}
	}
	if exists {
		for _, p := range []string{t.DBPath, t.DBPath + "-wal", t.DBPath + "-shm", t.BlobDir} {
			if _, err := os.Stat(p); err != nil {
				continue
			}
			aside := p + ".pre-restore-" + stamp
			if err := os.Rename(p, aside); err != nil {
				undo()
				return nil, fmt.Errorf("moving %s aside: %w", p, err)
			}
			moved = append(moved, [2]string{p, aside})
			sum.MovedAside = append(sum.MovedAside, aside)
		}
	}
	if err := os.Rename(stagedDB, t.DBPath); err != nil {
		undo()
		return nil, fmt.Errorf("moving the restored database into place: %w", err)
	}
	if err := os.Rename(blobStage, t.BlobDir); err != nil {
		undo()
		return nil, fmt.Errorf("moving the restored contents into place: %w", err)
	}
	swapped = true
	return sum, nil
}

// removeEmptyBlobDir removes a blob directory that holds no content (only
// the empty objects and tmp directories a store creates), so the restored
// one can take its place. Anything else in it is an error, never deleted.
func removeEmptyBlobDir(dir string) error {
	for _, sub := range []string{"objects", "tmp"} {
		if err := os.Remove(filepath.Join(dir, sub)); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("blob directory %s is not empty: %w", dir, err)
		}
	}
	if err := os.Remove(dir); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("blob directory %s is not empty: %w", dir, err)
	}
	return nil
}

// holdsInstance reports whether t already has a database or stored content.
// A non-empty write-ahead log counts: a server stopped before a checkpoint
// keeps its data there while the main file is still empty.
func holdsInstance(t Target) (bool, error) {
	for _, p := range []string{t.DBPath, t.DBPath + "-wal"} {
		if info, err := os.Stat(p); err == nil && info.Size() > 0 {
			return true, nil
		} else if err != nil && !errors.Is(err, os.ErrNotExist) {
			return false, err
		}
	}
	entries, err := os.ReadDir(filepath.Join(t.BlobDir, "objects"))
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	return len(entries) > 0, err
}

// unpack writes the database and blobs from the archive into the staging
// locations. The archive verifies each blob against its hash as it is read.
func unpack(ctx context.Context, ar *archive.Reader, dbPath, blobDir string, sum *Summary) error {
	store, err := blob.NewStore(blobDir, nil)
	if err != nil {
		return err
	}
	haveDB := false
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		e, err := ar.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return err
		}
		switch {
		case e.Name == archive.DatabaseName && !haveDB:
			haveDB = true
			if err := copyTo(dbPath, e); err != nil {
				return err
			}
		case e.IsBlob():
			hash, size, err := store.Put(e)
			if err != nil {
				return err
			}
			if hash != e.BlobHash() {
				return fmt.Errorf("content %s does not match its hash", e.BlobHash())
			}
			sum.Blobs++
			sum.Bytes += size
		default:
			return fmt.Errorf("a backup does not hold %s", e.Name)
		}
	}
	if !haveDB {
		return errors.New("the backup holds no database")
	}
	return nil
}

func copyTo(path string, r io.Reader) error {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	if _, err := io.Copy(f, r); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return err
	}
	return f.Close()
}

// check opens the staged database (migrating an older one), checks its
// integrity, and checks every fixture's content was restored.
func check(ctx context.Context, dbPath, blobDir string, sum *Summary) error {
	conn, err := db.Open(ctx, nil, dbPath)
	if err != nil {
		return fmt.Errorf("opening the restored database: %w", err)
	}
	defer func() { _ = conn.Close() }()
	var result string
	if err := conn.SQL.QueryRowContext(ctx, "PRAGMA integrity_check").Scan(&result); err != nil || result != "ok" {
		return fmt.Errorf("the restored database failed its integrity check: %s %v", result, err)
	}
	if err := conn.SQL.QueryRowContext(ctx, "SELECT COUNT(*) FROM fixtures").Scan(&sum.Fixtures); err != nil {
		return err
	}
	hashes, err := distinctHashes(ctx, conn.SQL)
	if err != nil {
		return err
	}
	// Every hash the database holds must be well formed: garbage collection
	// derives file paths from the blobs table, and a tampered backup could
	// otherwise plant a row it cannot handle.
	for _, q := range []string{"SELECT hash FROM blobs", "SELECT DISTINCT blob_hash FROM fixtures"} {
		bad, err := firstInvalidHash(ctx, conn.SQL, q)
		if err != nil {
			return err
		}
		if bad != "" {
			return fmt.Errorf("the backup's database holds a malformed content hash %q; it was not made by exemplar backup", bad)
		}
	}
	store, err := blob.NewStore(blobDir, nil)
	if err != nil {
		return err
	}
	for _, h := range hashes {
		f, err := store.Open(h)
		if err != nil {
			return fmt.Errorf("content %s used by a fixture is not in the backup", h)
		}
		_ = f.Close()
	}
	// Fold the WAL into the main file, so the database is one file to move.
	if _, err := conn.SQL.ExecContext(ctx, "PRAGMA wal_checkpoint(TRUNCATE)"); err != nil {
		return err
	}
	return nil
}

// firstInvalidHash returns the first value of query's single column that is
// not a valid blob hash, or "".
func firstInvalidHash(ctx context.Context, conn *sql.DB, query string) (string, error) {
	rows, err := conn.QueryContext(ctx, query)
	if err != nil {
		return "", err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var h string
		if err := rows.Scan(&h); err != nil {
			return "", err
		}
		if !blob.ValidHash(h) {
			return h, nil
		}
	}
	return "", rows.Err()
}

// RecordRestore records instance.restore in the audit log of the instance
// that was just restored. Call it on the app opened after Restore.
func (s *Service) RecordRestore(ctx context.Context, sum *Summary) error {
	return s.audit.Record(ctx, s.conn.DB, audit.Event{
		Action: audit.ActionRestore, TargetType: "instance",
		Details: map[string]any{
			"backup_created_at": sum.CreatedAt, "schema_version": sum.SchemaVersion, "fixtures": sum.Fixtures,
			"blobs": sum.Blobs, "moved_aside": sum.MovedAside,
		},
	})
}
