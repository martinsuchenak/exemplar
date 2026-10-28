// Command exemplar serves test fixtures over REST, MCP and a CLI.
package main

import (
	"context"
	"os"

	"github.com/paularlott/cli"
	"github.com/paularlott/cli/env"
	cli_toml "github.com/paularlott/cli/toml"

	"github.com/martinsuchenak/exemplar/build"
	"github.com/martinsuchenak/exemplar/cmd"
)

var configFile = "exemplar-config.toml"

func main() {
	_ = env.Load()

	app := &cli.Command{
		Name:       "exemplar",
		Usage:      "exemplar service",
		Version:    build.String(),
		ConfigFile: cli_toml.NewConfigFile(&configFile, nil),
		Flags: []cli.Flag{
			&cli.StringFlag{
				Name:         "config",
				DefaultValue: "exemplar-config.toml",
				Usage:        "Path to configuration file",
				EnvVars:      []string{"CONFIG_FILE"},
				AssignTo:     &configFile,
				Global:       true,
			},
			&cli.StringFlag{
				Name:         "log-level",
				DefaultValue: "info",
				Usage:        "Log level (debug, info, warn, error)",
				EnvVars:      []string{"LOG_LEVEL"},
				ConfigPath:   []string{"log.level"},
				Global:       true,
			},
			&cli.StringFlag{
				Name:         "log-format",
				DefaultValue: "text",
				Usage:        "Log format (text, json)",
				EnvVars:      []string{"LOG_FORMAT"},
				ConfigPath:   []string{"log.format"},
				Global:       true,
			},
		},
		Commands: cmd.Commands(),
	}

	if err := app.Execute(context.Background()); err != nil {
		// A plain line on stderr, not a log entry: the person at the
		// terminal reads it. The server logs its own errors.
		os.Exit(cmd.ReportError(os.Stderr, app, os.Args[1:], err))
	}
}
