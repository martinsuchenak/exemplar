package backup_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/martinsuchenak/exemplar/internal/account"
	"github.com/martinsuchenak/exemplar/internal/app"
	"github.com/martinsuchenak/exemplar/internal/archive"
	"github.com/martinsuchenak/exemplar/internal/audit"
	"github.com/martinsuchenak/exemplar/internal/backup"
	"github.com/martinsuchenak/exemplar/internal/dataset"
	"github.com/martinsuchenak/exemplar/internal/fixture"
	"github.com/martinsuchenak/exemplar/internal/generate"
	"github.com/martinsuchenak/exemplar/internal/testutil"
)

var ctx = context.Background()

// instance is an app with an admin key, a dataset of generated emails and
// one deleted dataset (so the store has content no fixture uses).
func instance(t *testing.T) (*app.App, string, app.Config) {
	t.Helper()
	var cfg app.Config
	a := testutil.NewAppWith(t, func(c *app.Config) { cfg = *c })
	_, issued, err := a.Accounts.Bootstrap(ctx, account.CreateAccountInput{Name: "root"})
	if err != nil {
		t.Fatal(err)
	}
	admin := testutil.Authenticate(t, a, issued.Secret)
	d, err := a.Datasets.Create(ctx, admin, dataset.CreateInput{Name: "kept"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.Generate.Generate(ctx, admin, d.ID, generate.Request{Kind: generate.KindEmails, Count: 4, Seed: 3}); err != nil {
		t.Fatal(err)
	}
	return a, issued.Secret, cfg
}

func target(t *testing.T) backup.Target {
	dir := t.TempDir()
	return backup.Target{DBPath: filepath.Join(dir, "data", "exemplar.db"), BlobDir: filepath.Join(dir, "data", "blobs")}
}

func TestBackupAndRestore(t *testing.T) {
	a, secret, _ := instance(t)
	var buf bytes.Buffer
	sum, err := a.Backup.Backup(ctx, &buf)
	if err != nil {
		t.Fatal(err)
	}
	if sum.Fixtures != 4 || sum.Blobs != 4 || sum.Bytes == 0 || sum.SchemaVersion == "" {
		t.Errorf("backup = %+v", sum)
	}
	admin := testutil.Authenticate(t, a, secret)
	if ev, _ := a.Audit.List(ctx, admin, audit.Filter{Action: audit.ActionBackup}); len(ev) != 1 {
		t.Errorf("backup events = %d", len(ev))
	}

	tgt := target(t)
	restored, err := backup.Restore(ctx, bytes.NewReader(buf.Bytes()), tgt, false)
	if err != nil {
		t.Fatal(err)
	}
	if restored.Fixtures != 4 || restored.Blobs != 4 || len(restored.MovedAside) != 0 {
		t.Errorf("restore = %+v", restored)
	}
	b, err := app.Open(ctx, nil, app.Config{DBPath: tgt.DBPath, BlobDir: tgt.BlobDir})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = b.Close() }()
	if err := b.Backup.RecordRestore(ctx, restored); err != nil {
		t.Fatal(err)
	}
	// The same key works: accounts, key hashes and the audit log came along.
	badmin := testutil.Authenticate(t, b, secret)
	if ev, _ := b.Audit.List(ctx, badmin, audit.Filter{}); len(ev) < 3 {
		t.Errorf("restored audit log has %d events", len(ev))
	}
	if ev, _ := b.Audit.List(ctx, badmin, audit.Filter{Action: audit.ActionRestore}); len(ev) != 1 {
		t.Errorf("restore events = %d", len(ev))
	}
	ds, _ := b.Datasets.List(ctx, badmin, dataset.ListFilter{})
	if len(ds) != 1 {
		t.Fatalf("datasets = %d", len(ds))
	}
	fx, _ := b.Fixtures.List(ctx, badmin, ds[0].ID, fixture.ListFilter{})
	for _, f := range fx {
		rc, _, err := b.Fixtures.Open(ctx, badmin, f.ID)
		if err != nil {
			t.Fatal(err)
		}
		got, _ := io.ReadAll(rc)
		_ = rc.Close()
		orig, _, _ := a.Fixtures.Open(ctx, admin, f.ID)
		want, _ := io.ReadAll(orig)
		_ = orig.Close()
		if !bytes.Equal(got, want) {
			t.Errorf("%s differs after restore", f.Name)
		}
	}
	if info, err := os.Stat(tgt.DBPath); err != nil || info.Mode().Perm() != 0o600 {
		t.Errorf("restored database mode = %v, %v", info, err)
	}
}

func TestRestoreOverExistingInstance(t *testing.T) {
	a, _, _ := instance(t)
	var buf bytes.Buffer
	if _, err := a.Backup.Backup(ctx, &buf); err != nil {
		t.Fatal(err)
	}
	tgt := target(t)
	if _, err := backup.Restore(ctx, bytes.NewReader(buf.Bytes()), tgt, false); err != nil {
		t.Fatal(err)
	}
	if _, err := backup.Restore(ctx, bytes.NewReader(buf.Bytes()), tgt, false); !errors.Is(err, backup.ErrNotEmpty) {
		t.Errorf("restore over an instance err = %v", err)
	}
	sum, err := backup.Restore(ctx, bytes.NewReader(buf.Bytes()), tgt, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(sum.MovedAside) < 2 {
		t.Fatalf("moved aside = %v", sum.MovedAside)
	}
	for _, p := range sum.MovedAside {
		if _, err := os.Stat(p); err != nil || !strings.Contains(p, ".pre-restore-") {
			t.Errorf("moved-aside %s: %v", p, err)
		}
	}
	if _, err := os.Stat(tgt.DBPath); err != nil {
		t.Errorf("no database after overwrite: %v", err)
	}
}

func TestRestoreIntoEmptyBlobDir(t *testing.T) {
	a, _, _ := instance(t)
	var buf bytes.Buffer
	if _, err := a.Backup.Backup(ctx, &buf); err != nil {
		t.Fatal(err)
	}
	tgt := target(t)
	// A server that started once and stored nothing leaves empty directories.
	for _, d := range []string{"objects", "tmp"} {
		if err := os.MkdirAll(filepath.Join(tgt.BlobDir, d), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := backup.Restore(ctx, bytes.NewReader(buf.Bytes()), tgt, false); err != nil {
		t.Errorf("restore into empty blob dir = %v", err)
	}
	other := target(t)
	if err := os.MkdirAll(other.BlobDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(other.BlobDir, "notes.txt"), []byte("mine"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := backup.Restore(ctx, bytes.NewReader(buf.Bytes()), other, false); err == nil {
		t.Error("restore deleted or replaced a blob dir holding other files")
	}
	if b, err := os.ReadFile(filepath.Join(other.BlobDir, "notes.txt")); err != nil || string(b) != "mine" {
		t.Errorf("other files touched: %q %v", b, err)
	}
}

func TestRestoreRejects(t *testing.T) {
	a, _, _ := instance(t)
	var good bytes.Buffer
	if _, err := a.Backup.Backup(ctx, &good); err != nil {
		t.Fatal(err)
	}
	write := func(m archive.Manifest, withDB bool, blobs map[string][]byte) []byte {
		var buf bytes.Buffer
		w, _ := archive.NewWriter(&buf, m)
		if withDB {
			_ = w.Database(5, strings.NewReader("hello"))
		}
		for h, b := range blobs {
			_ = w.Blob(h, int64(len(b)), bytes.NewReader(b))
		}
		_ = w.Close()
		return buf.Bytes()
	}
	cases := map[string][]byte{
		"export archive": write(archive.Manifest{Kind: archive.KindExport}, false, nil),
		"newer schema":   write(archive.Manifest{Kind: archive.KindBackup, SchemaVersion: "99990101000000"}, true, nil),
		"no database":    write(archive.Manifest{Kind: archive.KindBackup}, false, nil),
		"not sqlite":     write(archive.Manifest{Kind: archive.KindBackup}, true, nil),
		"truncated":      good.Bytes()[:good.Len()/2],
		"garbage":        []byte("nope"),
	}
	for name, data := range cases {
		tgt := target(t)
		if _, err := backup.Restore(ctx, bytes.NewReader(data), tgt, false); err == nil {
			t.Errorf("%s: restored", name)
		}
		if _, err := os.Stat(tgt.DBPath); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("%s: a failed restore left a database", name)
		}
		left, _ := filepath.Glob(filepath.Join(filepath.Dir(tgt.DBPath), "*"))
		if len(left) != 0 {
			t.Errorf("%s: a failed restore left %v", name, left)
		}
	}
	if _, err := backup.Restore(ctx, bytes.NewReader(good.Bytes()), backup.Target{}, false); err == nil {
		t.Error("empty target accepted")
	}
}

func TestBackupHoldsGarbageCollection(t *testing.T) {
	a, secret, cfg := instance(t)
	admin := testutil.Authenticate(t, a, secret)
	lock := filepath.Join(cfg.BlobDir, "backup.lock")
	if err := os.WriteFile(lock, []byte("other process"), 0o600); err != nil {
		t.Fatal(err)
	}
	// A second backup waits for no one: it refuses.
	if _, err := a.Backup.Backup(ctx, io.Discard); err == nil || !strings.Contains(err.Error(), "another backup") {
		t.Errorf("concurrent backup err = %v", err)
	}
	ds, _ := a.Datasets.List(ctx, admin, dataset.ListFilter{})
	if err := a.Datasets.Delete(ctx, admin, ds[0].ID); err != nil {
		t.Fatal(err)
	}
	objects, _ := filepath.Glob(filepath.Join(cfg.BlobDir, "objects", "*", "*", "*"))
	if len(objects) != 4 {
		t.Errorf("garbage collection ran during a backup: %d blobs left", len(objects))
	}
	if err := os.Remove(lock); err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	if _, err := a.Backup.Backup(ctx, &buf); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(lock); !errors.Is(err, os.ErrNotExist) {
		t.Error("backup left its lock behind")
	}
}

func TestRestoreHandlesLeftoverWAL(t *testing.T) {
	a, _, _ := instance(t)
	var buf bytes.Buffer
	if _, err := a.Backup.Backup(ctx, &buf); err != nil {
		t.Fatal(err)
	}

	// A server killed before its first checkpoint: empty main file, data in the log.
	crashed := target(t)
	if err := os.MkdirAll(filepath.Dir(crashed.DBPath), 0o700); err != nil {
		t.Fatal(err)
	}
	for p, data := range map[string]string{crashed.DBPath: "", crashed.DBPath + "-wal": "frames", crashed.DBPath + "-shm": "index"} {
		if err := os.WriteFile(p, []byte(data), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := backup.Restore(ctx, bytes.NewReader(buf.Bytes()), crashed, false); !errors.Is(err, backup.ErrNotEmpty) {
		t.Errorf("restore over a log-only instance err = %v", err)
	}
	sum, err := backup.Restore(ctx, bytes.NewReader(buf.Bytes()), crashed, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(sum.MovedAside) != 3 {
		t.Errorf("moved aside = %v", sum.MovedAside)
	}
	for _, p := range []string{crashed.DBPath + "-wal", crashed.DBPath + "-shm"} {
		if _, err := os.Stat(p); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("%s left beside the restored database", p)
		}
	}

	// Empty leftovers are cleared for a fresh restore.
	fresh := target(t)
	_ = os.MkdirAll(filepath.Dir(fresh.DBPath), 0o700)
	for _, p := range []string{fresh.DBPath + "-wal", fresh.DBPath + "-shm"} {
		if err := os.WriteFile(p, nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := backup.Restore(ctx, bytes.NewReader(buf.Bytes()), fresh, false); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(fresh.DBPath + "-wal"); !errors.Is(err, os.ErrNotExist) {
		t.Error("empty -wal left beside the restored database")
	}
	b, err := app.Open(ctx, nil, app.Config{DBPath: fresh.DBPath, BlobDir: fresh.BlobDir})
	if err != nil {
		t.Fatalf("restored database does not open: %v", err)
	}
	_ = b.Close()
}
