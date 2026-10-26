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
the datasets table itself. In the other direction, fork and promote need
fixtures copied inside the dataset service's transaction: the dataset service
calls a `dataset.FixtureCopier`, which `fixture.Service` implements and
`app.Open` wires in with `UseFixtures`.

## Generation

```
generate.Service ─> dataset.Service.Authorize (write)
                 ─> person.Service.Pick (reuse or create people)
                 ─> synth (content, from the seed)
                 ─> fixture.Service.Create (each item: access, blob, links, audit)
```

`synth` never touches storage, so it is tested on its own, byte for byte. See
[generation.md](generation.md).

With template emails (`from`), generation reads each template through
`fixture.Service.Get` and `Open` (read access, approval checked in
`generate`), loads its linked people from the person service, and renders
with `emailtemplate`, which is pure like `synth`.

## Ingest

```
ingest.Service ─> admin check, dataset.Service.Authorize (write, quarantine only)
               ─> person.Service.Identities (hashed identity and name-token registry)
               ─> anonymise.Engine.Prepare (every header in the batch)
               ─> anonymise.Engine.Anonymise (each email: mimetree, Presidio, patterns, synth)
               ─> fixture.Service.Create (each email: blob, provenance, people, audit)
               ─> audit (fixture.ingest)
```

`anonymise` reaches storage only through its `Registry` interface and Presidio
only through its `Detector` interface, so it is tested with in-memory fakes.
`app.Open` builds the anonymiser from the key, Presidio URL and profiles; with
no key or no Presidio URL, ingest reports that it is not configured. See
[anonymisation.md](anonymisation.md).

## CLI and API

The MCP server (`cmd/mcp`) is another entry point of the same kind: each tool
reads its arguments, calls a service with the key's principal and returns the
result, and `toolError` maps service errors to MCP error codes. It runs on its
own port behind the same API key middleware. See [mcp.md](mcp.md).

Host commands (`serve`, `bootstrap`, `init`) open the app directly. All other
commands are remote: they call the REST API through `internal/client`, so a
local instance and a shared server are used the same way and the server's
services make every access decision.

```
exemplar upload ─> internal/client ─HTTP─> cmd/routes ─> fixture.Handler ─> fixture.Service
```

`push-mailpit` downloads emails the same way, then `internal/mailpit` sends the
bytes over SMTP. That package only talks SMTP: no storage, no access decisions.

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
| `internal/person` | The person registry: list, get, generate, and `Pick` for generated content. |
| `internal/generate` | Fills datasets with synthetic fixtures; stores each through the fixture service. |
| `internal/synth` | Pure, seeded content generation: people, companies, text, files, emails, records. No database. |
| `internal/client` | Typed REST client; remote CLI commands use it. Errors unwrap to `apperr` kinds. |
| `internal/ingest` | Takes real emails into quarantine: access checks, the anonymiser, storing fixtures, the dry-run report. |
| `internal/anonymise` | The anonymisation engine: detection (Presidio client and patterns), keyed pseudonyms, MIME rewriting, profiles. No database; `anonymisetest` has a fake detector. |
| `internal/emailtemplate` | Renders an email as a template: swaps its people and companies, renews Message-IDs, keeps everything else. No database. |
| `internal/textswap` | One-pass, word-boundary, case-insensitive replacement of many strings; shared by the anonymiser and templates. |
| `internal/mimetree` | Parses and re-serialises MIME messages losslessly enough to rewrite parts in place; charsets and transfer encodings. |
| `internal/mailpit` | Sends email bytes over SMTP with run tags; `mailpittest` is a fake SMTP server for tests. |
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
