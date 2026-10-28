package cmd

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"

	"github.com/paularlott/cli"

	"github.com/martinsuchenak/exemplar/internal/client"
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
				Arguments: []cli.Argument{&cli.StringArg{Name: "dataset", Usage: "Dataset to list (id or name)", Required: true}},
				Flags: withClientFlags(
					&cli.StringFlag{Name: "kind", Usage: "email, file or record"},
					&cli.StringFlag{Name: "format", Usage: "File format, e.g. eml"},
					&cli.StringFlag{Name: "scenario", Usage: "Scenario label"},
					&cli.StringFlag{Name: "tag", Usage: "Tag"},
					&cli.StringFlag{Name: "review-state", Usage: "not-required, unreviewed, approved or rejected"},
					&cli.StringFlag{Name: "record-schema", Usage: "Records of one schema, e.g. contact"},
					&cli.StringFlag{Name: "person", Usage: "Fixtures that mention this person ID"},
					&cli.StringFlag{Name: "query", Usage: "Words that must each appear in the fixture name"},
					&cli.IntFlag{Name: "limit", Usage: "Maximum fixtures (default 100, at most 1000)"},
					&cli.IntFlag{Name: "offset", Usage: "Fixtures to skip"},
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
					list, err := c.ListFixtures(ctx, id, fixture.ListFilter{
						Kind:         fixture.Kind(cmd.GetString("kind")),
						Format:       cmd.GetString("format"),
						Scenario:     cmd.GetString("scenario"),
						Tag:          cmd.GetString("tag"),
						ReviewState:  fixture.ReviewState(cmd.GetString("review-state")),
						RecordSchema: cmd.GetString("record-schema"),
						PersonID:     cmd.GetString("person"),
						Query:        cmd.GetString("query"),
						Limit:        cmd.GetInt("limit"),
						Offset:       cmd.GetInt("offset"),
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
			{
				Name:      "download-all",
				Usage:     "Write every fixture of a dataset that matches the filters to a directory",
				Arguments: []cli.Argument{&cli.StringArg{Name: "dataset", Usage: "Dataset to download (id or name)", Required: true}},
				Flags: withClientFlags(
					&cli.StringFlag{Name: "dir", Usage: "Directory to write to; created if missing, files in it never overwritten", Required: true},
					&cli.StringFlag{Name: "kind", Usage: "email, file or record"},
					&cli.StringFlag{Name: "format", Usage: "File format, e.g. eml"},
					&cli.StringFlag{Name: "scenario", Usage: "Scenario label"},
					&cli.StringFlag{Name: "tag", Usage: "Tag"},
					&cli.StringFlag{Name: "review-state", Usage: "not-required, unreviewed, approved or rejected"},
					&cli.StringFlag{Name: "record-schema", Usage: "Records of one schema, e.g. contact"},
					&cli.StringFlag{Name: "person", Usage: "Fixtures that mention this person ID"},
					&cli.StringFlag{Name: "query", Usage: "Words that must each appear in the fixture name"},
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
					list, err := listAll(ctx, c, id, fixture.ListFilter{
						Kind:         fixture.Kind(cmd.GetString("kind")),
						Format:       cmd.GetString("format"),
						Scenario:     cmd.GetString("scenario"),
						Tag:          cmd.GetString("tag"),
						ReviewState:  fixture.ReviewState(cmd.GetString("review-state")),
						RecordSchema: cmd.GetString("record-schema"),
						PersonID:     cmd.GetString("person"),
						Query:        cmd.GetString("query"),
					})
					if err != nil {
						return err
					}
					return downloadAll(ctx, cmd, c, list, cmd.GetString("dir"))
				},
			},
		},
	}
}

// downloadAll writes each fixture to dir under its name. It checks every
// target first and writes nothing if any exists, so a rerun never mixes old
// and new files; a fixture that fails to download is reported and the rest
// still written.
func downloadAll(ctx context.Context, cmd *cli.Command, c *client.Client, list []fixture.Fixture, dir string) error {
	paths := make([]string, len(list))
	for i, f := range list {
		paths[i] = filepath.Join(dir, localName(f))
		if _, err := os.Lstat(paths[i]); err == nil {
			return fmt.Errorf("%s already exists; download into an empty directory", paths[i])
		}
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	var done []fixture.Fixture
	failed, unreviewed := 0, 0
	for i, f := range list {
		if err := downloadTo(ctx, c, f.ID, paths[i]); err != nil {
			failed++
			_, _ = fmt.Fprintf(stderr, "%s: %v\n", f.Name, err)
			continue
		}
		done = append(done, f)
		if f.ReviewState == fixture.ReviewUnreviewed {
			unreviewed++
		}
	}
	if cmd.GetBool("json") {
		if err := render(cmd, done, nil, nil); err != nil {
			return err
		}
	} else {
		_, _ = fmt.Fprintf(stdout, "Downloaded %d fixtures to %s.\n", len(done), dir)
		if unreviewed > 0 {
			_, _ = fmt.Fprintf(stdout, "%d are unreviewed real-derived data: keep them on this machine and delete them once reviewed.\n", unreviewed)
		}
	}
	if failed > 0 {
		return fmt.Errorf("%d of %d fixtures failed", failed, len(list))
	}
	return nil
}

// localName is the file name a fixture is written under. Names are unique in
// a dataset and the server allows no path separators in them; Base keeps an
// odd one inside the directory anyway.
func localName(f fixture.Fixture) string {
	name := filepath.Base(f.Name)
	if name == "." || name == ".." || name == string(filepath.Separator) {
		return f.ID
	}
	return name
}

// downloadTo writes a fixture's content to a new file at path (mode 600).
func downloadTo(ctx context.Context, c *client.Client, id, path string) error {
	body, err := c.Download(ctx, id)
	if err != nil {
		return err
	}
	defer func() { _ = body.Close() }()
	out, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, body); err != nil {
		_ = out.Close()
		_ = os.Remove(path)
		return err
	}
	return out.Close()
}

var fixtureHeader = []string{"ID", "NAME", "KIND", "FORMAT", "SIZE", "SOURCE", "REVIEW", "SCENARIO"}

func fixtureRow(f fixture.Fixture) []string {
	return []string{f.ID, f.Name, string(f.Kind), f.Format, strconv.FormatInt(f.Size, 10), string(f.Source), string(f.ReviewState), orDash(f.Scenario)}
}
