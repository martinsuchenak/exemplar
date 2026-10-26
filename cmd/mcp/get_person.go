package mcp

import (
	"context"

	mcplib "github.com/paularlott/mcp"
)

func init() {
	RegisterTool(registerGetPersonTool)
}

func registerGetPersonTool(server *mcplib.Server) {
	persons := services.Persons
	server.RegisterTool(
		mcplib.NewTool("get_person",
			"Get a fake person from the person registry: name, email, phone, job title, company and address. "+
				"Fixtures list the people they mention by id; search_fixtures with person_id finds everything about a person.",
			mcplib.String("id", "Person id", mcplib.Required()),
		),
		func(ctx context.Context, req *mcplib.ToolRequest) (*mcplib.ToolResponse, error) {
			p, err := caller(ctx)
			if err != nil {
				return nil, err
			}
			id, err := required(req, "id")
			if err != nil {
				return nil, err
			}
			person, err := persons.Get(ctx, p, id)
			if err != nil {
				return nil, toolError(err)
			}
			return mcplib.NewToolResponseStructured(person), nil
		},
	)
}
