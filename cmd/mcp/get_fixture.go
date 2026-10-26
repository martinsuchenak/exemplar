package mcp

import (
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"strings"
	"unicode/utf8"

	mcplib "github.com/paularlott/mcp"

	"github.com/martinsuchenak/exemplar/internal/fixture"
)

const (
	// maxInlineText is the largest text content get_fixture returns inline.
	maxInlineText = 256 << 10
	// maxInlineBinary is the largest binary content get_fixture returns, as
	// base64 or an image block.
	maxInlineBinary = 1 << 20
)

// Content modes for get_fixture.
const (
	contentAuto   = "auto"
	contentNone   = "none"
	contentBase64 = "base64"
)

func init() {
	RegisterTool(registerGetFixtureTool)
}

func registerGetFixtureTool(server *mcplib.Server) {
	fixtures := services.Fixtures
	server.RegisterTool(
		mcplib.NewTool("get_fixture",
			"Get one fixture: its metadata and, by default, its content. Text (emails, CSV, JSON records, HTML, RTF) up to 256 KiB comes back as text "+
				"and images up to 1 MiB as images; for other binaries (PDF, DOCX, XLSX) ask for content=base64 (up to 1 MiB).",
			mcplib.String("id", "Fixture id (from search_fixtures)", mcplib.Required()),
			mcplib.String("content", "auto (default), none, or base64"),
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
			mode := req.StringOr("content", contentAuto)
			if mode != contentAuto && mode != contentNone && mode != contentBase64 {
				return nil, mcplib.NewToolErrorInvalidParams("content must be auto, none or base64")
			}

			f, err := fixtures.Get(ctx, p, id)
			if err != nil {
				return nil, toolError(err)
			}
			out := map[string]any{"fixture": f}
			image, inline := plan(f, mode)
			if !inline {
				if mode != contentNone {
					out["content_note"] = contentNote(f, mode)
				}
				return mcplib.NewToolResponseStructured(out), nil
			}

			// Open checks access again and audit-logs reads of real-derived data.
			body, _, err := fixtures.Open(ctx, p, id)
			if err != nil {
				return nil, toolError(err)
			}
			defer func() { _ = body.Close() }()
			data, err := io.ReadAll(io.LimitReader(body, maxInlineBinary+1))
			if err != nil {
				return nil, mcplib.NewToolErrorInternal("reading fixture content failed")
			}

			switch {
			case mode == contentBase64:
				out["content_base64"] = base64.StdEncoding.EncodeToString(data)
			case image:
				return mcplib.NewToolResponseMulti(mcplib.NewToolResponseStructured(out), mcplib.NewToolResponseImage(data, f.MimeType)), nil
			case utf8.Valid(data):
				out["content"] = string(data)
			default:
				out["content_note"] = "content is not valid UTF-8 text; ask again with content=base64"
			}
			return mcplib.NewToolResponseStructured(out), nil
		},
	)
}

// plan decides whether to read the content inline, and whether it is an image.
func plan(f *fixture.Fixture, mode string) (image, inline bool) {
	switch mode {
	case contentNone:
		return false, false
	case contentBase64:
		return false, f.Size <= maxInlineBinary
	}
	if strings.HasPrefix(f.MimeType, "image/") {
		return true, f.Size <= maxInlineBinary
	}
	return false, isText(f) && f.Size <= maxInlineText
}

// isText reports whether a fixture's content is meant to be read as text.
func isText(f *fixture.Fixture) bool {
	mt := f.MimeType
	return strings.HasPrefix(mt, "text/") || mt == "message/rfc822" || mt == "application/json" ||
		mt == "application/rtf" || strings.HasSuffix(mt, "+json") || strings.HasSuffix(mt, "+xml")
}

// contentNote says why content was left out and how to get it.
func contentNote(f *fixture.Fixture, mode string) string {
	if mode == contentBase64 || f.Size > maxInlineText && isText(f) {
		return fmt.Sprintf("content is %d bytes, over the inline limit; download it from the REST API (GET /api/v1/fixtures/%s/content)", f.Size, f.ID)
	}
	return fmt.Sprintf("%s content is binary; ask again with content=base64 (up to %d bytes) or download it from the REST API", f.MimeType, maxInlineBinary)
}
