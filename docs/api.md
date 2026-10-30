# REST API

All API routes live under `/api/v1`. The OpenAPI description is in
[`openapi.yaml`](../openapi.yaml). The CLI uses this API through
`internal/client` (see [cli.md](cli.md)).

## Authentication

Send an API key in the `X-API-Key` header, or as `Authorization: Bearer exm_…`
(bearer tokens without the `exm_` prefix are ignored, so SSO tokens can use
the header later). Missing, unknown and revoked keys,
and keys of disabled accounts, all get `401`. The first admin key comes from
`exemplar bootstrap`; further keys are created through `POST /api/v1/keys`
(see [security.md](security.md) for who may create what).

## Errors

Errors are JSON: `{"error": "<message>"}`, including `404` for paths that
do not exist. A `401` carries `WWW-Authenticate: Bearer realm="exemplar"`
(plus `error="invalid_token"` when a key was sent but rejected). OAuth
discovery and registration paths (`/.well-known/oauth-*`,
`/.well-known/openid-configuration`, `/register`, `/authorize`, `/token`)
answer `404` with `{"error": "invalid_request", "error_description": …}`
explaining that exemplar takes API keys, so OAuth-first clients such as MCP
clients report something useful.

| Status | Service error | Meaning |
|--------|---------------|---------|
| 400 | `ErrInvalid` | The input failed validation, or the body or a query parameter is malformed |
| 401 | `ErrUnauthenticated` | No valid key |
| 403 | `ErrForbidden` | The key may see the resource but not do this (scope or role) |
| 404 | `ErrNotFound` | Missing, or not visible to this key |
| 409 | `ErrConflict` | Name taken, dataset frozen, already reviewed, last admin |
| 413 | `ErrTooLarge` | Fixture content over the server's limit (`storage.max_fixture_mb`), a JSON body over 1 MiB, or an ingest, anonymise run or import over its limit |
| 429 | `ErrBusy` | The key already has an anonymise run in progress |
| 503 | none | A one-file anonymise request whose file failed because the anonymiser's detector (Presidio) did; try again later |
| 500 | anything else | Internal error; details are logged, not returned |

## Endpoints

### Service

| Method and path | Auth | Description |
|-----------------|------|-------------|
| `GET /health` | none | `{"status":"ok"}` |
| `GET /metrics` | admin | Goroutine count and memory |

### Accounts and keys

| Method and path | Who | Body / query | Response |
|-----------------|-----|--------------|----------|
| `GET /api/v1/me` | any key | | `{"account": Account, "principal": Principal}` |
| `GET /api/v1/accounts` | any key | | All accounts (admin) or your own |
| `POST /api/v1/accounts` | admin | `{"name", "kind", "role"}` | `201` Account |
| `GET /api/v1/accounts/{id}` | self or admin | | Account |
| `POST /api/v1/accounts/{id}/disable` | admin | | `204`; the account's keys are revoked |
| `GET /api/v1/keys` | any key | `?account_id=` (others: admin) | Keys |
| `POST /api/v1/keys` | write key | `{"account_id", "name", "scopes"}` | `201` `{"key": APIKey, "secret": "exm_…"}`, `Cache-Control: no-store` |
| `DELETE /api/v1/keys/{id}` | own key or admin | | `204` |

### Datasets

| Method and path | Who | Body / query | Response |
|-----------------|-----|--------------|----------|
| `GET /api/v1/datasets` | any key | `?scope=`, `?name=`, `?query=` | Datasets you can read |
| `POST /api/v1/datasets` | write key; global and quarantine: admin | `{"name", "description", "scope", "tags"}` | `201` Dataset |
| `GET /api/v1/datasets/{id}` | readers | `{id}` may be the exact name | Dataset |
| `DELETE /api/v1/datasets/{id}` | owner or admin | | `204`; fixtures and unused contents go too |
| `POST /api/v1/datasets/{id}/freeze` | owner or admin | | Dataset |
| `POST /api/v1/datasets/{id}/fork` | write key that can read the source | `{"name", "description"}` (optional) | `201` CopyResult |
| `POST /api/v1/datasets/{id}/promote` | admin | `{"name", "description"}` (optional) | `201` CopyResult |

