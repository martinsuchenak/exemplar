package cmd

import (
	"context"
	"fmt"
	"io"
	"os"

	"github.com/paularlott/cli"
	logslog "github.com/paularlott/logger/slog"

	"github.com/martinsuchenak/exemplar/internal/account"
)

func init() {
	Register(bootstrapCmd())
}

// bootstrapCmd creates the first admin account, or with --recover replaces a
// lost admin key. It opens the database directly, so it only works on the host
// or inside the container, never over the network.
func bootstrapCmd() *cli.Command {
	return &cli.Command{
		Name:  "bootstrap",
		Usage: "Create the first admin account and key (run on the host)",
		Flags: append(storageFlags(),
			&cli.StringFlag{
				Name:         "name",
				DefaultValue: "admin",
				Usage:        "Admin account name",
			},
			&cli.StringFlag{
				Name:         "kind",
				DefaultValue: string(account.KindDeveloper),
				Usage:        "Account kind (developer, team, app)",
			},
			&cli.BoolFlag{
				Name:  "recover",
				Usage: "Revoke the named admin's keys and issue a new admin key",
			},
		),
		Run: func(ctx context.Context, cmd *cli.Command) error {
			log := logslog.New(logslog.Config{
				Level:  cmd.GetString("log-level"),
				Format: cmd.GetString("log-format"),
				Writer: os.Stderr,
			})
			services, err := openApp(ctx, cmd, log)
			if err != nil {
				return err
			}
			defer func() { _ = services.Close() }()

			var acc *account.Account
			var issued *account.IssuedKey
			if cmd.GetBool("recover") {
				acc, issued, err = services.Accounts.Recover(ctx, cmd.GetString("name"))
			} else {
				acc, issued, err = services.Accounts.Bootstrap(ctx, account.CreateAccountInput{
					Name: cmd.GetString("name"),
					Kind: account.Kind(cmd.GetString("kind")),
				})
			}
			if err != nil {
				return err
			}
			printIssuedKey(stdout, acc, issued)
			return nil
		},
	}
}

func printIssuedKey(w io.Writer, acc *account.Account, issued *account.IssuedKey) {
	_, _ = fmt.Fprintf(w, "Account: %s (%s, %s)\n", acc.Name, acc.ID, acc.Role)
	_, _ = fmt.Fprintf(w, "Key:     %s (%s)\n", issued.Key.Name, issued.Key.ID)
	_, _ = fmt.Fprintf(w, "Secret:  %s\n\n", issued.Secret)
	_, _ = fmt.Fprintln(w, "The secret is shown only once. Store it in a password manager.")
}
