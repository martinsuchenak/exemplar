# exemplar

A library of test fixtures: synthetic emails, documents and records about a
registry of fake people, and real ones anonymised through Presidio and
reviewed in quarantine before use. Served over a REST API, a read-only MCP
server and a CLI.

## Project Structure

```
.
├── main.go                  # Application entry point
├── cmd/                     # CLI commands (serve, bootstrap, init, completion)
├── cmd/routes/              # HTTP routes: handler wiring and auth only
├── cmd/mcp/                 # MCP server tools
├── internal/
│   ├── app/                 # Opens the database and blob store, builds the services
│   ├── account/             # Accounts and API keys (handler -> service -> storage)
│   ├── dataset/             # Datasets and their access rules
│   ├── fixture/             # Fixtures, content and review state
│   ├── blob/                # Content-addressed file store
│   ├── audit/               # Append-only audit log
│   ├── principal/           # The authenticated caller passed to services
│   ├── auth/                # API key middleware (calls the account service)
│   ├── db/                  # SQLite connection and migrations
│   └── rest/                # JSON helpers and service error mapping
├── skills/                  # Agent skills to install (exemplar-anonymise)
├── build/                   # Build information
├── exemplar-config.toml     # Configuration file
├── Dockerfile               # Docker build file
├── docker-compose.yml       # exemplar and Presidio on one machine
├── exemplar.nomad           # exemplar and Presidio on Nomad
└── Taskfile.yml             # Task runner
```

All database and file access, and every access check, happens in the service
layer. REST handlers, CLI commands and MCP tools get their services from
`internal/app` and pass the caller's `principal.Principal` to every call.

## Documentation

See [docs/](docs/README.md): architecture, security model, generation, data
model, CLI, API, MCP and development conventions.

To let an AI agent anonymise files without reading the originals, install
the [exemplar-anonymise skill](skills/exemplar-anonymise/SKILL.md); see
[docs/anonymisation.md](docs/anonymisation.md#anonymise-without-ingesting).

## Features

- **Datasets of fixtures** (global, workspace and quarantine) with accounts,
  scoped API keys, visibility rules and an audit log.
- **Synthetic data:** a registry of fake people and companies, and
  generated emails, documents and records about them, reproducible from a
  seed.
- **Real data, anonymised:** ingest emails and documents (text, Markdown,
  CSV, TSV, JSON, HTML, Twig and Jinja, DOCX, RTF, XLSX) into quarantine for
  review, or anonymise files on the spot without storing anything
  (`exemplar anonymise`, also `--local`).
- **Three interfaces** over the same services: REST, MCP (read-only) and
  the CLI.
- **Moving data:** export and import of datasets, backups and restores,
  and pushing fixtures to Mailpit.

## Install

```bash
brew install --cask martinsuchenak/tap/exemplar          # macOS
docker pull ghcr.io/martinsuchenak/exemplar:latest        # linux/amd64, linux/arm64
```

Or download an archive for Linux, macOS or Windows from the
[releases](https://github.com/martinsuchenak/exemplar/releases); each is
signed and has an SBOM. To deploy it with Presidio, see
[docs/deployment.md](docs/deployment.md).

## Quick Start

```bash
# Create the first admin account and key (run on the host; prints the key once)
go run . bootstrap

# Lost the admin key? Revoke its keys and issue a new one (audit-logged)
go run . bootstrap --recover --name admin

# Run the service
go run . serve

# Use the CLI against it
export EXEMPLAR_API_KEY=exm_...
go run . datasets create --name replies
go run . upload replies ./samples --scenario outlook-reply
go run . generate replies --kind emails --count 24
go run . push-mailpit replies --run my-test
go run . fixtures list replies

# Or the API
curl -H "X-API-Key: $EXEMPLAR_API_KEY" http://localhost:8080/api/v1/me
```

`bootstrap` opens the database directly, so it only works on the host or inside
the container (`docker exec`). It refuses to run once an admin exists.

To deploy it with Presidio, use `docker compose up -d --build` or the Nomad
job `exemplar.nomad`; see [docs/deployment.md](docs/deployment.md).

Back up with `exemplar backup --output FILE` (also host-only, safe while the
server runs) and move datasets between instances with `exemplar export` and
`exemplar import`; see [docs/backup-and-exchange.md](docs/backup-and-exchange.md).

## Configuration

Configuration is loaded from `exemplar-config.toml`, in the current directory or `~/.config/exemplar/`, and can be overridden via environment variables or flags.

| Option | Env Var | Default | Description |
|--------|---------|---------|-------------|
| `log.level` | `LOG_LEVEL` | `info` | Log level (debug, info, warn, error) |
| `log.format` | `LOG_FORMAT` | `text` | Log format (text, json) |
| `server.host` | `SERVER_HOST` | `0.0.0.0` | HTTP server host |
| `server.port` | `SERVER_PORT` | `8080` | HTTP server port |
| `database.path` | `DB_PATH` | `data/exemplar.db` | SQLite database file (created with mode 600) |
| `storage.blob_dir` | `BLOB_DIR` | `data/blobs` | Fixture contents, stored once per SHA-256 |
| `storage.max_fixture_mb` | `MAX_FIXTURE_MB` | `100` | Largest fixture accepted (0 for no limit) |
| `storage.max_ingest_mb` | `MAX_INGEST_MB` | `200` | Largest ingest request (0 for no limit) |
| `storage.max_import_mb` | `MAX_IMPORT_MB` | `2048` | Largest import archive (0 for no limit) |
| `anonymiser.key_file` | `ANON_KEY_FILE` | | Anonymisation key file, mode 600; or the key in `EXEMPLAR_ANON_KEY` |
| `anonymiser.presidio_url` | `PRESIDIO_URL` | | Presidio Analyzer, e.g. `http://localhost:3000`; with a key, turns ingest on |
| `anonymiser.profiles_dir` | `ANON_PROFILES_DIR` | | Extra anonymisation profiles (`*.toml`) |
| `mcp.enabled` | `MCP_ENABLED` | `true` | Read-only MCP endpoint at `/mcp` on the server port |
| `client.server` | `EXEMPLAR_SERVER` | `http://localhost:8080` | Server the remote CLI commands call |
| `mailpit.smtp` | `MAILPIT_SMTP` | `localhost:1025` | Where `push-mailpit` sends emails |
| (none) | `EXEMPLAR_API_KEY` | | API key for remote CLI commands |

## Dependencies

Key dependencies used in this project:

- [github.com/paularlott/cli](https://github.com/paularlott/cli) - CLI framework
- [github.com/paularlott/logger](https://github.com/paularlott/logger) - Structured logging
- [net/http](https://pkg.go.dev/net/http) - HTTP server
- [github.com/paularlott/mcp](https://github.com/paularlott/mcp) - MCP server
- [github.com/martinsuchenak/xdal](https://github.com/martinsuchenak/xdal) - Database abstraction and query builder
- [modernc.org/sqlite](https://modernc.org/sqlite) - SQLite driver

## License

Apache License 2.0 - see [LICENSE](LICENSE) and [NOTICE](NOTICE).
