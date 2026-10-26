package cmd

import (
	"bytes"
	"context"
	"os"
	"testing"

	"github.com/paularlott/cli"
)

// runCLI executes the registered commands with args, as the binary would,
// and returns what they wrote to stdout.
func runCLI(t *testing.T, args ...string) (string, error) {
	t.Helper()
	oldArgs, oldOut, oldErr := os.Args, stdout, stderr
	var out, errOut bytes.Buffer
	os.Args = append([]string{"exemplar"}, args...)
	stdout, stderr = &out, &errOut
	t.Cleanup(func() { os.Args, stdout, stderr = oldArgs, oldOut, oldErr })

	// Fresh command values each run: cli keeps parsed flag state on them.
	root := &cli.Command{Name: "exemplar", Commands: []*cli.Command{
		bootstrapCmd(), completionCmd(), initCmd(), serveCmd(),
		accountsCmd(), keysCmd(), datasetsCmd(), fixturesCmd(),
		uploadCmd(), forkCmd(), promoteCmd(), purgeCmd(),
	}}
	err := root.Execute(context.Background())
	return out.String(), err
}
