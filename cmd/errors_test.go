package cmd

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	"github.com/paularlott/cli"
)

func TestReportError(t *testing.T) {
	root := &cli.Command{Name: "exemplar", Commands: []*cli.Command{
		{Name: "datasets", Commands: []*cli.Command{{Name: "create"}, {Name: "list", Aliases: []string{"ls"}}}},
		{Name: "promote"},
	}}
	tests := []struct {
		args []string
		err  string
		want string
	}{
		// A wrong command line points at the failing command's help.
		{[]string{"promote"}, "missing required argument: dataset-id",
			"exemplar: missing required argument: dataset-id\nRun 'exemplar promote --help' for usage.\n"},
		{[]string{"--log-level", "debug", "datasets", "create", "--bogus"}, "unknown flag: --bogus",
			"exemplar: unknown flag: --bogus\nRun 'exemplar datasets create --help' for usage.\n"},
		{[]string{"datasets", "ls", "x"}, "missing <dataset-id>",
			"exemplar: missing <dataset-id>\nRun 'exemplar datasets list --help' for usage.\n"},
		{[]string{"datasets", "create", "--count", "x"}, "invalid integer value for flag --count: x",
			"exemplar: invalid integer value for flag --count: x\nRun 'exemplar datasets create --help' for usage.\n"},
		// The parser's own hint is not repeated.
		{[]string{"datsets"}, "unknown command \"datsets\"\n\nRun 'exemplar --help' for usage",
			"exemplar: unknown command \"datsets\"\n\nRun 'exemplar --help' for usage\n"},
		// Any other error is just the message: no log timestamp or level.
		{[]string{"promote", "x"}, "dataset x: not found", "exemplar: dataset x: not found\n"},
	}
	for _, tt := range tests {
		var b bytes.Buffer
		if code := ReportError(&b, root, tt.args, errors.New(tt.err)); code != 1 {
			t.Errorf("%v: exit code %d", tt.args, code)
		}
		if b.String() != tt.want {
			t.Errorf("%v:\n got %q\nwant %q", tt.args, b.String(), tt.want)
		}
		if strings.Contains(b.String(), "ERR") || strings.Contains(b.String(), "application error") {
			t.Errorf("%v: log formatting in %q", tt.args, b.String())
		}
	}
}

// TestCommandErrorsReachReportError checks a real parser error from the
// registered commands reads as ReportError expects.
func TestCommandErrorsReachReportError(t *testing.T) {
	_, err := runCLI(t, "promote")
	if err == nil {
		t.Fatal("promote without a dataset id succeeded")
	}
	var b bytes.Buffer
	ReportError(&b, &cli.Command{Name: "exemplar", Commands: Commands()}, []string{"promote"}, err)
	if !strings.Contains(b.String(), "Run 'exemplar promote --help' for usage.") {
		t.Errorf("output = %q", b.String())
	}
}
