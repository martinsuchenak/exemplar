package cmd

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"time"

	"github.com/paularlott/cli"
	logslog "github.com/paularlott/logger/slog"

	"github.com/martinsuchenak/exemplar/cmd/routes"

	mcpserver "github.com/martinsuchenak/exemplar/cmd/mcp"
	// go-scaffolder:serve-imports
)

func init() {
	Register(serveCmd())
}

func serveCmd() *cli.Command {
	return &cli.Command{
		Name:  "serve",
		Usage: "Start the exemplar service",
		Flags: []cli.Flag{
			&cli.StringFlag{
				Name:         "server-host",
				DefaultValue: "0.0.0.0",
				Usage:        "Server listen host",
				ConfigPath:   []string{"server.host"},
				EnvVars:      []string{"SERVER_HOST"},
			},
			&cli.IntFlag{
				Name:         "server-port",
				DefaultValue: 8080,
				Usage:        "Server listen port",
				ConfigPath:   []string{"server.port"},
				EnvVars:      []string{"SERVER_PORT"},
			},
		},
		// go-scaffolder:serve-flags
		Run: func(ctx context.Context, cmd *cli.Command) error {
			log := logslog.New(logslog.Config{
				Level:  cmd.GetString("log-level"),
				Format: cmd.GetString("log-format"),
				Writer: os.Stdout,
			})
			log.Info("starting exemplar service")

			mcpserver.StartMCPServer(log)
			// go-scaffolder:serve-init

			mux := http.NewServeMux()
			routes.RegisterRoutes(mux)

			addr := fmt.Sprintf("%s:%d", cmd.GetString("server-host"), cmd.GetInt("server-port"))
			log.Info("starting HTTP server", "addr", addr)
			// go-scaffolder:serve-start
			srv := &http.Server{
				Addr:              addr,
				Handler:           mux,
				ReadHeaderTimeout: 10 * time.Second,
				ReadTimeout:       30 * time.Second,
				WriteTimeout:      30 * time.Second,
				IdleTimeout:       120 * time.Second,
			}
			return srv.ListenAndServe()
		},
	}
}
