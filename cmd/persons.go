package cmd

import (
	"context"
	"fmt"

	"github.com/paularlott/cli"

	"github.com/martinsuchenak/exemplar/internal/person"
)

func init() {
	Register(personsCmd())
}

// personsCmd lists and adds people in the person registry over the REST API.
func personsCmd() *cli.Command {
	return &cli.Command{
		Name:  "persons",
		Usage: "List and generate people in the person registry",
		Commands: []*cli.Command{
			{
				Name:  "list",
				Usage: "List people",
				Flags: withClientFlags(
					&cli.StringFlag{Name: "company", Usage: "Exact company name"},
					&cli.IntFlag{Name: "limit", Usage: "Maximum people (default 100, at most 1000)"},
					&cli.IntFlag{Name: "offset", Usage: "People to skip"},
				),
				Run: func(ctx context.Context, cmd *cli.Command) error {
					c, err := newClient(cmd)
					if err != nil {
						return err
					}
					people, err := c.ListPersons(ctx, person.ListFilter{
						Company: cmd.GetString("company"),
						Limit:   cmd.GetInt("limit"),
						Offset:  cmd.GetInt("offset"),
					})
					if err != nil {
						return err
					}
					return renderPeople(cmd, people, people)
				},
			},
			{
				Name:  "generate",
				Usage: "Add new people to the registry",
				Flags: withClientFlags(
					&cli.IntFlag{Name: "count", DefaultValue: 10, Usage: fmt.Sprintf("How many people (1-%d)", person.MaxGenerate)},
					&cli.StringFlag{Name: "seed", Usage: "Seed to reproduce a run (default random)"},
				),
				Run: func(ctx context.Context, cmd *cli.Command) error {
					seed, err := parseSeed(cmd.GetString("seed"))
					if err != nil {
						return err
					}
					c, err := newClient(cmd)
					if err != nil {
						return err
					}
					res, err := c.GeneratePersons(ctx, person.GenerateInput{Count: cmd.GetInt("count"), Seed: seed})
					if err != nil {
						return err
					}
					if err := renderPeople(cmd, res, res.People); err != nil {
						return err
					}
					if !cmd.GetBool("json") {
						_, _ = fmt.Fprintf(stdout, "\nSeed %d.\n", res.Seed)
					}
					return nil
				},
			},
		},
	}
}

var personHeader = []string{"ID", "NAME", "EMAIL", "PHONE", "JOB TITLE", "COMPANY"}

func renderPeople(cmd *cli.Command, v interface{}, people []person.Person) error {
	rows := make([][]string, len(people))
	for i, p := range people {
		rows[i] = []string{p.ID, p.Name(), p.Email, p.Phone, p.JobTitle, p.Company}
	}
	return render(cmd, v, personHeader, rows)
}
