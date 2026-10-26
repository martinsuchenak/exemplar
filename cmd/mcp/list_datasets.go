package mcp

import (
	"context"

	mcplib "github.com/paularlott/mcp"

	"github.com/martinsuchenak/exemplar/internal/dataset"
)

func init() {
	RegisterTool(registerListDatasetsTool)
}

func registerListDatasetsTool(server *mcplib.Server) {
	datasets := services.Datasets
	server.RegisterTool(
		mcplib.NewTool("list_datasets",
			"List the test-data datasets this key can read: the global library, your workspaces and, for admins, quarantine. "+
				"Use a dataset's id with search_fixtures.",
			mcplib.String("scope", "Only this scope: global, workspace or quarantine"),
			mcplib.String("name", "Only datasets with exactly this name (all versions)"),
		),
		func(ctx context.Context, req *mcplib.ToolRequest) (*mcplib.ToolResponse, error) {
			p, err := caller(ctx)
			if err != nil {
				return nil, err
			}
			list, err := datasets.List(ctx, p, dataset.ListFilter{
				Scope: dataset.Scope(req.StringOr("scope", "")),
				Name:  req.StringOr("name", ""),
			})
			if err != nil {
				return nil, toolError(err)
			}
			return mcplib.NewToolResponseStructured(map[string]any{"datasets": list, "count": len(list)}), nil
		},
	)
}
