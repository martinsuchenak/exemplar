// Package mcp is the MCP endpoint; serve mounts Handler at /mcp on the same
// listener as the REST API. Tools register themselves in init and,
// like every entry point, call services rather than storage. Every request
// needs an API key (Authorization: Bearer exm_… or X-API-Key), and the tools
// act as that key, so MCP sees exactly what the key may see over REST.
// The tools are read-only.
package mcp

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	mcplib "github.com/paularlott/mcp"

	"github.com/martinsuchenak/exemplar/build"
	"github.com/martinsuchenak/exemplar/internal/app"
	"github.com/martinsuchenak/exemplar/internal/apperr"
	"github.com/martinsuchenak/exemplar/internal/auth"
	"github.com/martinsuchenak/exemplar/internal/principal"
)

var toolRegistrations []func(*mcplib.Server)

// services is set by NewServer before tools register, so tool files keep the
// scaffolder's func(*mcplib.Server) signature. Tools read it while
// registering, not while running.
var services *app.App

// RegisterTool adds a tool registration; tool files call it from init.
func RegisterTool(fn func(*mcplib.Server)) {
	toolRegistrations = append(toolRegistrations, fn)
}

// NewServer builds the MCP server with every registered tool backed by a.
func NewServer(a *app.App) *mcplib.Server {
	services = a
	server := mcplib.NewServer("exemplar-mcp", build.Version)
	for _, fn := range toolRegistrations {
		fn(server)
	}
	return server
}

// Handler serves the MCP endpoint behind API key authentication.
func Handler(a *app.App) http.Handler {
	server := NewServer(a)
	return auth.APIKeyMiddleware(a.Accounts)(http.HandlerFunc(server.HandleRequest))
}

// caller returns the principal the authentication middleware stored.
func caller(ctx context.Context) (principal.Principal, error) {
	p, ok := principal.FromContext(ctx)
	if !ok {
		return principal.Principal{}, mcplib.NewToolError(errorCodeUnauthenticated, "unauthorized", nil)
	}
	return p, nil
}

// MCP error codes for service errors, in the implementation-defined range.
const (
	errorCodeNotFound        = -32001
	errorCodeForbidden       = -32002
	errorCodeConflict        = -32003
	errorCodeUnauthenticated = -32004
)

// toolError turns a service error into an MCP error the client sees. Known
// kinds keep their message; anything else becomes a generic internal error,
// as over REST, so paths and SQL never leak.
func toolError(err error) error {
	switch {
	case errors.Is(err, apperr.ErrInvalid):
		return mcplib.NewToolErrorInvalidParams(err.Error())
	case errors.Is(err, apperr.ErrNotFound):
		return mcplib.NewToolError(errorCodeNotFound, err.Error(), nil)
	case errors.Is(err, apperr.ErrForbidden):
		return mcplib.NewToolError(errorCodeForbidden, err.Error(), nil)
	case errors.Is(err, apperr.ErrConflict):
		return mcplib.NewToolError(errorCodeConflict, err.Error(), nil)
	case errors.Is(err, apperr.ErrUnauthenticated):
		return mcplib.NewToolError(errorCodeUnauthenticated, "unauthorized", nil)
	default:
		return mcplib.NewToolErrorInternal("internal error")
	}
}

// required reads a required string argument.
func required(req *mcplib.ToolRequest, name string) (string, error) {
	v, err := req.String(name)
	if err != nil || v == "" {
		return "", mcplib.NewToolErrorInvalidParams(fmt.Sprintf("%s is required", name))
	}
	return v, nil
}
