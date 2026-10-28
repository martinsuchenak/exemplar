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
	"github.com/martinsuchenak/exemplar/internal/ingest"
)

func init() {
	Register(ingestCmd())
}

// ingestCmd sends real emails and documents to the server, which anonymises
// them and stores the results in a quarantine dataset for review. Admin key.
func ingestCmd() *cli.Command {
	return &cli.Command{
		Name:      "ingest",
		Usage:     "Anonymise real emails and documents into a quarantine dataset (admin key)",
		Arguments: []cli.Argument{&cli.StringArg{Name: "dataset", Usage: "Quarantine dataset (id or name)", Required: true}},
		MinArgs:   1,
		MaxArgs:   cli.UnlimitedArgs,
		Flags: withClientFlags(
			&cli.StringFlag{Name: "profile", Usage: "Anonymisation profile (default: default)"},
			&cli.StringFlag{Name: "batch", Usage: "Ingest batch name, for purge (default: a new one)"},
			&cli.BoolFlag{Name: "dry-run", Usage: "Show every replacement without storing anything (prints original data)"},
		),
		Run: func(ctx context.Context, cmd *cli.Command) error {
			id, err := requireArg(cmd, "dataset")
			if err != nil {
				return err
			}
			paths, err := expandPaths(cmd.GetArgs())
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
			files := make([]client.IngestFile, len(paths))
			for i, p := range paths {
				files[i] = client.IngestFile{Name: filepath.Base(p), Open: func() (io.ReadCloser, error) { return os.Open(p) }}
			}
			req := ingest.Request{Profile: cmd.GetString("profile"), Batch: cmd.GetString("batch"), DryRun: cmd.GetBool("dry-run")}
			res, err := c.Ingest(ctx, id, req, files)
			if err != nil {
				return err
			}
			return reportIngest(cmd, res)
		},
	}
}

func reportIngest(cmd *cli.Command, res *ingest.Result) error {
	if res.DryRun {
		_, _ = fmt.Fprintln(stderr, "The report below contains ORIGINAL personal data: do not paste, share or keep it.")
	}
	if cmd.GetBool("json") {
		return render(cmd, res, nil, nil)
	}
	failed := 0
	rows := make([][]string, len(res.Files))
	for i, f := range res.Files {
		status := "stored"
		if res.DryRun {
			status = "checked"
		}
		if f.Error != "" {
			status, failed = "failed: "+f.Error, failed+1
		}
		rows[i] = []string{f.Name, orDash(f.FixtureName), strconv.Itoa(f.Replacements), strconv.Itoa(f.Attachments), strconv.Itoa(f.DroppedHeaders), strconv.Itoa(f.DroppedParts), status}
	}
	if err := render(cmd, res, []string{"FILE", "FIXTURE", "REPLACED", "ATTACHMENTS", "HEADERS DROPPED", "PARTS DROPPED", "STATUS"}, rows); err != nil {
		return err
	}
	if res.DryRun {
		for _, f := range res.Files {
			if len(f.Report) == 0 {
				continue
			}
			_, _ = fmt.Fprintf(stdout, "\n%s\n", f.Name)
			for _, l := range f.Report {
				_, _ = fmt.Fprintf(stdout, "  %-15s %q -> %q\n", l.Entity, l.Original, l.Fake)
			}
		}
	}
	_, _ = fmt.Fprintf(stdout, "\nBatch %s, profile %s, key %s.", res.Batch, res.Profile, res.KeyID)
	if !res.DryRun {
		_, _ = fmt.Fprint(stdout, " Review each fixture, then approve or reject it.")
	}
	_, _ = fmt.Fprintln(stdout)
	if failed > 0 {
		return fmt.Errorf("%d of %d files failed", failed, len(res.Files))
	}
	return nil
}
