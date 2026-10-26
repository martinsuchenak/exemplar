package cmd

import (
	"context"

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

// openApp opens the database and blob store named by storageFlags.
func openApp(ctx context.Context, cmd *cli.Command, log logger.Logger) (*app.App, error) {
	return app.Open(ctx, log, app.Config{
		DBPath:          cmd.GetString("db-path"),
		BlobDir:         cmd.GetString("blob-dir"),
		MaxFixtureBytes: int64(cmd.GetInt("max-fixture-mb")) << 20,
	})
}
