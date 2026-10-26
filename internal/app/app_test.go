package app

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestOpenRequiresPaths(t *testing.T) {
	for _, cfg := range []Config{{}, {DBPath: "x.db"}, {BlobDir: "blobs"}} {
		if _, err := Open(context.Background(), nil, cfg); err == nil {
			t.Errorf("Open(%+v) succeeded, want error", cfg)
		}
	}
}

func TestOpenBuildsServices(t *testing.T) {
	dir := t.TempDir()
	a, err := Open(context.Background(), nil, Config{DBPath: filepath.Join(dir, "x.db"), BlobDir: filepath.Join(dir, "blobs")})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if a.Audit == nil || a.Accounts == nil || a.Datasets == nil || a.Fixtures == nil {
		t.Errorf("a service is missing: %+v", a)
	}
	if err := a.Close(); err != nil {
		t.Errorf("Close: %v", err)
	}
}

func TestOpenFailsOnUnusableBlobDir(t *testing.T) {
	dir := t.TempDir()
	blocker := filepath.Join(dir, "blobs")
	if err := os.WriteFile(blocker, []byte("not a directory"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(context.Background(), nil, Config{DBPath: filepath.Join(dir, "x.db"), BlobDir: blocker}); err == nil {
		t.Error("Open with a file as blob dir succeeded")
	}
}
