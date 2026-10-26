# Architecture

exemplar is one Go binary. It stores test fixtures (emails, files, records) in
datasets, keeps their contents in a content-addressed blob store, and serves them
over a REST API, a read-only MCP server and a CLI. State lives in one SQLite
file and one blob directory; each instance is independent.

## Layers

Every resource follows the scaffold's handler → service → storage layering:

```
REST handler ─┐
CLI command  ─┼─> service (access checks, transactions, audit) ─> storage (SQL) ─> SQLite
MCP tool     ─┘                    │
                                   └─> blob.Store (files) ─> blob directory
```

| Layer | Does | Never does |
|-------|------|------------|
| Entry points (`cmd/`, `cmd/routes/`, `cmd/mcp/`, `internal/auth/`) | Parse input, get the caller's `principal.Principal`, call a service, format the result | Query the database, touch blob files, decide permissions |
| Services (`internal/<resource>/service.go`) | Validate input, check access, run transactions, record audit events, call storage and the blob store | Know about HTTP, flags or MCP |
| Storage (`internal/<resource>/storage.go`, `internal/blob`) | Run SQL or file operations | Check permissions; be called by anything but its own service |

A service that needs another resource's rules asks that resource's service. For
example, the fixture service calls `dataset.Service.Authorize` and never reads
the datasets table itself.

## Packages

| Package | Role |
|---------|------|
| `internal/app` | Opens the database and blob store and builds every service. All entry points get services from here. |
| `internal/principal` | The authenticated caller: account, role, key and scopes. Passed to every service method. |
| `internal/account` | Accounts and API keys, bootstrap and recovery, key authentication. |
| `internal/dataset` | Datasets, their scopes (global, workspace, quarantine) and the access rules for them. |
| `internal/fixture` | Fixtures: metadata, content through the blob store, provenance and review state. |
| `internal/blob` | Content-addressed file store and the `blobs` table; garbage collection. |
| `internal/audit` | Append-only audit log, written inside the callers' transactions. |
| `internal/apperr` | Error kinds services return (`ErrNotFound`, `ErrForbidden`…). |
| `internal/rest` | JSON helpers; `RespondServiceError` maps error kinds to HTTP status codes. |
| `internal/auth` | `X-API-Key` middleware; resolves keys through the account service. |
| `internal/db` | SQLite connection (pragmas, file permissions) and migrations. |
| `internal/storeutil` | IDs (UUIDv7), timestamp and JSON column helpers for storage code. |
| `internal/testutil` | Builds a real app in a temp directory plus admin and member principals for tests. |

## Errors

Services wrap one of the `apperr` kinds with a human-readable message:

```go
return fmt.Errorf("%w: dataset %q is frozen", apperr.ErrConflict, d.Name)
```

Callers branch with `errors.Is`. The REST layer maps kinds to status codes (see
[api.md](api.md)); any other error becomes a generic 500 so internal details
(paths, SQL) never reach clients.

A resource the caller may not read is always reported as `ErrNotFound`, not
`ErrForbidden`, so keys cannot probe for things they cannot see.

## Transactions and audit

Services wrap each change in `db.WithTx` and record the audit event with the same
transaction, so a change and its audit entry commit or roll back together.
SQLite is opened with `_txlock=immediate`, so concurrent writers queue instead of
failing.

## Blob storage

Fixture contents are stored once per SHA-256 under
`<blob_dir>/objects/ab/cd/<hash>` (mode 600). Adding a fixture:

1. writes the content to `<blob_dir>/tmp`, hashing it on the way, and renames it into place;
2. registers the hash in the `blobs` table on its own;
3. inserts the fixture row and audit event in one transaction.

If step 3 fails, the blob row is unreferenced and the next garbage collection
removes it, so no file is ever left untracked. Garbage collection runs after a
fixture or dataset is deleted and removes blobs no fixture refers to. A
read/write lock in `blob.Store` stops it from deleting a file that an in-flight
upload of the same content is about to reference.

## Adding a resource

1. Generate the endpoint with `go-scaffolder add api-endpoint` (or `mcp-tool`, `cli-command`).
2. Put types, validation and access checks in `internal/<resource>/service.go`; SQL in `storage.go`.
3. Add a migration (see [data-model.md](data-model.md)).
4. Build the service in `internal/app` and use it from the handler, command or tool.
5. Record audit events for security-relevant changes; add the action constant in `internal/audit`.
6. Write tests (see [development.md](development.md)) and update these docs.
