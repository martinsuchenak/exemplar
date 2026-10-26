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
		Flags: append(append(storageFlags(), serverFlags()...),
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
			&cli.StringFlag{
				Name:         "mcp-host",
				DefaultValue: "0.0.0.0",
				Usage:        "MCP server listen host",
				ConfigPath:   []string{"mcp.host"},
				EnvVars:      []string{"MCP_HOST"},
			},
			&cli.IntFlag{
				Name:         "mcp-port",
				DefaultValue: 9000,
				Usage:        "MCP server listen port (0 turns MCP off)",
				ConfigPath:   []string{"mcp.port"},
				EnvVars:      []string{"MCP_PORT"},
			},
		),
		// go-scaffolder:serve-flags
		Run: func(ctx context.Context, cmd *cli.Command) error {
			log := logslog.New(logslog.Config{
				Level:  cmd.GetString("log-level"),
				Format: cmd.GetString("log-format"),
				Writer: os.Stdout,
			})
			log.Info("starting exemplar service")

			services, err := openServerApp(ctx, cmd, log)
			if err != nil {
				return err
			}
			defer func() { _ = services.Close() }()

			if port := cmd.GetInt("mcp-port"); port != 0 {
				mcpserver.StartMCPServer(log, services, fmt.Sprintf("%s:%d", cmd.GetString("mcp-host"), port))
			}
			// go-scaffolder:serve-init

			mux := http.NewServeMux()
			routes.RegisterRoutes(mux, services)

			addr := fmt.Sprintf("%s:%d", cmd.GetString("server-host"), cmd.GetInt("server-port"))
			log.Info("starting HTTP server", "addr", addr)
			// go-scaffolder:serve-start
			srv := newHTTPServer(addr, mux)
			return srv.ListenAndServe()
		},
	}
}

// newHTTPServer applies the timeouts every exemplar listener uses, so slow
// clients cannot hold connections open indefinitely. Body timeouts allow
// fixture uploads and downloads of the default 100 MiB limit on slow links.
func newHTTPServer(addr string, handler http.Handler) *http.Server {
	return &http.Server{
		Addr:              addr,
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       5 * time.Minute,
		WriteTimeout:      5 * time.Minute,
		IdleTimeout:       120 * time.Second,
	}
}
