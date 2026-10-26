package mcp

import (
	"net/http"
	"time"

	"github.com/paularlott/logger"
	mcplib "github.com/paularlott/mcp"

	"github.com/martinsuchenak/exemplar/build"
)

var toolRegistrations []func(*mcplib.Server)

func RegisterTool(fn func(*mcplib.Server)) {
	toolRegistrations = append(toolRegistrations, fn)
}

func StartMCPServer(log logger.Logger) {
	server := mcplib.NewServer("exemplar-mcp", build.Version)

	for _, fn := range toolRegistrations {
		fn(server)
	}

	go func() {
		log.Info("starting MCP server on :9000")
		mux := http.NewServeMux()
		mux.HandleFunc("/mcp", server.HandleRequest)
		srv := &http.Server{
			Addr:              ":9000",
			Handler:           mux,
			ReadHeaderTimeout: 10 * time.Second,
			IdleTimeout:       120 * time.Second,
		}
		if err := srv.ListenAndServe(); err != nil {
			log.Error("MCP server error", "error", err)
		}
	}()
}
