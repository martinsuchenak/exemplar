package cmd

import (
	"context"
	"fmt"
	"os"

	"github.com/paularlott/cli"
	"github.com/paularlott/logger"

	"github.com/martinsuchenak/exemplar/internal/app"
)

// storageFlags are shared by every command that opens the database directly.
func storageFlags() []cli.Flag {
	return []cli.Flag{
		&cli.StringFlag{
			Name:         "db-path",
			DefaultValue: "data/exemplar.db",
			Usage:        "SQLite database file",
			ConfigPath:   []string{"database.path"},
			EnvVars:      []string{"DB_PATH"},
		},
		&cli.StringFlag{
			Name:         "blob-dir",
			DefaultValue: "data/blobs",
			Usage:        "Directory holding fixture contents",
			ConfigPath:   []string{"storage.blob_dir"},
			EnvVars:      []string{"BLOB_DIR"},
		},
		&cli.IntFlag{
			Name:         "max-fixture-mb",
			DefaultValue: 100,
			Usage:        "Largest fixture content accepted, in MiB (0 for no limit)",
			ConfigPath:   []string{"storage.max_fixture_mb"},
			EnvVars:      []string{"MAX_FIXTURE_MB"},
		},
	}
}

// anonymiserFlags set up the anonymiser: serve takes them, and so does
// anonymise --local, which runs it without a server.
func anonymiserFlags() []cli.Flag {
	return []cli.Flag{
		&cli.StringFlag{
			Name:       "anon-key-file",
			Usage:      "File holding the anonymisation key (mode 600); or set EXEMPLAR_ANON_KEY",
			ConfigPath: []string{"anonymiser.key_file"},
			EnvVars:    []string{"ANON_KEY_FILE"},
		},
		&cli.StringFlag{
			Name:       "presidio-url",
			Usage:      "Presidio Analyzer URL, e.g. http://localhost:3000",
			ConfigPath: []string{"anonymiser.presidio_url"},
			EnvVars:    []string{"PRESIDIO_URL"},
		},
		&cli.StringFlag{
			Name:       "anon-profiles-dir",
			Usage:      "Directory of extra anonymisation profiles (*.toml)",
			ConfigPath: []string{"anonymiser.profiles_dir"},
			EnvVars:    []string{"ANON_PROFILES_DIR"},
		},
	}
}

// serverFlags are the settings only serve takes: the anonymiser (ingest of
// real data runs on the server, holding the key) and request size limits.
func serverFlags() []cli.Flag {
	return append(anonymiserFlags(), []cli.Flag{
		&cli.StringFlag{
			Name:       "record-schemas-dir",
			Usage:      "Directory of extra record schemas (*.toml); one named like a built-in replaces it",
			ConfigPath: []string{"generate.record_schemas_dir"},
			EnvVars:    []string{"RECORD_SCHEMAS_DIR"},
		},
		&cli.IntFlag{
			Name:         "max-ingest-mb",
			DefaultValue: 200,
			Usage:        "Largest total upload one ingest or anonymise run accepts, in MiB (0 for no limit)",
			ConfigPath:   []string{"storage.max_ingest_mb"},
			EnvVars:      []string{"MAX_INGEST_MB"},
		},
		&cli.IntFlag{
			Name:         "max-import-mb",
			DefaultValue: 2048,
			Usage:        "Largest archive one import accepts, in MiB (0 for no limit)",
			ConfigPath:   []string{"storage.max_import_mb"},
			EnvVars:      []string{"MAX_IMPORT_MB"},
		},
		&cli.IntFlag{
			Name:         "max-concurrent-runs",
			DefaultValue: 2,
			Usage:        "Most ingest and anonymise runs held at once, together (0 for no limit)",
			ConfigPath:   []string{"storage.max_concurrent_runs"},
			EnvVars:      []string{"MAX_CONCURRENT_RUNS"},
		},
	}...)
}

// openApp opens the database and blob store named by storageFlags.
func openApp(ctx context.Context, cmd *cli.Command, log logger.Logger) (*app.App, error) {
	return app.Open(ctx, log, app.Config{
		DBPath:          cmd.GetString("db-path"),
		BlobDir:         cmd.GetString("blob-dir"),
		MaxFixtureBytes: int64(cmd.GetInt("max-fixture-mb")) << 20,
	})
}

// openServerApp is openApp plus the anonymiser settings serve takes.
func openServerApp(ctx context.Context, cmd *cli.Command, log logger.Logger) (*app.App, error) {
	key, err := anonymiserKey(cmd.GetString("anon-key-file"), os.Getenv("EXEMPLAR_ANON_KEY"))
	if err != nil {
		return nil, err
	}
	return app.Open(ctx, log, app.Config{
		DBPath:            cmd.GetString("db-path"),
		BlobDir:           cmd.GetString("blob-dir"),
		MaxFixtureBytes:   int64(cmd.GetInt("max-fixture-mb")) << 20,
		MaxIngestBytes:    int64(cmd.GetInt("max-ingest-mb")) << 20,
		MaxImportBytes:    int64(cmd.GetInt("max-import-mb")) << 20,
		MaxConcurrentRuns: cmd.GetInt("max-concurrent-runs"),
		AnonKey:           key,
		PresidioURL:       cmd.GetString("presidio-url"),
		AnonProfilesDir:   cmd.GetString("anon-profiles-dir"),
		RecordSchemasDir:  cmd.GetString("record-schemas-dir"),
	})
}

// anonymiserKey reads the key from file, which other users must not be able
// to read, or takes it from the environment. Neither set means no key.
func anonymiserKey(file, env string) ([]byte, error) {
	if file == "" {
		return []byte(env), nil
	}
	info, err := os.Stat(file)
	if err != nil {
		return nil, fmt.Errorf("anonymisation key file: %w", err)
	}
	if info.Mode().Perm()&0o077 != 0 {
		return nil, fmt.Errorf("anonymisation key file %s is readable by other users; chmod 600 it", file)
	}
	return os.ReadFile(file)
}
