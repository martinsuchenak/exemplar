# AGENTS

## Overview

- App: `exemplar`
- Module: `github.com/martinsuchenak/exemplar`
- Primary config file: `exemplar-config.toml`
- CLI support is always enabled.

## Enabled Features

| Feature | Status |
|---------|--------|
| CLI | enabled |
| API | enabled |
| MCP | enabled |
| UI | disabled |
| DB | enabled (sqlite, xdal) |
| Cache | disabled |
| Docker | enabled |
| Nomad | disabled |

## Project Layout

- `main.go`: CLI entrypoint.
- `cmd/`: command registration and command implementations.
- `build/`: build metadata injected through `ldflags`.
- `cmd/routes/`: HTTP route registration.
- `internal/rest/`: JSON/HTTP helpers.
- `internal/auth/`: auth middleware placeholders.
- `internal/ctxkeys/`: typed context keys.
- `internal/sample/`: sample handler -> service -> storage implementation.
- `cmd/mcp/`: MCP server wiring and sample tool.
- `internal/db/`: connection setup and schema.
  The DB package returns a `Connection` wrapper with:
  `SQL` for raw `*sql.DB`
  `DB` for `xdal.DBInterface`
- `internal/resolve/`: DNS SRV resolution support.

## Configuration Model

- Runtime config is loaded from `exemplar-config.toml`.
- Environment variables and command flags can override config values.
- Logging config is always present under `[log]`.
- API server config lives under `[server]`.
- Database config lives under `[database]`.

## Working Rules

- Prefer the generated task targets before inventing ad hoc build or test commands.
- Preserve the generated layering and self-registration patterns unless there is a deliberate refactor.
- Use `go-scaffolder add` for new CLI commands, API endpoints, or MCP tools when the scaffolder is available.
- Do not remove comments that start with `go-scaffolder:` unless you also update the patching flow that depends on them.
- When editing feature-specific code, keep imports and initialization aligned with the enabled feature set.

## Standard Commands

```sh
task build
task build-local
task test
task lint
task check   # full CI gate: vet + lint + test:race
```

## Feature-Specific Commands

## Runtime Entry Points

```sh
go run . serve
go run . init
go run . completion --shell bash
```
## Database Notes

- Connection setup supports direct `host:port` targets and DNS SRV names that start with `_`.
- Schema lives in `internal/db/schema.sql`.
- Use `conn.DB` for portable query-builder-based access.
- Use `conn.SQL` for migrations, pool configuration, or low-level SQL calls.

## Testing Expectations

- Run `task test` after meaningful Go changes.
- Run `task lint` before concluding changes.
- Generated projects are expected to pass `go test ./...` out of the box.
