package cmd

import (
	"context"
	"errors"
	"fmt"

	"github.com/paularlott/cli"

	"github.com/martinsuchenak/exemplar/internal/fixture"
)

func init() {
	Register(approveCmd())
}

// approveCmd approves reviewed real-derived fixtures. Admin key.
func approveCmd() *cli.Command {
	return reviewCmd("approve", "Approve reviewed real-derived fixtures (admin key)", true)
}

// reviewCmd builds approve and reject: both review fixtures one by one and
// report each, failing if any failed.
func reviewCmd(name, usage string, approve bool) *cli.Command {
	return &cli.Command{
		Name:    name,
		Usage:   usage,
		MinArgs: 1,
		MaxArgs: cli.UnlimitedArgs,
		Flags:   withClientFlags(),
		Run: func(ctx context.Context, cmd *cli.Command) error {
			ids := cmd.GetArgs()
			if len(ids) == 0 {
				return errors.New("give at least one fixture id")
			}
			c, err := newClient(cmd)
			if err != nil {
				return err
			}
			var done []fixture.Fixture
			failed := 0
			for _, id := range ids {
				f, err := c.Review(ctx, id, approve)
				if err != nil {
					failed++
					_, _ = fmt.Fprintf(stderr, "%s: %v\n", id, err)
					continue
				}
				done = append(done, *f)
			}
			rows := make([][]string, len(done))
			for i, f := range done {
				rows[i] = fixtureRow(f)
			}
			if err := render(cmd, done, fixtureHeader, rows); err != nil {
				return err
			}
			if failed > 0 {
				return fmt.Errorf("%d of %d fixtures failed", failed, len(ids))
			}
			return nil
		},
	}
}
