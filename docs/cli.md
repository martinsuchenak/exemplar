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
| `--max-ingest-mb` | `MAX_INGEST_MB` | `storage.max_ingest_mb` | `200` (0 for no limit); `serve` only |

### `serve`

Opens the database (running pending migrations) and starts the HTTP API and the
MCP server.

| Flag | Env var | Config | Default |
|------|---------|--------|---------|
| `--server-host` | `SERVER_HOST` | `server.host` | `0.0.0.0` |
| `--server-port` | `SERVER_PORT` | `server.port` | `8080` |
| `--mcp-host` | `MCP_HOST` | `mcp.host` | `0.0.0.0` |
| `--mcp-port` | `MCP_PORT` | `mcp.port` | `9000` (0 turns MCP off; see [mcp.md](mcp.md)) |
| `--anon-key-file` | `ANON_KEY_FILE` | `anonymiser.key_file` | none; or the key itself in `EXEMPLAR_ANON_KEY` |
| `--presidio-url` | `PRESIDIO_URL` | `anonymiser.presidio_url` | none |
| `--anon-profiles-dir` | `ANON_PROFILES_DIR` | `anonymiser.profiles_dir` | none: built-in profiles only |

Ingest of real email is on only with both an anonymisation key and a Presidio
URL. The key file must not be readable by group or others (`chmod 600`), or
`serve` refuses to start. See [anonymisation.md](anonymisation.md).

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
exemplar generate <dataset-id> --kind files|emails|records [--count 10] [--formats pdf,docx] [--shapes reply,bounce] [--from <fixture-id>,...] [--schemas contact,company] [--profile insurance|manufacturing] [--people N] [--seed N] [--scenario S] [--tags a,b]
```

Fills a dataset with synthetic content about registry people and prints the
seed; see [generation.md](generation.md). `--from` renders emails from
template emails (approved real-derived ones, or any email with linked
people) instead of shapes.

### `upload`

```bash
exemplar upload <dataset-id> <file-or-directory>... [--scenario S] [--tags a,b] [--kind K] [--source uploaded|synthetic|public-corpus] [--generator G]
```

Each file becomes a fixture named after the file. A directory uploads the
regular files directly inside it (not subdirectories), in name order. If some
files fail, the rest still upload; the failures are printed to stderr and the
command exits non-zero. Uploading declares the files hold no real data.

### `push-mailpit`

```bash
exemplar push-mailpit <dataset-id> [--smtp localhost:1025] [--run ID] [--to a@x.test,b@x.test] [--scenario S] [--tag T] [--limit N] [--raw] [--dry-run]
```

Sends a dataset's `.eml` fixtures to Mailpit (or any SMTP server) for a test
run. The messages are downloaded through the REST API, so the key's access
rules apply, and their stored bytes are sent unchanged except for three
headers added at the top:

- `X-Tags: exemplar-run-<run>`, which Mailpit turns into a tag, so a test finds
  its own messages with the search `tag:exemplar-run-<run>`;
- `X-Exemplar-Run: <run>` and `X-Exemplar-Fixture: <fixture-id>`.

| Flag | Env var | Config | Description |
|------|---------|--------|-------------|
| `--smtp` | `MAILPIT_SMTP` | `mailpit.smtp` | SMTP address, default `localhost:1025` |
| `--smtp-user`, `--smtp-password` | `MAILPIT_SMTP_USER`, `MAILPIT_SMTP_PASSWORD` | | AUTH PLAIN, if Mailpit requires it |
| `--run` | | | Run ID; default a new one such as `20261027-091502-3fa1`, printed |
| `--to` | | | Deliver every message to these addresses instead of its own To, Cc and Bcc. Headers are unchanged; Mailpit lists the address as Bcc |
| `--scenario`, `--tag`, `--limit` | | | Which emails to send |
| `--raw` | | | Send the stored bytes exactly, without the added headers |
| `--dry-run` | | | Show what would be sent |

The envelope sender is the message's From address. Each message is sent on its
own connection; failures are listed per message and make the command exit
non-zero. Only `.eml` fixtures are sent (`.msg` cannot be sent over SMTP).

```bash
exemplar generate <dataset-id> --kind emails --count 24
exemplar push-mailpit <dataset-id> --run "$CI_JOB_ID"
# then in the test: GET http://mailpit:8025/api/v1/search?query=tag:exemplar-run-$CI_JOB_ID
```

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

### `ingest`

```bash
exemplar ingest <dataset-id> <file-or-directory>... [--profile P] [--batch B] [--dry-run]
```

Admin key. Sends real `.eml` files to the server, which anonymises them and
stores them in the quarantine dataset as unreviewed fixtures named
`<batch>-001.eml`, `<batch>-002.eml`, …. Without `--batch` a new batch name is
made up and printed; purge takes it. Directories are read as for `upload`.

`--dry-run` stores nothing and prints every replacement, original next to
fake. That output is original personal data: read it, don't save or share it.
A file that fails (a `.msg`, an unsupported charset) is reported and the rest
go ahead; the command then exits non-zero. See
[anonymisation.md](anonymisation.md).

### `approve`, `reject`

```bash
exemplar approve <fixture-id>...
exemplar reject <fixture-id>...
```

Admin key. Records the review of real-derived fixtures. Only approved fixtures
are copied by fork and promote; rejected ones stay in quarantine until the
dataset or batch is deleted. Each id is reviewed on its own; failures are
listed and make the command exit non-zero.

## Example

```bash
exemplar datasets create --name replies
exemplar upload 01a1... ./samples/replies --scenario outlook-reply --tags reply
exemplar fixtures list 01a1...
exemplar fork <global-dataset-id> --name my-emails
```
