package cmd

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/paularlott/cli"

	"github.com/martinsuchenak/exemplar/internal/recordschema"
	"github.com/martinsuchenak/exemplar/internal/synth"
)

func init() {
	Register(schemasCmd())
}

// schemasCmd lists the record schemas the server generates (the built-in
// ones, any it loads from --record-schemas-dir and any added through the
// API), shows one's file, and adds, changes and removes the API ones.
func schemasCmd() *cli.Command {
	return &cli.Command{
		Name:  "schemas",
		Usage: "List, show, add, update and remove the record schemas the server can generate",
		Commands: []*cli.Command{
			{
				Name:  "list",
				Usage: "List record schemas",
				Flags: withClientFlags(),
				Run: func(ctx context.Context, cmd *cli.Command) error {
					c, err := newClient(cmd)
					if err != nil {
						return err
					}
					schemas, err := c.RecordSchemas(ctx)
					if err != nil {
						return err
					}
					rows := make([][]string, len(schemas))
					for i, s := range schemas {
						rows[i] = schemaRow(s)
					}
					// REQUEST LISTS are the lists a generate request must give
					// (--lists); --json shows every list a schema picks from.
					return render(cmd, schemas, []string{"NAME", "STEP", "SOURCE", "HASH", "REQUEST LISTS", "FIELDS"}, rows)
				},
			},
			{
				Name:      "show",
				Usage:     "Print a record schema's file (redirect it to a file to edit it); a server file's needs an admin key",
				Arguments: []cli.Argument{&cli.StringArg{Name: "name", Usage: "Schema name", Required: true}},
				Flags: withClientFlags(
					&cli.BoolFlag{Name: "api", Usage: "The schema added through the API, even where a server file has taken its name"},
				),
				Run: func(ctx context.Context, cmd *cli.Command) error {
					name, err := requireArg(cmd, "name")
					if err != nil {
						return err
					}
					c, err := newClient(cmd)
					if err != nil {
						return err
					}
					d, err := c.RecordSchema(ctx, name, cmd.GetBool("api"))
					if err != nil {
						return err
					}
					if cmd.GetBool("json") {
						return render(cmd, d, nil, nil)
					}
					// The file alone on stdout, so it can be saved and sent
					// back with update; what it is on stderr.
					_, _ = fmt.Fprint(stdout, d.TOML)
					if !strings.HasSuffix(d.TOML, "\n") {
						_, _ = fmt.Fprintln(stdout)
					}
					_, _ = fmt.Fprintf(stderr, "%s: %s schema", d.Name, schemaSource(d.Source, false, d.Version))
					if d.Hash != "" {
						_, _ = fmt.Fprintf(stderr, ", hash %s", d.Hash)
					}
					_, _ = fmt.Fprintln(stderr)
					if d.Problem != "" {
						_, _ = fmt.Fprintf(stderr, "Not loaded: %s\n", d.Problem)
					}
					return nil
				},
			},
			{
				Name:      "add",
				Usage:     "Add a record schema from its file (admin key); every key can generate from it at once",
				Arguments: []cli.Argument{&cli.StringArg{Name: "file", Usage: "Schema file (*.toml)", Required: true}},
				Flags:     withClientFlags(),
				Run: func(ctx context.Context, cmd *cli.Command) error {
					data, err := readSchemaArg(cmd)
					if err != nil {
						return err
					}
					c, err := newClient(cmd)
					if err != nil {
						return err
					}
					d, err := c.CreateRecordSchema(ctx, data)
					if err != nil {
						return err
					}
					return renderDefinition(cmd, d, "Added")
				},
			},
			{
				Name:      "update",
				Usage:     "Replace a record schema added through the API with a new file of the same name (admin key)",
				Arguments: []cli.Argument{&cli.StringArg{Name: "file", Usage: "Schema file (*.toml)", Required: true}},
				Flags: withClientFlags(
					&cli.IntFlag{Name: "if-version", Usage: "Refuse unless the schema is still at this version (from schemas show), so no one else's change is lost"},
				),
				Run: func(ctx context.Context, cmd *cli.Command) error {
					data, err := readSchemaArg(cmd)
					if err != nil {
						return err
					}
					// The file's name says which schema; the server checks
					// the rest.
					name, err := synth.SchemaFileName(data)
					if err != nil {
						return fmt.Errorf("%s: %w", cmd.GetStringArg("file"), err)
					}
					c, err := newClient(cmd)
					if err != nil {
						return err
					}
					d, err := c.UpdateRecordSchema(ctx, string(name), data, cmd.GetInt("if-version"))
					if err != nil {
						return err
					}
					return renderDefinition(cmd, d, "Updated")
				},
			},
			{
				Name:      "remove",
				Usage:     "Remove a record schema added through the API (admin key); fixtures made from it stay",
				Arguments: []cli.Argument{&cli.StringArg{Name: "name", Usage: "Schema name", Required: true}},
				Flags:     withClientFlags(),
				Run: func(ctx context.Context, cmd *cli.Command) error {
					name, err := requireArg(cmd, "name")
					if err != nil {
						return err
					}
					c, err := newClient(cmd)
					if err != nil {
						return err
					}
					if err := c.DeleteRecordSchema(ctx, name); err != nil {
						return err
					}
					_, _ = fmt.Fprintf(stdout, "Removed record schema %s.\n", name)
					return nil
				},
			},
		},
	}
}

// schemaRow is a schema's row in schemas list. An API schema that does not
// load has no step or fields, and says why instead.
func schemaRow(s synth.Schema) []string {
	dash := func(v string) string {
		if v == "" {
			return "-"
		}
		return v
	}
	fields := strings.Join(s.Fields, ", ")
	if s.Problem != "" {
		fields = "NOT LOADED: " + s.Problem
	}
	return []string{string(s.Name), dash(s.Step), schemaSource(s.Source, s.Builtin, s.Version), dash(s.Hash), dash(strings.Join(s.RequestLists, ", ")), fields}
}

// readSchemaArg reads the file the file argument names.
func readSchemaArg(cmd *cli.Command) (string, error) {
	path, err := requireArg(cmd, "file")
	if err != nil {
		return "", err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

func renderDefinition(cmd *cli.Command, d *recordschema.Definition, verb string) error {
	if cmd.GetBool("json") {
		return render(cmd, d, nil, nil)
	}
	_, _ = fmt.Fprintf(stdout, "%s record schema %s: version %d, hash %s.\n", verb, d.Name, d.Version, d.Hash)
	return nil
}

// schemaSource names where a schema came from; builtin covers a server
// from before sources were reported.
func schemaSource(source string, builtin bool, version int) string {
	switch {
	case source == synth.SourceAPI && version > 0:
		return "api v" + strconv.Itoa(version)
	case source != "":
		return source
	case builtin:
		return synth.SourceBuiltin
	default:
		return synth.SourceFile
	}
}
