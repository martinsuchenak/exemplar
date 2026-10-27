package cmd

import (
	"fmt"
	"io"
	"regexp"
	"strings"

	"github.com/paularlott/cli"
)

// usageErrorRe matches the errors that mean the command line itself was
// wrong, from the cli package's parser and from requireArg.
var usageErrorRe = regexp.MustCompile(`^(missing required argument|missing <|unknown flag|required flag|flag --\S+ requires a value|invalid \S+ value for (flag|argument))`)

// ReportError prints a command's error for the person who ran it: one plain
// "exemplar: …" line on w, without the log timestamp and level the server's
// logger adds. When the command line was wrong it points at the help of the
// command that failed. It returns the exit code.
func ReportError(w io.Writer, root *cli.Command, args []string, err error) int {
	msg := err.Error()
	_, _ = fmt.Fprintf(w, "%s: %s\n", root.Name, msg)
	// The parser's unknown-command error already says where to look.
	if usageErrorRe.MatchString(msg) && !strings.Contains(msg, "--help") {
		_, _ = fmt.Fprintf(w, "Run '%s --help' for usage.\n", commandPath(root, args))
	}
	return 1
}

// commandPath returns the command args name, such as "exemplar datasets
// create": the subcommands named in order. Flags, their values and other
// arguments are skipped, since which flags take a value is not known here.
func commandPath(root *cli.Command, args []string) string {
	path := []string{root.Name}
	cur := root
	for _, a := range args {
		if strings.HasPrefix(a, "-") {
			continue
		}
		if next := subcommand(cur, a); next != nil {
			path, cur = append(path, next.Name), next
		}
	}
	return strings.Join(path, " ")
}

func subcommand(c *cli.Command, name string) *cli.Command {
	for _, s := range c.Commands {
		if s.Name == name {
			return s
		}
		for _, alias := range s.Aliases {
			if alias == name {
				return s
			}
		}
	}
	return nil
}