`GET /api/v1/datasets/{id}` also takes a dataset's exact name, so a client
can turn a name into an id; every other route takes an id. A name means the
newest version of a global dataset, or a workspace, among what the key can
read; one that names several answers `400` with their ids. `?query=` (on
this list and on a dataset's fixtures) matches words, each of which must
appear in a dataset's name, description or a tag, or a fixture's name; see
[cli.md](cli.md#naming-a-dataset).

A **CopyResult** is `{"dataset": Dataset, "copied": n, "skipped": n}`. Fork
creates a workspace you own; promote creates the next version of a global
dataset, frozen. Both copy fixture metadata only (contents are shared) and skip
real-derived fixtures that are not approved. Without a name or description,
the source's is used; give a promotion from quarantine its own description,
since the quarantine one is about review.

### Fixtures

| Method and path | Who | Body / query | Response |
|-----------------|-----|--------------|----------|
| `GET /api/v1/datasets/{id}/fixtures` | readers | `?kind= &format= &scenario= &tag= &review_state= &record_schema= &person_id= &query= &limit= &offset=` | Fixtures, by name; limit defaults to 100, at most 1000 |
| `POST /api/v1/datasets/{id}/fixtures` | can change the dataset | Raw content as the body; `?name=` (required) `&kind= &format= &mime_type= &scenario= &tags=a,b &source= &generator= &record_schema=` | `201` Fixture |
| `GET /api/v1/fixtures/{id}` | readers | | Fixture |
| `GET /api/v1/fixtures/{id}/content` | readers | | The content |
| `DELETE /api/v1/fixtures/{id}` | can change the dataset | | `204` |
| `POST /api/v1/fixtures/{id}/review` | admin | `{"approve": true\|false}` | Fixture |
| `POST /api/v1/purge` | admin | `{"ingest_batch": "…"}` | `{"ingest_batch", "deleted": n}` |

A Fixture includes `record_schema` (records only) and `people`: the registry
people it mentions, as `{"person_id", "role"}`.

Fixture names are 1–255 characters without slashes or control characters, and
not `.` or `..`. Uploads, ingests, imports and content downloads may take up
to an hour, as long as they never go two minutes without moving a byte;
other requests time out after five minutes.

Uploads stream to disk; kind, format and MIME type are inferred from the name
when not given; `record_schema` makes the upload a record. `tags` may be comma-separated or repeated. Uploading declares
the content holds no real data; `source=anonymised` is only accepted into
quarantine datasets.

Content downloads are served as attachments named after the fixture, with
`X-Content-Type-Options: nosniff` and `Content-Security-Policy: sandbox`, so a
browser never runs fixture HTML or scripts.

Purge removes the batch's fixtures from every dataset they were copied to,
frozen ones included, and is audit-logged.

### Ingest

| Method and path | Who | Body | Response |
|-----------------|-----|------|----------|
| `POST /api/v1/datasets/{id}/ingest` | admin; quarantine datasets only | `multipart/form-data`: one or more `file` parts (emails and documents; the file name's extension picks the format), optional `profile`, `batch`, `dry_run` fields | `201` IngestResult; `200` for a dry run |

An **IngestResult** is `{"batch", "profile", "key_id", "dry_run", "files":
[…]}`. Each file is `{"name", "fixture_id", "fixture_name", "replacements",
"attachments", "attachments_rewritten", "dropped_headers", "dropped_parts", "error", "report"}`
(`dropped_parts` counts a document's parts removed rather than rewritten, such
as a DOCX's macros); `error` is set for a
file that failed while the others went ahead, and `report` (dry run only)
lists `{"entity", "original", "fake"}`. The report is original personal data;
responses carry `Cache-Control: no-store`.

Parts are read into memory, never into temporary files. A request over
`storage.max_ingest_mb` gets `413`; more than 1000 files, an unknown profile, a
bad batch name or a dataset that is not quarantine get `400`, and so does a
server with no anonymiser configured. See [anonymisation.md](anonymisation.md).

### Anonymise

| Method and path | Who | Body / query | Response |
|-----------------|-----|--------------|----------|
| `POST /api/v1/anonymise` | any key with `read`; `link_registry`: admin | One file as the raw body, with `?name=` or `?format=`; or `multipart/form-data` with `file` parts. Optional `profile` and `link_registry` (query, or form fields) | `200` the anonymised file, in its own format; or a zip of the files plus `manifest.json` for a multipart request |

The anonymise tool stores nothing and never returns original data.

- **One file:** the response body is the file. `Content-Type` is the format's (`text/markdown`, `application/json`, …), and `Content-Disposition` carries the fake file name. `X-Exemplar-Format`, `X-Exemplar-Replacements`, `X-Exemplar-Attachments`, `X-Exemplar-Attachments-Rewritten` and `X-Exemplar-Dropped-Headers` describe it, and `X-Exemplar-Dropped-Parts` lists a document's dropped parts, comma-separated, when there are any. A file that cannot be read gets `400` with the usual JSON error; one that fails because the detector did gets `503` (see below).
- **Several files** (multipart) are one batch sharing fakes. The zip holds each file under its fake name and `manifest.json`: `{"profile", "linked", "files": [{"name", "format", "replacements", "attachments", "attachments_rewritten", "dropped_headers", "dropped_parts", "error"}]}`, in the order sent (`dropped_parts` is a list of part names, left out when empty: Office's own parts by name, any other DOCX or XLSX part by a keyed name, a PDF's by what they are (`JavaScript`, `embedded files`…), so none is the sender's text). A failed file has an `error` and no entry in the zip.
- **Detector failures:** when Presidio cannot be reached, answers with an error status or sends something unreadable, the file's error is only `the anonymiser's detector is unavailable; try again later`. The detector's URL, host and network error stay on the server, unlogged. Ingest reports the same error per file.
- **Every response** carries `Cache-Control: no-store`, `X-Content-Type-Options: nosniff`, `Content-Security-Policy: sandbox` and `X-Exemplar-Unreviewed: true`.
- **Fakes** come from a key made for the request alone, unless an admin sets `link_registry`. See [anonymisation.md](anonymisation.md#anonymise-without-ingesting).
- **Limits:** a request over `storage.max_ingest_mb` gets `413`; more than 1000 files, an unknown format or profile, or a server without Presidio get `400`; a second run for the same key while one is going, or any run past `storage.max_concurrent_runs` (shared with ingest), gets `429`, before its body is read.

### Export and import

| Method and path | Who | Body | Response |
|-----------------|-----|------|----------|
| `POST /api/v1/export` | readers of every dataset named | `{"dataset_ids": ["…"]}` (1–50) | `200` `application/gzip` archive, as an attachment, `Cache-Control: no-store`; counts in `X-Exemplar-Datasets`, `-Fixtures`, `-Persons`, `-Blobs`, `-Skipped` |
| `POST /api/v1/import` | write key; global and quarantine datasets and real-derived fixtures: admin | The archive as the body (`application/gzip`) | `201` `{"datasets": [{"id", "name", "scope", "version", "created", "added", "existing"}], "people_added", "people_merged", "skipped": [{"id", "name", "dataset_id", "reason"}]}` |

Export decides and audits everything before the first byte, so errors come
back with a status code; a failure while streaming aborts the connection, so
the client sees an error rather than a short archive. A damaged or truncated
archive gets `400`; one over `storage.max_import_mb`, over ten times that
uncompressed, or with a content entry over `storage.max_fixture_mb` gets
`413`. See
[backup-and-exchange.md](backup-and-exchange.md).

### People and generation

| Method and path | Who | Body / query | Response |
|-----------------|-----|--------------|----------|
| `GET /api/v1/persons` | any key | `?company= &limit= &offset=` | People by last name |
| `POST /api/v1/persons` | write key | `{"count", "seed"}` | `201` `{"seed", "people": [Person]}` |
| `GET /api/v1/persons/{id}` | any key | | Person |
| `GET /api/v1/record-schemas` | any key | | `[{"name", "description", "step", "builtin", "hash", "fields"}]`, by name |
| `POST /api/v1/datasets/{id}/generate` | can change the dataset | `{"kind", "count", "formats", "shapes", "from", "schemas", "profile", "people", "seed", "scenario", "tags"}` | `201` `{"seed", "fixtures": [Fixture], "person_ids"}` |

See [generation.md](generation.md) for the request fields, email shapes and
record schemas.

### Audit log

| Method and path | Who | Query | Response |
|-----------------|-----|-------|----------|
| `GET /api/v1/audit` | admin | `?account_id= &action= &target_type= &target_id= &after_seq= &limit=` | Events in order; page with `after_seq` set to the last `seq` seen |
