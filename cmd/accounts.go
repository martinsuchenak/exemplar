package cmd

import (
	"context"
	"fmt"

	"github.com/paularlott/cli"

	"github.com/martinsuchenak/exemplar/internal/account"
	"github.com/martinsuchenak/exemplar/internal/principal"
)

func init() {
	Register(accountsCmd())
}

// accountsCmd manages accounts over the REST API. Creating and disabling
// accounts needs an admin key; the server enforces it.
func accountsCmd() *cli.Command {
	return &cli.Command{
		Name:  "accounts",
		Usage: "List, create and disable accounts",
		Commands: []*cli.Command{
			{
				Name:  "list",
				Usage: "List accounts (all for admins, your own otherwise)",
				Flags: withClientFlags(),
				Run: func(ctx context.Context, cmd *cli.Command) error {
					c, err := newClient(cmd)
					if err != nil {
						return err
					}
					accounts, err := c.ListAccounts(ctx)
					if err != nil {
						return err
					}
					rows := make([][]string, len(accounts))
					for i, a := range accounts {
						rows[i] = accountRow(a)
					}
					return render(cmd, accounts, accountHeader, rows)
				},
			},
			{
				Name:  "create",
				Usage: "Create an account (admin key)",
				Flags: withClientFlags(
					&cli.StringFlag{Name: "name", Usage: "Account name", Required: true},
					&cli.StringFlag{Name: "kind", DefaultValue: string(account.KindDeveloper), Usage: "developer, team or app"},
					&cli.StringFlag{Name: "role", DefaultValue: string(principal.RoleMember), Usage: "member or admin"},
				),
				Run: func(ctx context.Context, cmd *cli.Command) error {
					c, err := newClient(cmd)
					if err != nil {
						return err
					}
					acc, err := c.CreateAccount(ctx, account.CreateAccountInput{
						Name: cmd.GetString("name"),
						Kind: account.Kind(cmd.GetString("kind")),
						Role: principal.Role(cmd.GetString("role")),
					})
					if err != nil {
						return err
					}
					return render(cmd, acc, accountHeader, [][]string{accountRow(*acc)})
				},
			},
			{
				Name:      "disable",
				Usage:     "Disable an account and revoke its keys (admin key)",
				Arguments: []cli.Argument{&cli.StringArg{Name: "account-id", Usage: "Account to disable", Required: true}},
				Flags:     withClientFlags(),
				Run: func(ctx context.Context, cmd *cli.Command) error {
					id, err := requireArg(cmd, "account-id")
					if err != nil {
						return err
					}
					c, err := newClient(cmd)
					if err != nil {
						return err
					}
					if err := c.DisableAccount(ctx, id); err != nil {
						return err
					}
					_, _ = fmt.Fprintf(stdout, "Disabled account %s and revoked its keys.\n", id)
					return nil
				},
			},
		},
	}
}

var accountHeader = []string{"ID", "NAME", "KIND", "ROLE", "DISABLED"}

func accountRow(a account.Account) []string {
	return []string{a.ID, a.Name, string(a.Kind), string(a.Role), formatTime(a.DisabledAt)}
}
