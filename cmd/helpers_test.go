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
	oldArgs, oldOut := os.Args, stdout
	var out bytes.Buffer
	os.Args = append([]string{"exemplar"}, args...)
	stdout = &out
	t.Cleanup(func() { os.Args, stdout = oldArgs, oldOut })

	// Fresh command values each run: cli keeps parsed flag state on them.
	root := &cli.Command{Name: "exemplar", Commands: []*cli.Command{
		bootstrapCmd(), completionCmd(), initCmd(), serveCmd(),
	}}
	err := root.Execute(context.Background())
	return out.String(), err
}
