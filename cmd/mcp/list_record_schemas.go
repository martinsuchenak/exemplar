package mcp

import (
	"context"

	mcplib "github.com/paularlott/mcp"
)

func init() {
	RegisterTool(registerListRecordSchemasTool)
}

func registerListRecordSchemasTool(server *mcplib.Server) {
	generate := services.Generate
	server.RegisterTool(
		mcplib.NewTool("list_record_schemas",
			"List the record schemas (contact, company, project, product, quote, task…) with their fields. "+
				"A record fixture's record_schema is one of these names; search_fixtures takes it as record_schema.",
		),
		func(ctx context.Context, req *mcplib.ToolRequest) (*mcplib.ToolResponse, error) {
			p, err := caller(ctx)
			if err != nil {
				return nil, err
			}
			schemas, err := generate.Schemas(p)
			if err != nil {
				return nil, toolError(err)
			}
			return mcplib.NewToolResponseStructured(map[string]any{"schemas": schemas, "count": len(schemas)}), nil
		},
	)
}
