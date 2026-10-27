package app

import (
	"context"
	"github.com/martinsuchenak/exemplar/internal/principal"
	"github.com/martinsuchenak/exemplar/internal/synth"
	"os"
	"path/filepath"
	"strings"
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

func TestAnonymiserConfig(t *testing.T) {
	key := []byte("0123456789abcdef")
	// Ingest needs a key and a detector; the anonymise tool only a detector.
	if c, sc, err := anonymiserConfig(Config{}); c != nil || err != nil || sc == nil || sc.Detector != nil || sc.Key.Valid() {
		t.Errorf("nothing = %+v, %+v, %v", c, sc, err)
	}
	if c, sc, err := anonymiserConfig(Config{AnonKey: key}); c != nil || err != nil || sc.Detector != nil || !sc.Key.Valid() {
		t.Errorf("key without Presidio = %+v, %+v, %v", c, sc, err)
	}
	if c, sc, err := anonymiserConfig(Config{PresidioURL: "http://localhost:3000"}); c != nil || err != nil || sc.Detector == nil || sc.Key.Valid() {
		t.Errorf("Presidio without a key = %+v, %+v, %v", c, sc, err)
	}
	if _, _, err := anonymiserConfig(Config{AnonKey: []byte("short"), PresidioURL: "http://x"}); err == nil {
		t.Error("short key accepted")
	}
	c, sc, err := anonymiserConfig(Config{AnonKey: key, PresidioURL: "http://localhost:3000"})
	if err != nil || c == nil || c.Detector == nil || !c.Key.Valid() || c.Profiles["default"] == nil || sc.Key.ID != c.Key.ID || sc.Detector == nil {
		t.Errorf("configured = %+v, %+v, %v", c, sc, err)
	}

	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "bad.toml"), []byte("name = ["), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(context.Background(), nil, Config{DBPath: filepath.Join(dir, "x.db"), BlobDir: filepath.Join(dir, "b"), AnonProfilesDir: dir}); err == nil {
		t.Error("Open with a broken profile succeeded")
	}
	// A broken record schema stops the server too, naming the file.
	if _, err := Open(context.Background(), nil, Config{DBPath: filepath.Join(dir, "y.db"), BlobDir: filepath.Join(dir, "c"), RecordSchemasDir: dir}); err == nil || !strings.Contains(err.Error(), "bad.toml") {
		t.Errorf("Open with a broken record schema err = %v", err)
	}
}

func TestLocalAnonymiser(t *testing.T) {
	for name, cfg := range map[string]LocalConfig{
		"presidio":      {PresidioURL: "http://localhost:3000"},
		"patterns only": {PatternsOnly: true},
		"with a key":    {PatternsOnly: true, Key: []byte("0123456789abcdef")},
		"nothing":       {},
	} {
		if s, err := LocalAnonymiser(cfg); err != nil || s == nil || s.MaxBytes() != 0 {
			t.Errorf("%s: %v", name, err)
		}
	}
	for name, cfg := range map[string]LocalConfig{
		"both":      {PresidioURL: "http://x", PatternsOnly: true},
		"short key": {PatternsOnly: true, Key: []byte("short")},
	} {
		if _, err := LocalAnonymiser(cfg); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
}

func TestOpenLoadsRecordSchemas(t *testing.T) {
	dir := t.TempDir()
	schema := "name = \"ticket\"\ntitle = \"{subject}\"\n\n[[fields]]\nname = \"subject\"\ntext = \"subject\"\n"
	if err := os.WriteFile(filepath.Join(dir, "ticket.toml"), []byte(schema), 0o600); err != nil {
		t.Fatal(err)
	}
	a, err := Open(context.Background(), nil, Config{DBPath: filepath.Join(dir, "x.db"), BlobDir: filepath.Join(dir, "b"), RecordSchemasDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = a.Close() }()
	schemas, err := a.Generate.Schemas(principal.Principal{Scopes: []principal.Scope{principal.ScopeRead}})
	if want := len(synth.BuiltinSchemas().Names()) + 1; err != nil || len(schemas) != want {
		t.Errorf("schemas = %d, %v; want the built-in ones and ticket, %d", len(schemas), err, want)
	}
}

func TestOpenWithoutMigrating(t *testing.T) {
	dir := t.TempDir()
	cfg := Config{DBPath: filepath.Join(dir, "x.db"), BlobDir: filepath.Join(dir, "blobs"), NoMigrate: true}
	if _, err := Open(context.Background(), nil, cfg); err == nil {
		t.Error("NoMigrate created a database")
	}
	cfg.NoMigrate = false
	a, err := Open(context.Background(), nil, cfg)
	if err != nil {
		t.Fatal(err)
	}
	_ = a.Close()
	cfg.NoMigrate = true
	a, err = Open(context.Background(), nil, cfg)
	if err != nil {
		t.Fatalf("current database refused: %v", err)
	}
	_ = a.Close()
}
