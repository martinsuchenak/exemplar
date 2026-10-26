package cmd

import (
	"context"
	"fmt"
	"io"

	"github.com/paularlott/cli"
)

func init() {
	Register(exportCmd())
}

// exportCmd writes datasets you can read, with their fixtures, linked people
// and contents, to an archive another instance can import.
func exportCmd() *cli.Command {
	return &cli.Command{
		Name:      "export",
		Usage:     "Export datasets to an archive for another instance",
		Arguments: []cli.Argument{&cli.StringArg{Name: "dataset-id", Usage: "Dataset to export", Required: true}},
		MaxArgs:   cli.UnlimitedArgs,
		Flags: withClientFlags(
			&cli.StringFlag{Name: "output", Usage: "Archive file to write (.tar.gz); never overwritten", Required: true},
		),
		Run: func(ctx context.Context, cmd *cli.Command) error {
			id, err := requireArg(cmd, "dataset-id")
			if err != nil {
				return err
			}
			c, err := newClient(cmd)
			if err != nil {
				return err
			}
			ids := append([]string{id}, cmd.GetArgs()...)
			var counts map[string]int
			path := cmd.GetString("output")
			err = writeArchive(path, func(w io.Writer) error {
				var err error
				counts, err = c.Export(ctx, ids, w)
				return err
			})
			if err != nil {
				return err
			}
			if cmd.GetBool("json") {
				return render(cmd, counts, nil, nil)
			}
			_, _ = fmt.Fprintf(stdout, "Exported %d datasets, %d fixtures and %d people to %s.\n", counts["datasets"], counts["fixtures"], counts["persons"], path)
			if counts["skipped"] > 0 {
				_, _ = fmt.Fprintf(stdout, "%d real-derived fixtures were left out because they are not approved.\n", counts["skipped"])
			}
			return nil
		},
	}
}
