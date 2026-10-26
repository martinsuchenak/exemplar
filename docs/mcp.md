# MCP server

`exemplar serve` also runs a Model Context Protocol server so AI agents can
find and read test data. It is **read-only**: four tools that list and read,
nothing that changes data. Write tools will follow once the data structures
and workflows settle; until then, use the REST API or CLI to change data.

## Connecting

The endpoint is `/mcp` on the same port as the REST API (`server.port`,
default 8080). It is on by default:

| Flag | Env var | Config | Default |
|------|---------|--------|---------|
| `--mcp` | `MCP_ENABLED` | `mcp.enabled` | `true`; `--mcp=false` turns it off (`/mcp` then answers 404) |

Every request needs an API key, sent as `Authorization: Bearer exm_…` (what
most MCP clients send) or `X-API-Key: exm_…`. The tools act as that key, so
an agent sees exactly what the key may see over REST: its own workspaces, the
global library, and quarantine only with an admin key. A read-only key is
enough for every tool; give agents one.

For Claude Code, add the server to `.mcp.json`:

```json
{
  "mcpServers": {
    "exemplar": {
      "type": "http",
      "url": "http://localhost:8080/mcp",
      "headers": { "Authorization": "Bearer ${EXEMPLAR_API_KEY}" }
    }
  }
}
```

## Tools

### `list_datasets`

The datasets the key can read.

| Argument | Description |
|----------|-------------|
| `scope` | `global`, `workspace` or `quarantine` |
| `name` | Exact name (all versions) |

Returns `{"datasets": [Dataset], "count": n}`.

### `search_fixtures`

A dataset's fixtures, metadata only, ordered by name.

| Argument | Description |
|----------|-------------|
| `dataset_id` | Required |
| `kind` | `email`, `file` or `record` |
| `format` | e.g. `eml`, `pdf`, `json` |
| `scenario`, `tag` | Labels; generated emails have their shape as scenario |
| `record_schema` | `contact`, `company`, `project`, `opportunity`, `address` |
| `person_id` | Fixtures that mention this person |
| `review_state` | `not-required`, `unreviewed`, `approved`, `rejected` |
| `limit`, `offset` | Paging; limit defaults to 100, at most 1000 |

Returns `{"fixtures": [Fixture], "count": n}`.

### `get_fixture`

One fixture's metadata and content.

| Argument | Description |
|----------|-------------|
| `id` | Required |
| `content` | `auto` (default), `none` or `base64` |

With `auto`:

- text (emails, CSV, JSON records, HTML, RTF, plain text) up to 256 KiB comes back
  as `content`;
- images up to 1 MiB come back as an image block next to the metadata;
- other binaries (PDF, DOCX, XLSX) and larger text get a `content_note`
  saying how to fetch them.

With `base64`, any content up to 1 MiB comes back as `content_base64`. Larger
content is left out with a note pointing at the REST download
(`GET /api/v1/fixtures/{id}/content`). `none` returns metadata only.

Reading real-derived content is audit-logged as `fixture.download`, exactly as
a REST download is; asking for metadata only is not a download.

### `get_person`

A person from the registry. Fixtures list the people they mention
(`people[].person_id`); `search_fixtures` with `person_id` finds everything
about one person.

| Argument | Description |
|----------|-------------|
| `id` | Required |

## Errors

Tool errors are JSON-RPC errors:

| Code | Meaning |
|------|---------|
| `-32602` | Invalid arguments (missing `id`, unknown kind or scope, bad `content` mode) |
| `-32001` | Not found, or not visible to this key |
| `-32002` | Forbidden for this key |
| `-32003` | Conflict |
| `-32004` | Unauthenticated |
| `-32603` | Internal error; details are logged, not returned |

A request without a valid key never reaches a tool: it gets HTTP `401`.
