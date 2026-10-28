# Development

## Commands

```bash
task build:local   # bin/exemplar, stamped with version, commit and date
task build         # every release platform into bin/
task test          # go test ./... -v, with a coverage profile
task test:race     # with the race detector
task lint          # golangci-lint over every package
task check         # vet + lint + race tests (the local CI gate)
task vuln          # govulncheck
task docker        # the image, tagged with the version
task dev           # go run . serve with debug logging
```

Builds use Go 1.27.3: the Taskfile sets `GOTOOLCHAIN`, so an older local Go
downloads it.

Integration test against a real Mailpit (needs Docker; starts and stops a
pinned `axllent/mailpit` container):

```bash
task test:mailpit
```

It sets `EXEMPLAR_TEST_MAILPIT_SMTP` and `EXEMPLAR_TEST_MAILPIT_API`; without
them `TestRealMailpit` is skipped, so `task test` needs no Docker.

Integration test of the anonymiser against a real Presidio Analyzer (needs
Docker; the image is about 1 GB):

```bash
task test:presidio
```

It sets `EXEMPLAR_TEST_PRESIDIO_URL`; without it `TestRealPresidio` is skipped.

Coverage across packages:

```bash
go test -coverpkg=./... -coverprofile=cover.out ./... && go tool cover -func=cover.out | tail -1
```

CI (`.github/workflows/ci.yml`) runs on every pull request and push to main:
formatting, a tidy `go.mod`, vet, lint, tests, `govulncheck`, the race
detector, a cross-compile for every release platform, and the Docker image
and `docker-compose.yml`. Tools are installed at the versions it names. It
pins every action to a commit hash, with the version in a trailing comment,
and `build/ci_test.go` fails on any action that is not pinned. Dependabot
(`.github/dependabot.yml`) opens weekly pull requests moving the pins, Go
modules and the Dockerfiles' base images to their latest releases.

