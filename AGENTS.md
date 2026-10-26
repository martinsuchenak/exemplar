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
- `internal/auth/`: API key middleware; it resolves keys through the account service.
- `internal/ctxkeys/`: typed context keys.
- `internal/app/`: opens the database and blob store and builds every service.
- `internal/account/`, `internal/dataset/`, `internal/fixture/`: handler -> service -> storage per resource.
- `internal/audit/`: append-only audit log, written inside the callers' transactions.
- `internal/blob/`: content-addressed file store (storage layer only).
- `internal/principal/`: the authenticated caller passed to every service method.
- `internal/apperr/`: error kinds services return; `rest.RespondServiceError` maps them to HTTP.
- `cmd/mcp/`: read-only MCP server and its tools; same API key auth and services as REST.
- `internal/ingest/`, `internal/anonymise/`, `internal/mimetree/`: real-email ingest into quarantine; the anonymiser touches no storage (registry and Presidio sit behind interfaces). Never log or store raw input or dry-run reports. See `docs/anonymisation.md`.
- `internal/archive/`, `internal/exchange/`, `internal/backup/`: one archive format; export/import of datasets (REST, normal access rules) and host-only backup/restore. See `docs/backup-and-exchange.md`.
- `internal/db/`: SQLite connection and migrations.
  The DB package returns a `Connection` wrapper with:
  `SQL` for raw `*sql.DB`
  `DB` for `xdal.DBInterface`

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
- All database and file access and every access check live in the service layer. Handlers, CLI commands and MCP tools only parse input, call a service from `internal/app` with the caller's principal, and format the result.
- Storage types hold SQL only and are called only by their own service. Services that need another resource's rules call that service (for example `dataset.Service.Authorize`).
- Record an audit event in the same transaction as every security-relevant change.
- Cover every change with tests as fully as practical (success path, each validation error, each access rule, each error kind) and document it: code comments plus the matching page in `docs/`. See `docs/development.md`.

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

- `db.Open` creates the SQLite file (mode 600) and runs pending migrations.
- Migrations are embedded SQL files in `internal/db/migrations/`, listed in `internal/db/migrations.go` and run by `xdal/pkg/migrate` in the `core` namespace. Add a new file and entry; never edit an applied one.
- Use `conn.DB` for portable query-builder-based access.
- Use `conn.SQL` for migrations, pool configuration, or low-level SQL calls.

## Testing Expectations

- Run `task test` after meaningful Go changes.
- Run `task lint` before concluding changes.
- Generated projects are expected to pass `go test ./...` out of the box.
