package cmd

import (
	"context"
	"fmt"
	"io"
	"os"

	"github.com/paularlott/cli"
	logslog "github.com/paularlott/logger/slog"

	"github.com/martinsuchenak/exemplar/internal/backup"
)

func init() {
	Register(backupCmd())
}

// backupCmd writes a backup of the whole instance. It opens the database
// directly, so it runs on the host, and is safe while the server runs.
func backupCmd() *cli.Command {
	return &cli.Command{
		Name:  "backup",
		Usage: "Back up the whole instance to an archive (run on the host)",
		Flags: append(storageFlags(),
			&cli.StringFlag{Name: "output", Usage: "Archive file to write (.tar.gz); never overwritten", Required: true},
		),
		Run: func(ctx context.Context, cmd *cli.Command) error {
			log := logslog.New(logslog.Config{Level: cmd.GetString("log-level"), Format: cmd.GetString("log-format"), Writer: os.Stderr})
			a, err := openApp(ctx, cmd, log)
			if err != nil {
				return err
			}
			defer func() { _ = a.Close() }()
			var sum *backup.Summary
			path := cmd.GetString("output")
			err = writeArchive(path, func(w io.Writer) error {
				var err error
				sum, err = a.Backup.Backup(ctx, w)
				return err
			})
			if err != nil {
				return err
			}
			_, _ = fmt.Fprintf(stdout, "Backed up %d fixtures and %d contents (%d bytes, schema %s) to %s.\n", sum.Fixtures, sum.Blobs, sum.Bytes, sum.SchemaVersion, path)
			_, _ = fmt.Fprintln(stdout, "It holds key hashes, the audit log and quarantined data: keep it on encrypted storage.")
			return nil
		},
	}
}
