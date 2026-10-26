# Development

## Commands

```bash
task test          # go test ./... -v
task test:race     # with the race detector
task lint          # golangci-lint
task check         # vet + lint + race tests (the CI gate)
go run . serve
```

Integration test against a real Mailpit (needs Docker; starts and stops a
pinned `axllent/mailpit` container):

```bash
task test:mailpit
```

It sets `EXEMPLAR_TEST_MAILPIT_SMTP` and `EXEMPLAR_TEST_MAILPIT_API`; without
them `TestRealMailpit` is skipped, so `task test` needs no Docker.

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
  each run because the CLI library keeps parsed flags on them; add new
  commands' constructors to its list.

- **HTTP tests** register routes on a mux with `routes.RegisterRoutes(mux, app)`
  and drive it with `httptest`. `cmd/routes/resources_test.go` covers what the
  client never sends: missing keys on every route, malformed bodies and query
  parameters, and response headers.

- **Client tests** (`internal/client`) run the real routes in an
  `httptest.Server` and exercise every client method, including how each
  status maps back to an `apperr` kind.

- **SMTP tests** use `internal/mailpit/mailpittest`, an in-process SMTP server
  that records envelopes and data, can require AUTH PLAIN and can reject a
  recipient.

- **MCP tests** (`cmd/mcp/mcp_test.go`) serve `Handler(app)` with
  `httptest`, generate data, and call the tools with the MCP library's own
  client and a bearer key, checking results, access rules and error codes.

- **Remote CLI tests** start the same server with `startServer(t)` in
  `cmd/remote_test.go` and run commands with `s.run(t, key, args...)` or decode
  `--json` output with `s.runJSON`. `s.member(t, name)` creates a member and
  returns its key.

Not covered by tests: `main` and the blocking listeners in `serve` and
`StartMCPServer` (the servers they run are tested).
