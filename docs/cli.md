# CLI

```
exemplar [--config FILE] [--log-level LEVEL] [--log-format FORMAT] <command> [flags]
```

Each setting can come from a flag, an environment variable or
`exemplar-config.toml`.

## Storage flags

Commands that open the database directly (`serve`, `bootstrap`) take:

| Flag | Env var | Config | Default |
|------|---------|--------|---------|
| `--db-path` | `DB_PATH` | `database.path` | `data/exemplar.db` |
| `--blob-dir` | `BLOB_DIR` | `storage.blob_dir` | `data/blobs` |

## Commands

### `serve`

Opens the database (running pending migrations) and starts the HTTP API and the
MCP server.

| Flag | Env var | Config | Default |
|------|---------|--------|---------|
| `--server-host` | `SERVER_HOST` | `server.host` | `0.0.0.0` |
| `--server-port` | `SERVER_PORT` | `server.port` | `8080` |

### `bootstrap`

Creates the first admin account and prints its key once. Run it on the host or
in the container; it refuses once an admin exists.

| Flag | Default | Description |
|------|---------|-------------|
| `--name` | `admin` | Admin account name |
| `--kind` | `developer` | `developer`, `team` or `app` |
| `--recover` | off | Revoke the named admin's keys and print a new admin key |

```bash
exemplar bootstrap
exemplar bootstrap --recover --name admin
```

### `init`

Writes a default `exemplar-config.toml` in the current directory. Refuses to
overwrite an existing file.

### `completion`

Prints shell completion setup: `--shell bash|zsh|fish`.
