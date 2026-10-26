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
	}
}

// openApp opens the database and blob store named by storageFlags.
func openApp(ctx context.Context, cmd *cli.Command, log logger.Logger) (*app.App, error) {
	return app.Open(ctx, log, app.Config{
		DBPath:  cmd.GetString("db-path"),
		BlobDir: cmd.GetString("blob-dir"),
	})
}
