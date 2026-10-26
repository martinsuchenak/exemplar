// Package blob stores file contents once, addressed by SHA-256, on the
// filesystem, and tracks them in the blobs table. It is a storage layer: only
// services call it, and it does no access checks of its own.
package blob

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sync"
	"time"

	"github.com/martinsuchenak/xdal/pkg/xdal"

	"github.com/martinsuchenak/exemplar/internal/apperr"
	"github.com/martinsuchenak/exemplar/internal/storeutil"
)

var hashRe = regexp.MustCompile(`^[0-9a-f]{64}$`)

// Store keeps blobs under root/objects/ab/cd/<hash>, written through root/tmp.
//
// Adding a fixture writes the file before its row commits, so a garbage
// collection running in between could delete the file. Writers hold Acquire
// (shared) from Put until their transaction commits; Collect takes it exclusively.
type Store struct {
	root string
	qb   *xdal.QueryBuilder
	mu   sync.RWMutex
	now  func() time.Time
}

// NewStore creates root's objects and tmp directories (mode 700) if needed.
func NewStore(root string, qb *xdal.QueryBuilder) (*Store, error) {
	for _, dir := range []string{"objects", "tmp"} {
		if err := os.MkdirAll(filepath.Join(root, dir), 0o700); err != nil {
			return nil, fmt.Errorf("creating blob directory: %w", err)
		}
	}
	return &Store{root: root, qb: qb, now: time.Now}, nil
}

// Acquire blocks garbage collection until the returned release is called.
func (s *Store) Acquire() (release func()) {
	s.mu.RLock()
	return s.mu.RUnlock
}

// Put streams r to disk and returns its hash and size. Identical content is
// stored once.
func (s *Store) Put(r io.Reader) (hash string, size int64, err error) {
	tmp, err := os.CreateTemp(filepath.Join(s.root, "tmp"), "put-*")
	if err != nil {
		return "", 0, fmt.Errorf("creating temp file: %w", err)
	}
	defer func() {
		if err != nil {
			_ = tmp.Close()
			_ = os.Remove(tmp.Name())
		}
	}()

	h := sha256.New()
	size, err = io.Copy(io.MultiWriter(tmp, h), r)
	if err != nil {
		return "", 0, fmt.Errorf("writing blob: %w", err)
	}
	if err = tmp.Sync(); err != nil {
		return "", 0, fmt.Errorf("syncing blob: %w", err)
	}
	if err = tmp.Close(); err != nil {
		return "", 0, fmt.Errorf("closing blob: %w", err)
	}
	hash = hex.EncodeToString(h.Sum(nil))
	dst := s.path(hash)
	if err = os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
		return "", 0, fmt.Errorf("creating blob directory: %w", err)
	}
	// Renaming over an existing copy is atomic and leaves the same bytes.
	if err = os.Rename(tmp.Name(), dst); err != nil {
		return "", 0, fmt.Errorf("storing blob: %w", err)
	}
	return hash, size, nil
}

// Open opens a blob for reading. Malformed hashes return apperr.ErrInvalid,
// so a caller can never reach a path outside the store.
func (s *Store) Open(hash string) (*os.File, error) {
	if !ValidHash(hash) {
		return nil, fmt.Errorf("%w: invalid blob hash", apperr.ErrInvalid)
	}
	f, err := os.Open(s.path(hash))
	if errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("blob %s: %w", hash, apperr.ErrNotFound)
	}
	return f, err
}

// Register records a stored blob; registering the same hash again is a no-op.
func (s *Store) Register(ctx context.Context, exec xdal.DBExecutor, hash string, size int64) error {
	query, args, err := s.qb.Insert("blobs").
		Set("hash", hash).
		Set("size", size).
		Set("created_at", storeutil.FormatTime(s.now())).
		OnConflict("hash").DoNothing().
		Build()
	if err != nil {
		return err
	}
	_, err = exec.Exec(ctx, query, args...)
	return err
}

// backupLock is the file that tells garbage collection in every process
// (the server, a separate backup command) that a backup is copying blobs.
// One older than backupLockMaxAge is left over from a crash and ignored.
const (
	backupLock       = "backup.lock"
	backupLockMaxAge = 24 * time.Hour
)

// HoldForBackup stops garbage collection, in this process and others using
// the same directory, until release is called. Only one backup may hold it.
func (s *Store) HoldForBackup() (release func(), err error) {
	path := filepath.Join(s.root, backupLock)
	if info, err := os.Stat(path); err == nil && s.now().Sub(info.ModTime()) > backupLockMaxAge {
		_ = os.Remove(path)
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if errors.Is(err, os.ErrExist) {
		return nil, fmt.Errorf("%w: another backup is running (%s)", apperr.ErrConflict, path)
	}
	if err != nil {
		return nil, fmt.Errorf("creating backup lock: %w", err)
	}
	_, _ = fmt.Fprintf(f, "pid %d at %s\n", os.Getpid(), s.now().UTC().Format(time.RFC3339))
	if err := f.Close(); err != nil {
		return nil, err
	}
	return func() { _ = os.Remove(path) }, nil
}

var errBackupStarted = errors.New("backup started")

func (s *Store) backupRunning() bool {
	info, err := os.Stat(filepath.Join(s.root, backupLock))
	return err == nil && s.now().Sub(info.ModTime()) <= backupLockMaxAge
}

// Collect removes blobs no fixture refers to, rows first, then files, and
// returns how many it removed. While a backup runs it removes nothing; the
// next collection catches up.
func (s *Store) Collect(ctx context.Context, db xdal.DBInterface) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.backupRunning() {
		return 0, nil
	}

	var hashes []string
	err := db.WithTx(ctx, nil, func(tx *xdal.Tx) error {
		query, args, err := s.qb.Select("b.hash").From("blobs b").
			Where("NOT EXISTS (SELECT 1 FROM fixtures f WHERE f.blob_hash = b.hash)").
			Build()
		if err != nil {
			return err
		}
		rows, err := tx.Query(ctx, query, args...)
		if err != nil {
			return err
		}
		for rows.Next() {
			var h string
			if err := rows.Scan(&h); err != nil {
				_ = rows.Close()
				return err
			}
			hashes = append(hashes, h)
		}
		if err := rows.Close(); err != nil {
			return err
		}
		if s.backupRunning() {
			// A backup started after the check above; roll back and let
			// the next collection catch up.
			return errBackupStarted
		}
		for _, h := range hashes {
			query, args, err := s.qb.Delete("blobs").Where("hash = ?", h).Build()
			if err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, query, args...); err != nil {
				return err
			}
		}
		return nil
	})
	if errors.Is(err, errBackupStarted) {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("collecting blobs: %w", err)
	}
	for _, h := range hashes {
		if err := os.Remove(s.path(h)); err != nil && !errors.Is(err, os.ErrNotExist) {
			return 0, fmt.Errorf("removing blob %s: %w", h, err)
		}
	}
	return len(hashes), nil
}

func (s *Store) path(hash string) string {
	return filepath.Join(s.root, "objects", hash[:2], hash[2:4], hash)
}

// ValidHash reports whether hash is a lower-case SHA-256 hex string.
func ValidHash(hash string) bool {
	return hashRe.MatchString(hash)
}
