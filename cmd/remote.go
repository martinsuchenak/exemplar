package cmd

import (
	"encoding/json"
	"fmt"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/paularlott/cli"

	"github.com/martinsuchenak/exemplar/internal/client"
)

// clientFlags are shared by every command that talks to a server over the
// REST API. The key is read from the flag or environment only, never from the
// config file, so it does not end up in a file that gets committed.
func clientFlags() []cli.Flag {
	return []cli.Flag{
		&cli.StringFlag{
			Name:         "server",
			DefaultValue: "http://localhost:8080",
			Usage:        "exemplar server URL",
			ConfigPath:   []string{"client.server"},
			EnvVars:      []string{"EXEMPLAR_SERVER"},
		},
		&cli.StringFlag{
			Name:    "api-key",
			Usage:   "API key (prefer the EXEMPLAR_API_KEY environment variable)",
			EnvVars: []string{"EXEMPLAR_API_KEY"},
		},
		&cli.BoolFlag{
			Name:  "json",
			Usage: "Print JSON instead of a table",
		},
	}
}

// withClientFlags appends the client flags to a command's own flags.
func withClientFlags(flags ...cli.Flag) []cli.Flag {
	return append(flags, clientFlags()...)
}

func newClient(cmd *cli.Command) (*client.Client, error) {
	return client.New(cmd.GetString("server"), cmd.GetString("api-key"))
}

// render prints v as JSON with --json, otherwise as a table of rows.
func render(cmd *cli.Command, v interface{}, header []string, rows [][]string) error {
	if cmd.GetBool("json") {
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(v)
	}
	tw := tabwriter.NewWriter(stdout, 0, 0, 2, ' ', 0)
	_, _ = fmt.Fprintln(tw, strings.Join(header, "\t"))
	for _, r := range rows {
		_, _ = fmt.Fprintln(tw, strings.Join(r, "\t"))
	}
	return tw.Flush()
}

// splitList splits a comma-separated flag value, dropping empty items.
func splitList(s string) []string {
	var out []string
	for _, item := range strings.Split(s, ",") {
		if item = strings.TrimSpace(item); item != "" {
			out = append(out, item)
		}
	}
	return out
}

// requireArg returns the named positional argument or an error naming it.
func requireArg(cmd *cli.Command, name string) (string, error) {
	v := cmd.GetStringArg(name)
	if v == "" {
		return "", fmt.Errorf("missing <%s>", name)
	}
	return v, nil
}

func formatTime(t *time.Time) string {
	if t == nil || t.IsZero() {
		return "-"
	}
	return t.Local().Format("2006-01-02 15:04")
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}
