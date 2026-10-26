// Package cmd defines the CLI commands. Commands register themselves in init
// and call services from internal/app; they never touch storage directly.
package cmd

import (
	"io"
	"os"

	"github.com/paularlott/cli"
)

// stdout receives command output meant for the user and stderr per-item
// problems; tests replace both.
var (
	stdout io.Writer = os.Stdout
	stderr io.Writer = os.Stderr
)

var registry []*cli.Command

// Register adds a command; command files call it from init.
func Register(c *cli.Command) {
	registry = append(registry, c)
}

// Commands returns every registered command for the root command.
func Commands() []*cli.Command {
	return registry
}
