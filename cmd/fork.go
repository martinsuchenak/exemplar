package cmd

import (
	"context"
	"fmt"

	"github.com/paularlott/cli"

	"github.com/martinsuchenak/exemplar/internal/dataset"
)

func init() {
	Register(forkCmd())
}

// forkCmd copies a dataset into a new workspace owned by the key's account.
func forkCmd() *cli.Command {
	return &cli.Command{
		Name:      "fork",
		Usage:     "Copy a dataset into a new workspace of your own",
		Arguments: []cli.Argument{&cli.StringArg{Name: "dataset", Usage: "Dataset to fork (id or name)", Required: true}},
		Flags: withClientFlags(
			&cli.StringFlag{Name: "name", Usage: "Workspace name (default: the source's name)"},
			&cli.StringFlag{Name: "description", Usage: "Workspace description (default: the source's)"},
		),
		Run: func(ctx context.Context, cmd *cli.Command) error {
			id, err := requireArg(cmd, "dataset")
			if err != nil {
				return err
			}
			c, err := newClient(cmd)
			if err != nil {
				return err
			}
			if id, err = resolveDataset(ctx, c, id); err != nil {
				return err
			}
			res, err := c.Fork(ctx, id, dataset.ForkInput{
				Name:        cmd.GetString("name"),
				Description: cmd.GetString("description"),
			})
			if err != nil {
				return err
			}
			return renderCopy(cmd, res)
		},
	}
}

// renderCopy prints the dataset a fork or promotion created and what it copied.
func renderCopy(cmd *cli.Command, res *dataset.CopyResult) error {
	if cmd.GetBool("json") {
		return render(cmd, res, nil, nil)
	}
	if err := render(cmd, res, datasetHeader, [][]string{datasetRow(*res.Dataset)}); err != nil {
		return err
	}
	_, _ = fmt.Fprintf(stdout, "\nCopied %d fixtures", res.Copied)
	if res.Skipped > 0 {
		_, _ = fmt.Fprintf(stdout, "; skipped %d real-derived fixtures that are not approved", res.Skipped)
	}
	_, _ = fmt.Fprintln(stdout, ".")
	return nil
}
