package mcp

import (
	"context"

	mcplib "github.com/paularlott/mcp"

	"github.com/martinsuchenak/exemplar/internal/fixture"
)

func init() {
	RegisterTool(registerSearchFixturesTool)
}

func registerSearchFixturesTool(server *mcplib.Server) {
	fixtures := services.Fixtures
	server.RegisterTool(
		mcplib.NewTool("search_fixtures",
			"Search a dataset's fixtures (emails, files and JSON records) by kind, format, scenario, tag, record schema or the person they mention. "+
				"Returns metadata only, ordered by name; use get_fixture for content.",
			mcplib.String("dataset_id", "Dataset to search (from list_datasets)", mcplib.Required()),
			mcplib.String("kind", "email, file or record"),
			mcplib.String("format", "File format, e.g. eml, pdf, json"),
			mcplib.String("scenario", "Scenario label, e.g. reply or bounce for generated emails"),
			mcplib.String("tag", "Tag"),
			mcplib.String("record_schema", "Records of one schema: contact, company, project, opportunity, address"),
			mcplib.String("person_id", "Fixtures that mention this person (see get_person)"),
			mcplib.String("review_state", "not-required, unreviewed, approved or rejected"),
			mcplib.Integer("limit", "Maximum results (default 100, at most 1000)"),
			mcplib.Integer("offset", "Results to skip, for paging"),
		),
		func(ctx context.Context, req *mcplib.ToolRequest) (*mcplib.ToolResponse, error) {
			p, err := caller(ctx)
			if err != nil {
				return nil, err
			}
			datasetID, err := required(req, "dataset_id")
			if err != nil {
				return nil, err
			}
			list, err := fixtures.List(ctx, p, datasetID, fixture.ListFilter{
				Kind:         fixture.Kind(req.StringOr("kind", "")),
				Format:       req.StringOr("format", ""),
				Scenario:     req.StringOr("scenario", ""),
				Tag:          req.StringOr("tag", ""),
				RecordSchema: req.StringOr("record_schema", ""),
				PersonID:     req.StringOr("person_id", ""),
				ReviewState:  fixture.ReviewState(req.StringOr("review_state", "")),
				Limit:        req.IntOr("limit", 0),
				Offset:       req.IntOr("offset", 0),
			})
			if err != nil {
				return nil, toolError(err)
			}
			return mcplib.NewToolResponseStructured(map[string]any{"fixtures": list, "count": len(list)}), nil
		},
	)
}
