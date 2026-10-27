package cmd

import (
	"context"
	"fmt"
	"strings"

	"github.com/paularlott/cli"

	"github.com/martinsuchenak/exemplar/internal/generate"
	"github.com/martinsuchenak/exemplar/internal/synth"
)

func init() {
	Register(generateCmd())
}

// generateCmd fills a dataset with synthetic files, emails or records over the
// REST API. The content is about people from the person registry. With
// --from, emails are rendered from template emails instead of shapes.
func generateCmd() *cli.Command {
	return &cli.Command{
		Name:      "generate",
		Usage:     "Fill a dataset with synthetic files, emails or records",
		Arguments: []cli.Argument{&cli.StringArg{Name: "dataset-id", Usage: "Dataset to fill", Required: true}},
		Flags: withClientFlags(
			&cli.StringFlag{Name: "kind", Usage: "files, emails or records", Required: true},
			&cli.IntFlag{Name: "count", DefaultValue: 10, Usage: fmt.Sprintf("How many items (1-%d); a thread email counts once", generate.MaxCount)},
			&cli.StringFlag{Name: "formats", Usage: "Files: comma-separated, from " + strings.Join(synth.FileFormats(), ", ") + " (default all)"},
			&cli.StringFlag{Name: "shapes", Usage: "Emails: comma-separated, from " + strings.Join(synth.ShapeNames(), ", ") + " (default all)"},
			&cli.StringFlag{Name: "from", Usage: "Emails: comma-separated template email fixture IDs to render instead of shapes (real-derived ones must be approved)"},
			&cli.StringFlag{Name: "schemas", Usage: "Records: comma-separated schema names; exemplar schemas list shows them (default all)"},
			&cli.StringFlag{Name: "profile", DefaultValue: string(synth.ProfileInsurance), Usage: "Business vocabulary: insurance or manufacturing"},
			&cli.IntFlag{Name: "people", Usage: "How many registry people the content is about (default up to 10)"},
			&cli.StringFlag{Name: "seed", Usage: "Seed to reproduce a run (default random; printed afterwards)"},
			&cli.StringFlag{Name: "scenario", Usage: "Scenario label (emails default to their shape)"},
			&cli.StringFlag{Name: "tags", Usage: "Comma-separated tags"},
		),
		Run: func(ctx context.Context, cmd *cli.Command) error {
			id, err := requireArg(cmd, "dataset-id")
			if err != nil {
				return err
			}
			seed, err := parseSeed(cmd.GetString("seed"))
			if err != nil {
				return err
			}
			req := generate.Request{
				Kind:     generate.Kind(cmd.GetString("kind")),
				Count:    cmd.GetInt("count"),
				Formats:  splitList(cmd.GetString("formats")),
				From:     splitList(cmd.GetString("from")),
				Profile:  synth.Profile(cmd.GetString("profile")),
				People:   cmd.GetInt("people"),
				Seed:     seed,
				Scenario: cmd.GetString("scenario"),
				Tags:     splitList(cmd.GetString("tags")),
			}
			for _, s := range splitList(cmd.GetString("shapes")) {
				req.Shapes = append(req.Shapes, synth.Shape(s))
			}
			for _, s := range splitList(cmd.GetString("schemas")) {
				req.Schemas = append(req.Schemas, synth.RecordSchema(s))
			}
			c, err := newClient(cmd)
			if err != nil {
				return err
			}
			res, err := c.Generate(ctx, id, req)
			if err != nil {
				return err
			}
			if cmd.GetBool("json") {
				return render(cmd, res, nil, nil)
			}
			rows := make([][]string, len(res.Fixtures))
			for i, f := range res.Fixtures {
				rows[i] = fixtureRow(f)
			}
			if err := render(cmd, res, fixtureHeader, rows); err != nil {
				return err
			}
			_, _ = fmt.Fprintf(stdout, "\nGenerated %d fixtures about %d people with seed %d (the same seed and people give the same content).\n", len(res.Fixtures), len(res.PersonIDs), res.Seed)
			return nil
		},
	}
}

// parseSeed reads an optional unsigned seed; empty means random.
func parseSeed(s string) (uint64, error) {
	if s == "" {
		return 0, nil
	}
	var v uint64
	if _, err := fmt.Sscan(s, &v); err != nil || v == 0 {
		return 0, fmt.Errorf("seed must be a positive whole number, got %q", s)
	}
	return v, nil
}
