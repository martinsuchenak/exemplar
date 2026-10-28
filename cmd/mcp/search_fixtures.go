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
	datasets, fixtures := services.Datasets, services.Fixtures
	server.RegisterTool(
		mcplib.NewTool("search_fixtures",
			"Search a dataset's fixtures (emails, files and JSON records) by words in their name, kind, format, scenario, tag, record schema or the person they mention. "+
				"Returns the dataset searched and fixture metadata, ordered by name; use get_fixture for content.",
			mcplib.String("dataset", "Dataset to search: its id, or its exact name for the newest version (from list_datasets)", mcplib.Required()),
			mcplib.String("query", "Words that must each appear in the fixture name, e.g. reply eml; case is ignored"),
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
			ref, err := required(req, "dataset")
			if err != nil {
				return nil, err
			}
			d, err := datasets.Resolve(ctx, p, ref)
			if err != nil {
				return nil, toolError(err)
			}
			list, err := fixtures.List(ctx, p, d.ID, fixture.ListFilter{
				Kind:         fixture.Kind(req.StringOr("kind", "")),
				Format:       req.StringOr("format", ""),
				Scenario:     req.StringOr("scenario", ""),
				Tag:          req.StringOr("tag", ""),
				RecordSchema: req.StringOr("record_schema", ""),
				PersonID:     req.StringOr("person_id", ""),
				Query:        req.StringOr("query", ""),
				ReviewState:  fixture.ReviewState(req.StringOr("review_state", "")),
				Limit:        req.IntOr("limit", 0),
				Offset:       req.IntOr("offset", 0),
			})
			if err != nil {
				return nil, toolError(err)
			}
			return mcplib.NewToolResponseStructured(map[string]any{"dataset": d, "fixtures": list, "count": len(list)}), nil
		},
	)
}
