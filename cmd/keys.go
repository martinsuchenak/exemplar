package cmd

import (
	"context"
	"fmt"
	"strings"

	"github.com/paularlott/cli"

	"github.com/martinsuchenak/exemplar/internal/account"
	"github.com/martinsuchenak/exemplar/internal/principal"
)

func init() {
	Register(keysCmd())
}

// keysCmd manages API keys over the REST API. Members manage their own keys;
// admin keys can manage anyone's. The server enforces the rules.
func keysCmd() *cli.Command {
	return &cli.Command{
		Name:  "keys",
		Usage: "List, create and revoke API keys",
		Commands: []*cli.Command{
			{
				Name:  "list",
				Usage: "List keys (your own, or --account for admins)",
				Flags: withClientFlags(
					&cli.StringFlag{Name: "account", Usage: "Account ID (admin key)"},
				),
				Run: func(ctx context.Context, cmd *cli.Command) error {
					c, err := newClient(cmd)
					if err != nil {
						return err
					}
					keys, err := c.ListKeys(ctx, cmd.GetString("account"))
					if err != nil {
						return err
					}
					rows := make([][]string, len(keys))
					for i, k := range keys {
						rows[i] = keyRow(k)
					}
					return render(cmd, keys, keyHeader, rows)
				},
			},
			{
				Name:  "create",
				Usage: "Create a key; the secret is printed once",
				Flags: withClientFlags(
					&cli.StringFlag{Name: "name", Usage: "Key name", Required: true},
					&cli.StringFlag{Name: "scopes", DefaultValue: "read,write", Usage: "Comma-separated: read, write, admin"},
					&cli.StringFlag{Name: "account", Usage: "Account ID (default: your own; others need an admin key)"},
				),
				Run: func(ctx context.Context, cmd *cli.Command) error {
					c, err := newClient(cmd)
					if err != nil {
						return err
					}
					var scopes []principal.Scope
					for _, s := range splitList(cmd.GetString("scopes")) {
						scopes = append(scopes, principal.Scope(s))
					}
					issued, err := c.CreateKey(ctx, account.CreateKeyInput{
						AccountID: cmd.GetString("account"),
						Name:      cmd.GetString("name"),
						Scopes:    scopes,
					})
					if err != nil {
						return err
					}
					if cmd.GetBool("json") {
						return render(cmd, issued, nil, nil)
					}
					_, _ = fmt.Fprintf(stdout, "Key:     %s (%s)\n", issued.Key.Name, issued.Key.ID)
					_, _ = fmt.Fprintf(stdout, "Account: %s\n", issued.Key.AccountID)
					_, _ = fmt.Fprintf(stdout, "Scopes:  %s\n", joinScopes(issued.Key.Scopes))
					_, _ = fmt.Fprintf(stdout, "Secret:  %s\n\n", issued.Secret)
					_, _ = fmt.Fprintln(stdout, "The secret is shown only once. Store it in a password manager.")
					return nil
				},
			},
			{
				Name:      "revoke",
				Usage:     "Revoke a key",
				Arguments: []cli.Argument{&cli.StringArg{Name: "key-id", Usage: "Key to revoke", Required: true}},
				Flags:     withClientFlags(),
				Run: func(ctx context.Context, cmd *cli.Command) error {
					id, err := requireArg(cmd, "key-id")
					if err != nil {
						return err
					}
					c, err := newClient(cmd)
					if err != nil {
						return err
					}
					if err := c.RevokeKey(ctx, id); err != nil {
						return err
					}
					_, _ = fmt.Fprintf(stdout, "Revoked key %s.\n", id)
					return nil
				},
			},
		},
	}
}

var keyHeader = []string{"ID", "NAME", "PREFIX", "SCOPES", "LAST USED", "REVOKED"}

func keyRow(k account.APIKey) []string {
	return []string{k.ID, k.Name, k.Prefix, joinScopes(k.Scopes), formatTime(k.LastUsedAt), formatTime(k.RevokedAt)}
}

func joinScopes(scopes []principal.Scope) string {
	parts := make([]string, len(scopes))
	for i, s := range scopes {
		parts[i] = string(s)
	}
	return strings.Join(parts, ",")
}
