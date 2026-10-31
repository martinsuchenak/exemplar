package cmd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/paularlott/cli"

	"github.com/martinsuchenak/exemplar/internal/app"
	"github.com/martinsuchenak/exemplar/internal/generate"
	"github.com/martinsuchenak/exemplar/internal/principal"
	"github.com/martinsuchenak/exemplar/internal/synth"
)

func init() {
	Register(generateCmd())
}

// generateCmd fills a dataset with synthetic files, emails or records over the
// REST API. The content is about people from the person registry. With
// --from, emails are rendered from template emails instead of shapes. With
// --local, it makes records here, about people made up from the seed, and
// prints them as JSON, storing nothing.
func generateCmd() *cli.Command {
	return &cli.Command{
		Name:      "generate",
		Usage:     "Fill a dataset with synthetic files, emails or records, or print records with --local",
		Arguments: []cli.Argument{&cli.StringArg{Name: "dataset", Usage: "Dataset to fill (id or name); none with --local"}},
		Flags: withClientFlags(
			&cli.StringFlag{Name: "kind", Usage: "files, emails or records", Required: true},
			&cli.IntFlag{Name: "count", DefaultValue: 10, Usage: fmt.Sprintf("How many items (1-%d); a thread email counts once", generate.MaxCount)},
			&cli.StringFlag{Name: "formats", Usage: "Files: comma-separated, from " + strings.Join(synth.FileFormats(), ", ") + " (default all)"},
			&cli.StringFlag{Name: "shapes", Usage: "Emails: comma-separated, from " + strings.Join(synth.ShapeNames(), ", ") + " (default all)"},
			&cli.StringFlag{Name: "from", Usage: "Emails: comma-separated template email fixture IDs to render instead of shapes (real-derived ones must be approved)"},
			&cli.StringFlag{Name: "schemas", Usage: "Records: comma-separated schema names; exemplar schemas list shows them (default all, or the --schema-file ones)"},
			&cli.StringFlag{Name: "schema-file", Usage: "Records: comma-separated schema files (*.toml) to send with the request, beside the server's"},
			&cli.StringFlag{Name: "lists", Usage: "Records: a JSON file of lists by name, replacing the schemas' lists or giving their request_lists"},
			&cli.BoolFlag{Name: "local", Usage: "Make records here, about people made up from the seed, and print them as JSON; no server or dataset"},
			recordSchemasDirFlag(), // read with --local only
			&cli.StringFlag{Name: "profile", DefaultValue: string(synth.ProfileInsurance), Usage: "Business vocabulary: insurance or manufacturing"},
			&cli.IntFlag{Name: "people", Usage: "How many registry people the content is about (default up to 10)"},
			&cli.StringFlag{Name: "seed", Usage: "Seed to reproduce a run (default random; printed afterwards)"},
			&cli.StringFlag{Name: "scenario", Usage: "Scenario label (emails default to their shape)"},
			&cli.StringFlag{Name: "tags", Usage: "Comma-separated tags"},
		),
		Run: func(ctx context.Context, cmd *cli.Command) error {
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
			for _, path := range splitList(cmd.GetString("schema-file")) {
				data, err := os.ReadFile(path)
				if err != nil {
					return err
				}
				req.SchemaFiles = append(req.SchemaFiles, string(data))
			}
			if path := cmd.GetString("lists"); path != "" {
				if req.Lists, err = readLists(path); err != nil {
					return err
				}
			}
			if cmd.GetBool("local") {
				return generateLocally(ctx, cmd, req)
			}
			// --record-schemas-dir is for --local, and ignored here: a config
			// file or environment may set it for serve, and a server uses its
			// own (send a file with --schema-file).
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

// generateLocally makes records in this process and prints them as JSON on
// stdout ({"seed": …, "records": […]}), with the seed on stderr: nothing is
// stored, so a dataset, scenario or tags mean nothing here.
func generateLocally(ctx context.Context, cmd *cli.Command, req generate.Request) error {
	switch {
	case cmd.GetStringArg("dataset") != "":
		return errors.New("--local stores nothing, so it takes no dataset")
	case req.Scenario != "" || len(req.Tags) > 0:
		return errors.New("--scenario and --tags label stored fixtures; --local stores nothing")
	}
	svc, err := app.LocalGenerator(cmd.GetString("record-schemas-dir"))
	if err != nil {
		return err
	}
	res, err := svc.Records(ctx, principal.Principal{}, req)
	if err != nil {
		return err
	}
	data, err := json.MarshalIndent(res, "", "  ")
	if err != nil {
		return err
	}
	if _, err := stdout.Write(append(data, '\n')); err != nil {
		return err
	}
	_, _ = fmt.Fprintf(stderr, "Made %d records with seed %d (the same seed, schemas and lists give the same records).\n", len(res.Records), res.Seed)
	return nil
}

// readLists reads a JSON object of lists by name. Numbers keep their exact
// digits (see generate.Lists), so a large ID from a target system stays as
// it is.
func readLists(path string) (generate.Lists, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var lists generate.Lists
	if err := json.Unmarshal(data, &lists); err != nil {
		return nil, fmt.Errorf("--lists %s: want a JSON object of lists by name: %w", path, err)
	}
	return lists, nil
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
