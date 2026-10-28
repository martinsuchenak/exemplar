package cmd

import (
	"context"
	"fmt"
	"strconv"

	"github.com/paularlott/cli"

	"github.com/martinsuchenak/exemplar/internal/dataset"
)

func init() {
	Register(datasetsCmd())
}

// datasetsCmd manages datasets over the REST API.
func datasetsCmd() *cli.Command {
	return &cli.Command{
		Name:  "datasets",
		Usage: "List, create, freeze and delete datasets",
		Commands: []*cli.Command{
			{
				Name:  "list",
				Usage: "List the datasets you can read",
				Flags: withClientFlags(
					&cli.StringFlag{Name: "scope", Usage: "global, workspace or quarantine"},
					&cli.StringFlag{Name: "name", Usage: "Exact dataset name"},
					&cli.StringFlag{Name: "query", Usage: "Words that must each appear in the name, description or a tag"},
				),
				Run: func(ctx context.Context, cmd *cli.Command) error {
					c, err := newClient(cmd)
					if err != nil {
						return err
					}
					list, err := c.ListDatasets(ctx, dataset.ListFilter{
						Scope: dataset.Scope(cmd.GetString("scope")),
						Name:  cmd.GetString("name"),
						Query: cmd.GetString("query"),
					})
					if err != nil {
						return err
					}
					rows := make([][]string, len(list))
					for i, d := range list {
						rows[i] = datasetRow(d)
					}
					return render(cmd, list, datasetHeader, rows)
				},
			},
			{
				Name:  "create",
				Usage: "Create a dataset (global and quarantine need an admin key)",
				Flags: withClientFlags(
					&cli.StringFlag{Name: "name", Usage: "Dataset name", Required: true},
					&cli.StringFlag{Name: "scope", DefaultValue: string(dataset.ScopeWorkspace), Usage: "global, workspace or quarantine"},
					&cli.StringFlag{Name: "description", Usage: "What the dataset is for"},
					&cli.StringFlag{Name: "tags", Usage: "Comma-separated tags"},
				),
				Run: func(ctx context.Context, cmd *cli.Command) error {
					c, err := newClient(cmd)
					if err != nil {
						return err
					}
					d, err := c.CreateDataset(ctx, dataset.CreateInput{
						Name:        cmd.GetString("name"),
						Scope:       dataset.Scope(cmd.GetString("scope")),
						Description: cmd.GetString("description"),
						Tags:        splitList(cmd.GetString("tags")),
					})
					if err != nil {
						return err
					}
					return render(cmd, d, datasetHeader, [][]string{datasetRow(*d)})
				},
			},
			{
				Name:      "freeze",
				Usage:     "Make a dataset's contents read-only",
				Arguments: []cli.Argument{&cli.StringArg{Name: "dataset", Usage: "Dataset to freeze (id or name)", Required: true}},
				Flags:     withClientFlags(),
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
					d, err := c.FreezeDataset(ctx, id)
					if err != nil {
						return err
					}
					return render(cmd, d, datasetHeader, [][]string{datasetRow(*d)})
				},
			},
			{
				Name:      "delete",
				Usage:     "Delete a dataset and all its fixtures",
				Arguments: []cli.Argument{&cli.StringArg{Name: "dataset-id", Usage: "Dataset to delete", Required: true}},
				Flags:     withClientFlags(),
				Run: func(ctx context.Context, cmd *cli.Command) error {
					id, err := requireArg(cmd, "dataset-id")
					if err != nil {
						return err
					}
					c, err := newClient(cmd)
					if err != nil {
						return err
					}
					if err := c.DeleteDataset(ctx, id); err != nil {
						return err
					}
					_, _ = fmt.Fprintf(stdout, "Deleted dataset %s.\n", id)
					return nil
				},
			},
		},
	}
}

var datasetHeader = []string{"ID", "SCOPE", "NAME", "VERSION", "FROZEN", "OWNER"}

func datasetRow(d dataset.Dataset) []string {
	return []string{d.ID, string(d.Scope), d.Name, strconv.Itoa(d.Version), formatTime(d.FrozenAt), d.OwnerAccountID}
}
