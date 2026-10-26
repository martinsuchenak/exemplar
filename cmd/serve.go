package cmd

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/paularlott/cli"
	"github.com/paularlott/logger"
	logslog "github.com/paularlott/logger/slog"

	"github.com/martinsuchenak/exemplar/cmd/routes"
	"github.com/martinsuchenak/exemplar/internal/app"

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
			&cli.BoolFlag{
				Name:         "mcp",
				DefaultValue: true,
				Usage:        "Serve the read-only MCP endpoint at /mcp (--mcp=false turns it off)",
				ConfigPath:   []string{"mcp.enabled"},
				EnvVars:      []string{"MCP_ENABLED"},
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

			// go-scaffolder:serve-init

			mux := newMux(services, cmd.GetBool("mcp"))

			addr := fmt.Sprintf("%s:%d", cmd.GetString("server-host"), cmd.GetInt("server-port"))
			// go-scaffolder:serve-start
			servers := []namedServer{{"HTTP", newHTTPServer(addr, mux)}}
			// SIGTERM (docker stop) and Ctrl-C shut down gracefully: requests in
			// flight finish and the database is closed.
			ctx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
			defer stop()
			return runServers(ctx, log, servers, shutdownGrace)
		},
	}
}

// newMux serves the REST API and, with withMCP, the MCP endpoint at /mcp, on
// one listener: both use the same API keys and services.
func newMux(services *app.App, withMCP bool) *http.ServeMux {
	mux := http.NewServeMux()
	routes.RegisterRoutes(mux, services)
	if withMCP {
		mux.Handle("/mcp", mcpserver.Handler(services))
	}
	return mux
}

// shutdownGrace is how long requests in flight get to finish on shutdown.
const shutdownGrace = 30 * time.Second

// namedServer is a listener serve runs, named for logs and errors.
type namedServer struct {
	name string
	srv  *http.Server
}

// runServers runs every server until ctx ends (a signal) or one of them fails,
// for example because its port is taken; then it shuts all of them down,
// letting requests in flight finish for up to grace. A failure is returned,
// so serve never keeps running without a listener it was asked for. serve
// has one listener today; extra ones (metrics, say) would join it here.
func runServers(ctx context.Context, log logger.Logger, servers []namedServer, grace time.Duration) error {
	errs := make(chan error, len(servers))
	for _, s := range servers {
		go func() {
			log.Info("starting "+s.name+" server", "addr", s.srv.Addr)
			if err := s.srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
				errs <- fmt.Errorf("%s server on %s: %w", s.name, s.srv.Addr, err)
			}
		}()
	}
	var failed error
	select {
	case <-ctx.Done():
		log.Info("shutting down")
	case failed = <-errs:
	}
	shutdown, cancel := context.WithTimeout(context.Background(), grace)
	defer cancel()
	for _, s := range servers {
		if err := s.srv.Shutdown(shutdown); err != nil && failed == nil {
			failed = fmt.Errorf("shutting down the %s server: %w", s.name, err)
		}
	}
	return failed
}

// newHTTPServer applies the timeouts every exemplar listener uses, so slow
// clients cannot hold connections open indefinitely. Handlers that move large
// bodies (fixture content, ingest, export, import) extend their own deadline
// to rest.LongTransfer.
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
