# CLI

```
exemplar [--config FILE] [--log-level LEVEL] [--log-format FORMAT] <command> [flags]
```

Each setting can come from a flag, an environment variable or
`exemplar-config.toml`.

Two kinds of command:

- **Host commands** (`serve`, `bootstrap`, `init`) open the database directly.
- **Remote commands** (everything else) call a server's REST API with an API
  key. They work the same against a local instance and a shared server, and
  the server's services enforce every permission.

## Host commands

### Storage flags

| Flag | Env var | Config | Default |
|------|---------|--------|---------|
| `--db-path` | `DB_PATH` | `database.path` | `data/exemplar.db` |
| `--blob-dir` | `BLOB_DIR` | `storage.blob_dir` | `data/blobs` |
| `--max-fixture-mb` | `MAX_FIXTURE_MB` | `storage.max_fixture_mb` | `100` (0 for no limit) |

### `serve`

Opens the database (running pending migrations) and starts the HTTP API and the
MCP server.

| Flag | Env var | Config | Default |
|------|---------|--------|---------|
| `--server-host` | `SERVER_HOST` | `server.host` | `0.0.0.0` |
| `--server-port` | `SERVER_PORT` | `server.port` | `8080` |
| `--mcp-host` | `MCP_HOST` | `mcp.host` | `0.0.0.0` |
| `--mcp-port` | `MCP_PORT` | `mcp.port` | `9000` (0 turns MCP off; see [mcp.md](mcp.md)) |

### `bootstrap`

Creates the first admin account and prints its key once. It refuses once an
admin exists.

| Flag | Default | Description |
|------|---------|-------------|
| `--name` | `admin` | Admin account name |
| `--kind` | `developer` | `developer`, `team` or `app` |
| `--recover` | off | Revoke the named admin's keys and print a new admin key |

### `init`

Writes a default `exemplar-config.toml` in the current directory. Refuses to
overwrite an existing file.

### `completion`

Prints shell completion setup: `--shell bash|zsh|fish`.

## Remote commands

### Client flags

| Flag | Env var | Config | Default |
|------|---------|--------|---------|
| `--server` | `EXEMPLAR_SERVER` | `client.server` | `http://localhost:8080` |
| `--api-key` | `EXEMPLAR_API_KEY` | (never read from config) | |
| `--json` | | | off: print a table |

Keep the key in `EXEMPLAR_API_KEY` rather than on the command line, where it
ends up in shell history. `--json` prints the API's JSON, for scripts.

```bash
export EXEMPLAR_SERVER=https://exemplar.internal
export EXEMPLAR_API_KEY=exm_...
```

### `accounts`

| Command | Description |
|---------|-------------|
| `accounts list` | All accounts (admin key) or your own |
| `accounts create --name N [--kind developer\|team\|app] [--role member\|admin]` | Admin key |
| `accounts disable <account-id>` | Admin key; revokes the account's keys |

### `keys`

| Command | Description |
|---------|-------------|
| `keys list [--account ID]` | Your keys; another account's with an admin key |
| `keys create --name N [--scopes read,write] [--account ID]` | Prints the secret once |
| `keys revoke <key-id>` | Your own keys; any key with an admin key |

### `datasets`

| Command | Description |
|---------|-------------|
| `datasets list [--scope S] [--name N]` | Datasets you can read |
| `datasets create --name N [--scope workspace\|global\|quarantine] [--description D] [--tags a,b]` | Global and quarantine need an admin key |
| `datasets freeze <dataset-id>` | Make contents read-only |
| `datasets delete <dataset-id>` | Delete with all fixtures |

### `fixtures`

| Command | Description |
|---------|-------------|
| `fixtures list <dataset-id> [--kind] [--format] [--scenario] [--tag] [--review-state] [--record-schema] [--person ID] [--limit] [--offset]` | |
| `fixtures download <fixture-id> [--output FILE]` | To stdout, or a new file (never overwrites) |

### `persons`

| Command | Description |
|---------|-------------|
| `persons list [--company C] [--limit] [--offset]` | People in the registry |
| `persons generate [--count 10] [--seed N]` | Add people; prints the seed |

### `generate`

```bash
exemplar generate <dataset-id> --kind files|emails|records [--count 10] [--formats pdf,docx] [--shapes reply,bounce] [--schemas contact,company] [--profile insurance|manufacturing] [--people N] [--seed N] [--scenario S] [--tags a,b]
```

Fills a dataset with synthetic content about registry people and prints the
seed; see [generation.md](generation.md).

### `upload`

```bash
exemplar upload <dataset-id> <file-or-directory>... [--scenario S] [--tags a,b] [--kind K] [--source uploaded|synthetic|public-corpus] [--generator G]
```

Each file becomes a fixture named after the file. A directory uploads the
regular files directly inside it (not subdirectories), in name order. If some
files fail, the rest still upload; the failures are printed to stderr and the
command exits non-zero. Uploading declares the files hold no real data.

### `fork`

```bash
exemplar fork <dataset-id> [--name N] [--description D]
```

Copies a dataset you can read into a new workspace you own. Contents are
shared, not duplicated.

### `promote`

```bash
exemplar promote <dataset-id> [--name N]
```

Admin key. Copies a dataset into the global library as the next version of
that name, frozen.

### `purge`

```bash
exemplar purge <ingest-batch> --yes
```

Admin key. Deletes every fixture from an ingest batch, in every dataset it was
copied to. It cannot be undone, so it needs `--yes`.

## Example

```bash
exemplar datasets create --name replies
exemplar upload 01a1... ./samples/replies --scenario outlook-reply --tags reply
exemplar fixtures list 01a1...
exemplar fork <global-dataset-id> --name my-emails
```
