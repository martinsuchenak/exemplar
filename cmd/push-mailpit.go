package cmd

import (
	"context"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/paularlott/cli"

	"github.com/martinsuchenak/exemplar/internal/client"
	"github.com/martinsuchenak/exemplar/internal/fixture"
	"github.com/martinsuchenak/exemplar/internal/mailpit"
)

func init() {
	Register(pushMailpitCmd())
}

// pageSize is how many fixtures push-mailpit lists per request.
const pageSize = 1000

// pushMailpitCmd sends a dataset's .eml fixtures to Mailpit (or any SMTP
// server). It downloads them through the REST API, so the key's access rules
// apply, then delivers the stored bytes over SMTP.
func pushMailpitCmd() *cli.Command {
	return &cli.Command{
		Name:      "push-mailpit",
		Usage:     "Send a dataset's emails to Mailpit over SMTP, tagged with a run ID",
		Arguments: []cli.Argument{&cli.StringArg{Name: "dataset-id", Usage: "Dataset whose .eml fixtures to send", Required: true}},
		Flags: withClientFlags(
			&cli.StringFlag{
				Name:         "smtp",
				DefaultValue: "localhost:1025",
				Usage:        "Mailpit SMTP address, host:port",
				ConfigPath:   []string{"mailpit.smtp"},
				EnvVars:      []string{"MAILPIT_SMTP"},
			},
			&cli.StringFlag{Name: "smtp-user", Usage: "SMTP username, if Mailpit requires auth", EnvVars: []string{"MAILPIT_SMTP_USER"}},
			&cli.StringFlag{Name: "smtp-password", Usage: "SMTP password (prefer MAILPIT_SMTP_PASSWORD)", EnvVars: []string{"MAILPIT_SMTP_PASSWORD"}},
			&cli.StringFlag{Name: "run", Usage: "Run ID to tag messages with (default: a new one, printed)"},
			&cli.StringFlag{Name: "to", Usage: "Deliver every message to these comma-separated addresses instead of its own recipients"},
			&cli.StringFlag{Name: "scenario", Usage: "Only fixtures with this scenario"},
			&cli.StringFlag{Name: "tag", Usage: "Only fixtures with this tag"},
			&cli.IntFlag{Name: "limit", Usage: "Send at most this many messages (default all)"},
			&cli.BoolFlag{Name: "raw", Usage: "Send the stored bytes unchanged, without X-Tags and X-Exemplar-* headers"},
			&cli.BoolFlag{Name: "dry-run", Usage: "List what would be sent without sending"},
		),
		Run: func(ctx context.Context, cmd *cli.Command) error {
			id, err := requireArg(cmd, "dataset-id")
			if err != nil {
				return err
			}
			c, err := newClient(cmd)
			if err != nil {
				return err
			}
			run := cmd.GetString("run")
			if run == "" {
				run = mailpit.NewRunID(time.Now())
			}
			if !mailpit.ValidRun(run) {
				return fmt.Errorf("run ID %q must be 1-64 letters, digits, '.', '_' or '-'", run)
			}
			emails, skipped, err := listEmails(ctx, c, id, cmd.GetString("scenario"), cmd.GetString("tag"), cmd.GetInt("limit"))
			if err != nil {
				return err
			}
			if skipped > 0 {
				_, _ = fmt.Fprintf(stderr, "Skipping %d real-derived emails that are not approved; they stay in quarantine.\n", skipped)
			}
			if len(emails) == 0 {
				return fmt.Errorf("dataset %s has no matching .eml fixtures that may be sent", id)
			}
			opts := mailpit.Options{
				Addr:     cmd.GetString("smtp"),
				Run:      run,
				To:       splitList(cmd.GetString("to")),
				Raw:      cmd.GetBool("raw"),
				Username: cmd.GetString("smtp-user"),
				Password: cmd.GetString("smtp-password"),
			}

			msgs := make([]mailpit.Message, 0, len(emails))
			for _, f := range emails {
				data, err := download(ctx, c, f.ID)
				if err != nil {
					return fmt.Errorf("downloading %s: %w", f.Name, err)
				}
				msgs = append(msgs, mailpit.Message{FixtureID: f.ID, Name: f.Name, Data: data})
			}

			var results []mailpit.Result
			if cmd.GetBool("dry-run") {
				for _, m := range msgs {
					from, to, _, err := mailpit.Prepare(m, opts)
					r := mailpit.Result{FixtureID: m.FixtureID, Name: m.Name, From: from, To: to}
					if err != nil {
						r.Error = err.Error()
					}
					results = append(results, r)
				}
			} else {
				results, err = mailpit.Push(ctx, opts, msgs)
				if err != nil {
					return err
				}
			}
			return reportPush(cmd, results, opts, cmd.GetBool("dry-run"))
		},
	}
}

// listEmails pages through a dataset's .eml fixtures matching the filters.
// Real-derived emails that are not approved are left out and counted: an
// admin key may read them to review them, but sending them to Mailpit would
// take them out of quarantine.
func listEmails(ctx context.Context, c *client.Client, datasetID, scenario, tag string, limit int) (out []fixture.Fixture, skipped int, err error) {
	for offset := 0; ; offset += pageSize {
		page, err := c.ListFixtures(ctx, datasetID, fixture.ListFilter{
			Kind: fixture.KindEmail, Format: "eml", Scenario: scenario, Tag: tag, Limit: pageSize, Offset: offset,
		})
		if err != nil {
			return nil, 0, err
		}
		for _, f := range page {
			if !f.Shareable() {
				skipped++
				continue
			}
			out = append(out, f)
		}
		if limit > 0 && len(out) >= limit {
			return out[:limit], skipped, nil
		}
		if len(page) < pageSize {
			return out, skipped, nil
		}
	}
}

func download(ctx context.Context, c *client.Client, id string) ([]byte, error) {
	body, err := c.Download(ctx, id)
	if err != nil {
		return nil, err
	}
	defer func() { _ = body.Close() }()
	return io.ReadAll(body)
}

// reportPush prints the results and fails if any message failed.
func reportPush(cmd *cli.Command, results []mailpit.Result, opts mailpit.Options, dryRun bool) error {
	failed := 0
	rows := make([][]string, len(results))
	for i, r := range results {
		status := "sent"
		if dryRun {
			status = "would send"
		}
		if r.Error != "" {
			status, failed = "failed: "+r.Error, failed+1
		}
		rows[i] = []string{r.Name, r.From, strings.Join(r.To, ", "), status}
	}
	if err := render(cmd, map[string]any{"run": opts.Run, "tag": mailpit.Tag(opts.Run), "smtp": opts.Addr, "dry_run": dryRun, "results": results},
		[]string{"FIXTURE", "FROM", "TO", "STATUS"}, rows); err != nil {
		return err
	}
	if !cmd.GetBool("json") {
		verb := "Pushed"
		if dryRun {
			verb = "Would push"
		}
		_, _ = fmt.Fprintf(stdout, "\n%s %d of %d emails to %s. Run %s", verb, len(results)-failed, len(results), opts.Addr, opts.Run)
		if !opts.Raw {
			_, _ = fmt.Fprintf(stdout, " (Mailpit tag %s)", mailpit.Tag(opts.Run))
		}
		_, _ = fmt.Fprintln(stdout, ".")
	}
	if failed > 0 {
		return fmt.Errorf("%d of %d emails failed", failed, len(results))
	}
	return nil
}
