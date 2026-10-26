package cmd

import (
	"context"
	"fmt"
	"os"

	"github.com/paularlott/cli"
	logslog "github.com/paularlott/logger/slog"
)

func init() {
	Register(initCmd())
}

func initCmd() *cli.Command {
	return &cli.Command{
		Name:  "init",
		Usage: "Initialize a default configuration file",
		Run: func(ctx context.Context, cmd *cli.Command) error {
			log := logslog.New(logslog.Config{
				Level:  "info",
				Format: "console",
				Writer: os.Stdout,
			})
			configPath := "exemplar-config.toml"

			if _, err := os.Stat(configPath); err == nil {
				log.Warn("config file already exists", "path", configPath)
				return fmt.Errorf("config file %s already exists", configPath)
			}

			defaultConfig := `[log]
level = "info"
format = "text"

[server]
host = "0.0.0.0"
port = 8080

[database]
path = "data/exemplar.db"

[storage]
blob_dir = "data/blobs"
max_fixture_mb = 100

[client]
server = "http://localhost:8080"
`
			if err := os.WriteFile(configPath, []byte(defaultConfig), 0644); err != nil {
				return fmt.Errorf("writing config file: %w", err)
			}

			log.Info("config file created", "path", configPath)
			return nil
		},
	}
}
