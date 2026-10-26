package cmd

import (
	"context"
	"errors"
	"fmt"

	"github.com/paularlott/cli"
)

func init() {
	Register(purgeCmd())
}

// purgeCmd deletes every fixture from an ingest batch, everywhere it was
// copied, frozen datasets included. It cannot be undone, so it needs --yes.
// The server only allows admin keys.
func purgeCmd() *cli.Command {
	return &cli.Command{
		Name:      "purge",
		Usage:     "Delete every fixture from an ingest batch (admin key; cannot be undone)",
		Arguments: []cli.Argument{&cli.StringArg{Name: "ingest-batch", Usage: "Ingest batch to purge", Required: true}},
		Flags: withClientFlags(
			&cli.BoolFlag{Name: "yes", Usage: "Confirm the purge"},
		),
		Run: func(ctx context.Context, cmd *cli.Command) error {
			batch, err := requireArg(cmd, "ingest-batch")
			if err != nil {
				return err
			}
			if !cmd.GetBool("yes") {
				return errors.New("purge deletes data permanently; run again with --yes to confirm")
			}
			c, err := newClient(cmd)
			if err != nil {
				return err
			}
			res, err := c.Purge(ctx, batch)
			if err != nil {
				return err
			}
			if cmd.GetBool("json") {
				return render(cmd, res, nil, nil)
			}
			_, _ = fmt.Fprintf(stdout, "Purged %d fixtures from ingest batch %s.\n", res.Deleted, res.IngestBatch)
			return nil
		},
	}
}
