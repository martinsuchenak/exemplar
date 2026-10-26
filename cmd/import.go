package cmd

import (
	"context"
	"fmt"
	"os"
	"strconv"

	"github.com/paularlott/cli"
)

func init() {
	Register(importCmd())
}

// importCmd adds the datasets in an export archive to this instance.
func importCmd() *cli.Command {
	return &cli.Command{
		Name:      "import",
		Usage:     "Import datasets from an export archive",
		Arguments: []cli.Argument{&cli.StringArg{Name: "file", Usage: "Archive made by exemplar export", Required: true}},
		Flags:     withClientFlags(),
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
			c, err := newClient(cmd)
			if err != nil {
				return err
			}
			res, err := c.Import(ctx, f)
			if err != nil {
				return err
			}
			if cmd.GetBool("json") {
				return render(cmd, res, nil, nil)
			}
			rows := make([][]string, len(res.Datasets))
			for i, d := range res.Datasets {
				state := "added"
				if !d.Created {
					state = "already here"
				}
				rows[i] = []string{d.ID, d.Name, string(d.Scope), strconv.Itoa(d.Version), state, strconv.Itoa(d.Added), strconv.Itoa(d.Existing)}
			}
			if err := render(cmd, res, []string{"ID", "NAME", "SCOPE", "VERSION", "DATASET", "FIXTURES ADDED", "ALREADY HERE"}, rows); err != nil {
				return err
			}
			_, _ = fmt.Fprintf(stdout, "\nPeople: %d added, %d matched to people already here.\n", res.PeopleAdded, res.PeopleMerged)
			for _, s := range res.Skipped {
				_, _ = fmt.Fprintf(stderr, "skipped %s (%s): %s\n", s.Name, s.ID, s.Reason)
			}
			return nil
		},
	}
}
