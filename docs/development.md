# Development

## Commands

```bash
task test          # go test ./... -v
task test:race     # with the race detector
task lint          # golangci-lint
task check         # vet + lint + race tests (the CI gate)
go run . serve
```

Coverage across packages:

```bash
go test -coverpkg=./... -coverprofile=cover.out ./... && go tool cover -func=cover.out | tail -1
```

## Expectations

- Every change is covered by tests, as fully as practical: the success path,
  each validation error, each access rule (who may and who may not) and each
  error kind a caller can see.
- Every change is documented: package and exported-symbol comments in code, and
  the matching page in `docs/` (architecture, security, data model, CLI, API).
- `task check` passes before committing.

## Writing tests

- **Service tests** live in external test packages (`package account_test`) and
  use `internal/testutil`:

  ```go
  a := testutil.NewApp(t)                       // real SQLite + blob dir in t.TempDir()
  admin := testutil.Admin(t, a)                 // bootstraps the first admin
  alice := testutil.Member(t, a, admin, "alice") // member with a read/write key
  ```

  Assert error kinds with `errors.Is(err, apperr.ErrForbidden)`, not messages.
  To test a narrower key, copy a principal and change its `Scopes`.

- **Storage-level tests** (`internal/db`, `internal/blob`) are internal tests
  that open a database with `db.Open` in a temp directory.

- **CLI tests** run real commands through `runCLI(t, args...)` in
  `cmd/helpers_test.go`, which returns stdout. It builds fresh command values for
  each run because the CLI library keeps parsed flags on them.

- **HTTP tests** register routes on a mux with `routes.RegisterRoutes(mux, app)`
  and drive it with `httptest`.

Not covered by tests: `main`, the blocking listener in `serve`, and the
scaffold's sample MCP tool (to be replaced by the read-only MCP tools).
