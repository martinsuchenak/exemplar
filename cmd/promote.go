package cmd

import (
	"context"

	"github.com/paularlott/cli"

	"github.com/martinsuchenak/exemplar/internal/dataset"
)

func init() {
	Register(promoteCmd())
}

// promoteCmd copies a dataset into the global library as a new frozen
// version. The server only allows admin keys.
func promoteCmd() *cli.Command {
	return &cli.Command{
		Name:      "promote",
		Usage:     "Copy a dataset into the global library as a frozen version (admin key)",
		Arguments: []cli.Argument{&cli.StringArg{Name: "dataset-id", Usage: "Dataset to promote", Required: true}},
		Flags: withClientFlags(
			&cli.StringFlag{Name: "name", Usage: "Global dataset name (default: the source's name)"},
			&cli.StringFlag{Name: "description", Usage: "Global dataset description (default: the source's)"},
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
			res, err := c.Promote(ctx, id, dataset.PromoteInput{
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
