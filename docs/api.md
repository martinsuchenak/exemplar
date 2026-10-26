# REST API

All API routes live under `/api/v1`. The OpenAPI description is in
[`openapi.yaml`](../openapi.yaml). The CLI uses this API through
`internal/client` (see [cli.md](cli.md)).

## Authentication

Send an API key in the `X-API-Key` header. Missing, unknown and revoked keys,
and keys of disabled accounts, all get `401`. The first admin key comes from
`exemplar bootstrap`; further keys are created through `POST /api/v1/keys`
(see [security.md](security.md) for who may create what).

## Errors

Errors are JSON: `{"error": "<message>"}`.

| Status | Service error | Meaning |
|--------|---------------|---------|
| 400 | `ErrInvalid` | The input failed validation, or the body or a query parameter is malformed |
| 401 | `ErrUnauthenticated` | No valid key |
| 403 | `ErrForbidden` | The key may see the resource but not do this (scope or role) |
| 404 | `ErrNotFound` | Missing, or not visible to this key |
| 409 | `ErrConflict` | Name taken, dataset frozen, already reviewed, last admin |
| 413 | `ErrTooLarge` | Fixture content over the server's limit (`storage.max_fixture_mb`) |
| 500 | anything else | Internal error; details are logged, not returned |

## Endpoints

### Service

| Method and path | Auth | Description |
|-----------------|------|-------------|
| `GET /health` | none | `{"status":"ok"}` |
| `GET /metrics` | none | Goroutine count and memory |

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
| `GET /api/v1/datasets` | any key | `?scope=`, `?name=` | Datasets you can read |
| `POST /api/v1/datasets` | write key; global and quarantine: admin | `{"name", "description", "scope", "tags"}` | `201` Dataset |
| `GET /api/v1/datasets/{id}` | readers | | Dataset |
| `DELETE /api/v1/datasets/{id}` | owner or admin | | `204`; fixtures and unused contents go too |
| `POST /api/v1/datasets/{id}/freeze` | owner or admin | | Dataset |
| `POST /api/v1/datasets/{id}/fork` | write key that can read the source | `{"name", "description"}` (optional) | `201` CopyResult |
| `POST /api/v1/datasets/{id}/promote` | admin | `{"name"}` (optional) | `201` CopyResult |

A **CopyResult** is `{"dataset": Dataset, "copied": n, "skipped": n}`. Fork
creates a workspace you own; promote creates the next version of a global
dataset, frozen. Both copy fixture metadata only (contents are shared) and skip
real-derived fixtures that are not approved. Without a name, the source's name
is used.

### Fixtures

| Method and path | Who | Body / query | Response |
|-----------------|-----|--------------|----------|
| `GET /api/v1/datasets/{id}/fixtures` | readers | `?kind= &format= &scenario= &tag= &review_state= &limit= &offset=` | Fixtures, by name; limit defaults to 100, at most 1000 |
| `POST /api/v1/datasets/{id}/fixtures` | can change the dataset | Raw content as the body; `?name=` (required) `&kind= &format= &mime_type= &scenario= &tags=a,b &source= &generator=` | `201` Fixture |
| `GET /api/v1/fixtures/{id}` | readers | | Fixture |
| `GET /api/v1/fixtures/{id}/content` | readers | | The content |
| `DELETE /api/v1/fixtures/{id}` | can change the dataset | | `204` |
| `POST /api/v1/fixtures/{id}/review` | admin | `{"approve": true\|false}` | Fixture |
| `POST /api/v1/purge` | admin | `{"ingest_batch": "…"}` | `{"ingest_batch", "deleted": n}` |

Uploads stream to disk; kind, format and MIME type are inferred from the name
when not given. `tags` may be comma-separated or repeated. Uploading declares
the content holds no real data; `source=anonymised` is only accepted into
quarantine datasets.

Content downloads are served as attachments named after the fixture, with
`X-Content-Type-Options: nosniff` and `Content-Security-Policy: sandbox`, so a
browser never runs fixture HTML or scripts.

Purge removes the batch's fixtures from every dataset they were copied to,
frozen ones included, and is audit-logged.

### Audit log

| Method and path | Who | Query | Response |
|-----------------|-----|-------|----------|
| `GET /api/v1/audit` | admin | `?account_id= &action= &target_type= &target_id= &after_seq= &limit=` | Events in order; page with `after_seq` set to the last `seq` seen |
