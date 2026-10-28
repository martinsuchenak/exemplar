package cmd

import (
	"bytes"
	"context"
	"os"
	"strings"
	"testing"

	"github.com/paularlott/cli"
)

// runCLI executes the registered commands with args, as the binary would,
// and returns what they wrote to stdout.
func runCLI(t *testing.T, args ...string) (string, error) {
	t.Helper()
	out, _, err := runCLIWith(t, "", args...)
	return out, err
}

// runCLIWith is runCLI with input on stdin, returning stderr too.
func runCLIWith(t *testing.T, input string, args ...string) (string, string, error) {
	t.Helper()
	oldArgs, oldOut, oldErr, oldIn := os.Args, stdout, stderr, stdin
	var out, errOut bytes.Buffer
	os.Args = append([]string{"exemplar"}, args...)
	stdout, stderr, stdin = &out, &errOut, strings.NewReader(input)
	t.Cleanup(func() { os.Args, stdout, stderr, stdin = oldArgs, oldOut, oldErr, oldIn })

	// Fresh command values each run: cli keeps parsed flag state on them.
	root := &cli.Command{Name: "exemplar", Commands: []*cli.Command{
		bootstrapCmd(), completionCmd(), initCmd(), serveCmd(),
		accountsCmd(), keysCmd(), datasetsCmd(), fixturesCmd(),
		uploadCmd(), forkCmd(), promoteCmd(), purgeCmd(),
		generateCmd(), personsCmd(), pushMailpitCmd(),
		ingestCmd(), approveCmd(), rejectCmd(), schemasCmd(),
		exportCmd(), importCmd(), backupCmd(), restoreCmd(), anonymiseCmd(),
		reviewWalkCmd(),
	}}
	err := root.Execute(context.Background())
	return out.String(), errOut.String(), err
}
