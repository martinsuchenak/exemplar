package cmd

import (
	"context"
	"fmt"
	"os"

	"github.com/paularlott/cli"
	logslog "github.com/paularlott/logger/slog"

	"github.com/martinsuchenak/exemplar/internal/backup"
)

func init() {
	Register(restoreCmd())
}

// restoreCmd restores a backup into the configured database and blob
// directory. Stop the server first.
func restoreCmd() *cli.Command {
	return &cli.Command{
		Name:      "restore",
		Usage:     "Restore the instance from a backup (run on the host, server stopped)",
		Arguments: []cli.Argument{&cli.StringArg{Name: "file", Usage: "Archive made by exemplar backup", Required: true}},
		Flags: append(storageFlags(),
			&cli.BoolFlag{Name: "overwrite", Usage: "Replace an existing instance (it is moved aside, not deleted)"},
		),
		Run: func(ctx context.Context, cmd *cli.Command) error {
			path, err := requireArg(cmd, "file")
			if err != nil {
				return err
			}
			f, err := os.Open(path)
			if err != nil {
				return err
			}
			defer func() { _ = f.Close() }()
			sum, err := backup.Restore(ctx, f, backup.Target{DBPath: cmd.GetString("db-path"), BlobDir: cmd.GetString("blob-dir")}, cmd.GetBool("overwrite"))
			if err != nil {
				return err
			}
			log := logslog.New(logslog.Config{Level: cmd.GetString("log-level"), Format: cmd.GetString("log-format"), Writer: os.Stderr})
			a, err := openApp(ctx, cmd, log)
			if err != nil {
				return err
			}
			defer func() { _ = a.Close() }()
			if err := a.Backup.RecordRestore(ctx, sum); err != nil {
				return err
			}
			_, _ = fmt.Fprintf(stdout, "Restored %d fixtures and %d contents from the backup of %s.\n", sum.Fixtures, sum.Blobs, sum.CreatedAt.Format("2006-01-02 15:04 MST"))
			for _, p := range sum.MovedAside {
				_, _ = fmt.Fprintf(stdout, "The previous data is at %s; delete it once you no longer need it.\n", p)
			}
			return nil
		},
	}
}
