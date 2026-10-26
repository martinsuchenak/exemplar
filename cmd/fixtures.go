package cmd

import (
	"context"
	"fmt"
	"io"
	"os"
	"strconv"

	"github.com/paularlott/cli"

	"github.com/martinsuchenak/exemplar/internal/fixture"
)

func init() {
	Register(fixturesCmd())
}

// fixturesCmd lists and downloads fixtures over the REST API. Adding them is
// the upload command.
func fixturesCmd() *cli.Command {
	return &cli.Command{
		Name:  "fixtures",
		Usage: "List and download fixtures",
		Commands: []*cli.Command{
			{
				Name:      "list",
				Usage:     "List a dataset's fixtures",
				Arguments: []cli.Argument{&cli.StringArg{Name: "dataset-id", Usage: "Dataset to list", Required: true}},
				Flags: withClientFlags(
					&cli.StringFlag{Name: "kind", Usage: "email, file or record"},
					&cli.StringFlag{Name: "format", Usage: "File format, e.g. eml"},
					&cli.StringFlag{Name: "scenario", Usage: "Scenario label"},
					&cli.StringFlag{Name: "tag", Usage: "Tag"},
					&cli.StringFlag{Name: "review-state", Usage: "not-required, unreviewed, approved or rejected"},
					&cli.IntFlag{Name: "limit", Usage: "Maximum fixtures (default 100, at most 1000)"},
					&cli.IntFlag{Name: "offset", Usage: "Fixtures to skip"},
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
					list, err := c.ListFixtures(ctx, id, fixture.ListFilter{
						Kind:        fixture.Kind(cmd.GetString("kind")),
						Format:      cmd.GetString("format"),
						Scenario:    cmd.GetString("scenario"),
						Tag:         cmd.GetString("tag"),
						ReviewState: fixture.ReviewState(cmd.GetString("review-state")),
						Limit:       cmd.GetInt("limit"),
						Offset:      cmd.GetInt("offset"),
					})
					if err != nil {
						return err
					}
					rows := make([][]string, len(list))
					for i, f := range list {
						rows[i] = fixtureRow(f)
					}
					return render(cmd, list, fixtureHeader, rows)
				},
			},
			{
				Name:      "download",
				Usage:     "Write a fixture's content to a file or stdout",
				Arguments: []cli.Argument{&cli.StringArg{Name: "fixture-id", Usage: "Fixture to download", Required: true}},
				Flags: withClientFlags(
					&cli.StringFlag{Name: "output", Usage: "File to write (default: stdout)"},
				),
				Run: func(ctx context.Context, cmd *cli.Command) error {
					id, err := requireArg(cmd, "fixture-id")
					if err != nil {
						return err
					}
					c, err := newClient(cmd)
					if err != nil {
						return err
					}
					body, err := c.Download(ctx, id)
					if err != nil {
						return err
					}
					defer func() { _ = body.Close() }()
					out := stdout
					if path := cmd.GetString("output"); path != "" {
						f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_EXCL, 0o600)
						if err != nil {
							return fmt.Errorf("creating %s: %w", path, err)
						}
						defer func() { _ = f.Close() }()
						out = f
					}
					_, err = io.Copy(out, body)
					return err
				},
			},
		},
	}
}

var fixtureHeader = []string{"ID", "NAME", "KIND", "FORMAT", "SIZE", "SOURCE", "REVIEW", "SCENARIO"}

func fixtureRow(f fixture.Fixture) []string {
	return []string{f.ID, f.Name, string(f.Kind), f.Format, strconv.FormatInt(f.Size, 10), string(f.Source), string(f.ReviewState), orDash(f.Scenario)}
}
