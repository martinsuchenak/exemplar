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
			"List the test-data datasets this key can read: the global library and your workspaces. "+
				"Use query to find them by words in their name, description or tags. "+
				"Pass a dataset's name or id to search_fixtures.",
			mcplib.String("scope", "Only this scope: global or workspace"),
			mcplib.String("name", "Only datasets with exactly this name (all versions)"),
			mcplib.String("query", "Words that must each appear in the name, description or a tag, e.g. crm contacts; case is ignored"),
		),
		func(ctx context.Context, req *mcplib.ToolRequest) (*mcplib.ToolResponse, error) {
			p, err := caller(ctx)
			if err != nil {
				return nil, err
			}
			list, err := datasets.List(ctx, p, dataset.ListFilter{
				Scope: dataset.Scope(req.StringOr("scope", "")),
				Name:  req.StringOr("name", ""),
				Query: req.StringOr("query", ""),
			})
			if err != nil {
				return nil, toolError(err)
			}
			return mcplib.NewToolResponseStructured(map[string]any{"datasets": list, "count": len(list)}), nil
		},
	)
}
