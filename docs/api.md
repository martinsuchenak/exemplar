# REST API

The OpenAPI description is in [`openapi.yaml`](../openapi.yaml).

## Authentication

Send an API key in the `X-API-Key` header. Missing, unknown and revoked keys,
and keys of disabled accounts, all get `401`. Keys are created with
`exemplar bootstrap` (first admin) and by admins and members as described in
[security.md](security.md).

## Errors

Errors are JSON: `{"error": "<message>"}`.

| Status | Service error | Meaning |
|--------|---------------|---------|
| 400 | `ErrInvalid` | The input failed validation; the message says why |
| 401 | `ErrUnauthenticated` | No valid key |
| 403 | `ErrForbidden` | The key may see the resource but not do this (scope or role) |
| 404 | `ErrNotFound` | Missing, or not visible to this key |
| 409 | `ErrConflict` | Name taken, dataset frozen, already reviewed, last admin |
| 500 | anything else | Internal error; details are logged, not returned |

## Endpoints

| Method and path | Auth | Description |
|-----------------|------|-------------|
| `GET /health` | none | `{"status":"ok"}` |
| `GET /metrics` | none | Goroutine count and memory |
| `GET /api/v1/me` | key | The calling account and the principal the key acts as |

### `GET /api/v1/me`

```json
{
  "account": {"id": "…", "name": "admin", "kind": "developer", "role": "admin", "created_at": "…"},
  "principal": {"account_id": "…", "account_name": "admin", "role": "admin", "key_id": "…", "scopes": ["read", "write", "admin"]}
}
```

Endpoints for accounts, keys, datasets and fixtures come next; the services
behind them already exist.