Scratch code (experiments, a security tool's proof-of-concept tests) goes
in `tmp/`, which git ignores. Give it its own module (`tmp/go.mod` with
`module scratch`) so `go test ./...` and the linter in the repository root
skip it.

## Expectations

- Every change is covered by tests, as fully as practical: the success path,
  each validation error, each access rule (who may and who may not) and each
  error kind a caller can see.
- Every change is documented: package and exported-symbol comments in code, and
  the matching page in `docs/` (architecture, security, data model, CLI, API).
- A change to the `anonymise` command's flags, output or rules updates
  [`skills/exemplar-anonymise/SKILL.md`](../skills/exemplar-anonymise/SKILL.md)
  too: users install it, and agents follow it as written.
- `task check` passes before committing.

## Releasing

1. Under `## [Unreleased]` in `CHANGELOG.md`, list what the release
   changes; rename the heading to the version and date
   (`## [0.2.0] - 2026-11-03`) and start a new empty `[Unreleased]` above it.
2. Commit that and push it to main.
3. `task release -- v0.2.0`. It refuses a version with no changelog section,
   uncommitted changes, or a `HEAD` that is not `origin/main`; it waits for
   CI's run on that commit (`scripts/wait-for-ci.sh`, which needs `gh`) and
   tags only if it passed. CI has run the whole suite there, race detector
   included, so the release does not run it again.

The tag starts `.github/workflows/release.yml`, which runs GoReleaser
(`.goreleaser.yml`):

- archives for Linux, macOS and Windows on amd64 and arm64, each with the
  licence, README, changelog, `exemplar-config.toml` and the `skills/` folder, plus `checksums.txt` and an
  SPDX SBOM per archive;
- keyless cosign signatures of the checksums and SBOMs, and a GitHub build
  provenance attestation;
- a multi-arch image (`linux/amd64`, `linux/arm64`) at
  `ghcr.io/martinsuchenak/exemplar`, tagged with the version, the minor
  version and `latest` (not for pre-releases), built from the release
  binaries by `Dockerfile.release` and signed with cosign;
- the macOS binaries signed with a Developer ID certificate and notarized by
  Apple, when the `MACOS_*` secrets are set (see below); unsigned otherwise;
- a Homebrew cask in `martinsuchenak/homebrew-tap`, which needs the
  `TAP_GITHUB_TOKEN` secret (a token that can push to that repository);
- the GitHub release, with its changelog section as the notes.

Verify a download with `cosign verify-blob --bundle checksums.txt.sigstore.json
checksums.txt` (with `--certificate-identity-regexp
'https://github.com/martinsuchenak/exemplar/'` and
`--certificate-oidc-issuer https://token.actions.githubusercontent.com`), or
`gh attestation verify <archive> --repo martinsuchenak/exemplar`.

To try the release configuration without publishing anything:

```bash
goreleaser check
goreleaser release --snapshot --clean --skip=sign,sbom,docker
```

### macOS signing and notarization

The release signs the macOS binaries with a Developer ID certificate and
notarizes them with Apple, on the Linux runner. It needs an Apple Developer
Program membership and these repository secrets; while `MACOS_SIGN_P12` is
unset, the step is skipped and the binaries ship unsigned (the Homebrew cask
still lifts the quarantine on install).

| Secret | What it is |
|--------|-----------|
| `MACOS_SIGN_P12` | The Developer ID Application certificate and its private key, as a base64 `.p12` |
| `MACOS_SIGN_PASSWORD` | The password the `.p12` was exported with |
| `MACOS_NOTARY_ISSUER_ID` | The App Store Connect API issuer ID (a UUID) |
| `MACOS_NOTARY_KEY_ID` | The API key's ID |
| `MACOS_NOTARY_KEY` | The API key (`AuthKey_<id>.p8`), base64 |

One certificate and key serve every project of the same Apple team, so the
ones another project already uses work here; GitHub secrets are per
repository, so set them again on this one. To create them:

1. **Certificate.** In [Certificates, Identifiers & Profiles](https://developer.apple.com/account/resources/certificates/list),
   add a **Developer ID Application** certificate (only the account holder
   can). Install it, then in Keychain Access export the certificate together
   with its private key as a `.p12`, with a password.
2. **API key.** In [App Store Connect](https://appstoreconnect.apple.com/access/integrations/api),
   under Users and Access → Integrations → App Store Connect API, create a
   team key with the Developer role. Note the issuer ID and the key ID, and
   download the `.p8` (it can only be downloaded once).
3. **Secrets.** From the files:

   ```bash
   base64 -i DeveloperID.p12 | gh secret set MACOS_SIGN_P12 -R martinsuchenak/exemplar
   gh secret set MACOS_SIGN_PASSWORD -R martinsuchenak/exemplar
   gh secret set MACOS_NOTARY_ISSUER_ID -R martinsuchenak/exemplar
   gh secret set MACOS_NOTARY_KEY_ID -R martinsuchenak/exemplar
   base64 -i AuthKey_XXXXXXXXXX.p8 | gh secret set MACOS_NOTARY_KEY -R martinsuchenak/exemplar
   ```

   Then delete the local `.p12` and `.p8` copies you no longer need.

The binaries are notarized but not stapled (a bare binary can't carry a
ticket), so the first launch of a downloaded copy checks with Apple online.
To check a downloaded binary:

```bash
codesign --verify --verbose=2 exemplar   # "valid on disk" and "satisfies its Designated Requirement"
spctl --assess --type open --context context:primary-signature --verbose=2 exemplar   # "accepted", "source=Notarized Developer ID"
```

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

- **Anonymiser tests** run the engine with an in-memory registry and a
  detector that tags given words as names. Outside the package, use
  `internal/anonymise/anonymisetest`: `Detector{Names: …}`, `Key` and `Email(…)`
  for small real-looking emails. Configure an app with
  `testutil.NewAppWith(t, func(c *app.Config) { c.AnonKey = anonymisetest.Key; c.AnonDetector = … })`.
  Tests assert that no original value survives anywhere in the output.
  `TestEmailGolden` pins the email output byte for byte: 70 emails (the
  hand-made ones in the tests, and generated ones of every shape for both
  profiles), inputs and outputs stored in
  `internal/anonymise/testdata/emails-golden.json`. A refactor must leave it
  passing. Only a deliberate change in output may rewrite the file, with
  `go test ./internal/anonymise -run TestEmailGolden -update`, and the
  commit says why. Each document format has a sample in `document_test.go`
  holding the same secrets in every place that format can hide them; a new
  format adds one, plus the structure it must keep and its parse errors.
  DOCX has a Word-shaped fixture in `docx_test.go` (split runs, tracked
  changes, comments, reviewers, properties, images, an embedded workbook,
  macros); a new part rule adds its secrets there. RTF has one in
  `rtf_test.go` (split runs, escapes, fields, comments, `\info`, shapes,
  pictures, object data).

- **SMTP tests** use `internal/mailpit/mailpittest`, an in-process SMTP server
  that records envelopes and data, can require AUTH PLAIN and can reject a
  recipient.

- **MCP tests** (`cmd/mcp/mcp_test.go`) serve `Handler(app)` with
  `httptest`, generate data, and call the tools with the MCP library's own
  client and a bearer key, checking results, access rules and error codes.

- **Remote CLI tests** start the same server with `startServer(t)` (or
  `startServerWith(t, configure)` to change the app config) in
  `cmd/remote_test.go` and run commands with `s.run(t, key, args...)` or decode
  `--json` output with `s.runJSON`. `s.member(t, name)` creates a member and
  returns its key.

Not covered by tests: `main` and the blocking listeners in `serve` and
`StartMCPServer` (the servers they run are tested).
