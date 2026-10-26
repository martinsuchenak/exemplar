package cmd

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"github.com/paularlott/cli"

	"github.com/martinsuchenak/exemplar/internal/client"
	"github.com/martinsuchenak/exemplar/internal/fixture"
)

func init() {
	Register(uploadCmd())
}

// uploadCmd adds files to a dataset over the REST API. A directory uploads
// the regular files directly inside it. Each file becomes a fixture named
// after the file; the server infers kind, format and MIME type.
func uploadCmd() *cli.Command {
	return &cli.Command{
		Name:      "upload",
		Usage:     "Upload files into a dataset (declares they contain no real data)",
		Arguments: []cli.Argument{&cli.StringArg{Name: "dataset-id", Usage: "Dataset to add to", Required: true}},
		MinArgs:   1,
		MaxArgs:   cli.UnlimitedArgs,
		Flags: withClientFlags(
			&cli.StringFlag{Name: "scenario", Usage: "Scenario label for every file"},
			&cli.StringFlag{Name: "tags", Usage: "Comma-separated tags for every file"},
			&cli.StringFlag{Name: "kind", Usage: "email, file or record (default: from the extension)"},
			&cli.StringFlag{Name: "source", DefaultValue: string(fixture.SourceUploaded), Usage: "uploaded, synthetic or public-corpus"},
			&cli.StringFlag{Name: "generator", Usage: "Tool that generated synthetic files"},
		),
		Run: func(ctx context.Context, cmd *cli.Command) error {
			datasetID, err := requireArg(cmd, "dataset-id")
			if err != nil {
				return err
			}
			files, err := expandPaths(cmd.GetArgs())
			if err != nil {
				return err
			}
			c, err := newClient(cmd)
			if err != nil {
				return err
			}

			var uploaded []fixture.Fixture
			var failed int
			for _, path := range files {
				f, err := uploadFile(ctx, c, datasetID, path, client.UploadInput{
					Name:      filepath.Base(path),
					Kind:      fixture.Kind(cmd.GetString("kind")),
					Scenario:  cmd.GetString("scenario"),
					Tags:      splitList(cmd.GetString("tags")),
					Source:    fixture.Source(cmd.GetString("source")),
					Generator: cmd.GetString("generator"),
				})
				if err != nil {
					failed++
					_, _ = fmt.Fprintf(stderr, "%s: %v\n", path, err)
					continue
				}
				uploaded = append(uploaded, *f)
			}

			rows := make([][]string, len(uploaded))
			for i, f := range uploaded {
				rows[i] = fixtureRow(f)
			}
			if err := render(cmd, uploaded, fixtureHeader, rows); err != nil {
				return err
			}
			if failed > 0 {
				return fmt.Errorf("%d of %d files failed to upload", failed, len(files))
			}
			return nil
		},
	}
}

func uploadFile(ctx context.Context, c *client.Client, datasetID, path string, in client.UploadInput) (*fixture.Fixture, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	return c.Upload(ctx, datasetID, in, f)
}

// expandPaths turns the arguments into a list of regular files. Directories
// contribute the regular files directly inside them, in name order.
func expandPaths(args []string) ([]string, error) {
	if len(args) == 0 {
		return nil, errors.New("give at least one file or directory to upload")
	}
	var files []string
	for _, arg := range args {
		info, err := os.Stat(arg)
		if err != nil {
			return nil, err
		}
		if info.Mode().IsRegular() {
			files = append(files, arg)
			continue
		}
		if !info.IsDir() {
			return nil, fmt.Errorf("%s is not a regular file or directory", arg)
		}
		entries, err := os.ReadDir(arg)
		if err != nil {
			return nil, err
		}
		var inDir []string
		for _, e := range entries {
			if e.Type().IsRegular() {
				inDir = append(inDir, filepath.Join(arg, e.Name()))
			}
		}
		sort.Strings(inDir)
		files = append(files, inDir...)
	}
	if len(files) == 0 {
		return nil, errors.New("no files to upload")
	}
	return files, nil
}
