package cmd

import (
	"context"
	"strings"

	"github.com/paularlott/cli"
)

func init() {
	Register(schemasCmd())
}

// schemasCmd lists the record schemas the server generates: the built-in
// ones and any the server loads from --record-schemas-dir.
func schemasCmd() *cli.Command {
	return &cli.Command{
		Name:  "schemas",
		Usage: "List the record schemas the server can generate",
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
						source := "file"
						if s.Builtin {
							source = "built-in"
						}
						rows[i] = []string{string(s.Name), s.Step, source, s.Hash, strings.Join(s.Fields, ", ")}
					}
					return render(cmd, schemas, []string{"NAME", "STEP", "SOURCE", "HASH", "FIELDS"}, rows)
				},
			},
		},
	}
}
