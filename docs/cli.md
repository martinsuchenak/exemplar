# CLI

```
exemplar [--config FILE] [--log-level LEVEL] [--log-format FORMAT] <command> [flags]
```

Each setting can come from a flag, an environment variable or
`exemplar-config.toml`, in that order.

## Config file

`exemplar-config.toml` (or `.exemplar-config.toml`) is read from the
current directory, or failing that from your config directory:
`$XDG_CONFIG_HOME/exemplar/`, or `~/.config/exemplar/`, on Linux and macOS,
and `%AppData%\exemplar\` on Windows. Only the first one found is read.
`--config` (`CONFIG_FILE`) names another file; a path with a directory in
it is read only where it points.

The config directory suits the CLI on your own machine: point it at your
server once instead of exporting `EXEMPLAR_SERVER` in every shell. The API
key is never read from a config file.

```toml
# ~/.config/exemplar/exemplar-config.toml
[client]
server = "https://exemplar.example.internal"
```

A server started in a directory without a config file reads this one too,
so keep server settings next to the server.

Two kinds of command:

- **Host commands** (`serve`, `bootstrap`, `init`, `backup`, `restore`) open the database directly.
- **Remote commands** (everything else) call a server's REST API with an API
  key. They work the same against a local instance and a shared server, and
  the server's services enforce every permission. `anonymise --local` is the
  one exception: it runs the anonymiser in the CLI, with no server or key.

A command that fails prints one line on stderr, `exemplar: <message>`, and
exits 1. When the command line was wrong (a missing argument, an unknown
flag, a bad value) a second line names the command's help, for example
`Run 'exemplar promote --help' for usage.` Logs (`--log-level`,
`--log-format`) are the server's; command errors are not log entries.

## Host commands

### Storage flags

| Flag | Env var | Config | Default |
|------|---------|--------|---------|
| `--db-path` | `DB_PATH` | `database.path` | `data/exemplar.db` |
| `--blob-dir` | `BLOB_DIR` | `storage.blob_dir` | `data/blobs` |
| `--max-fixture-mb` | `MAX_FIXTURE_MB` | `storage.max_fixture_mb` | `100` (0 for no limit) |
| `--max-ingest-mb` | `MAX_INGEST_MB` | `storage.max_ingest_mb` | `200` (0 for no limit); `serve` only |
| `--max-import-mb` | `MAX_IMPORT_MB` | `storage.max_import_mb` | `2048` (0 for no limit); `serve` only |
| `--max-concurrent-runs` | `MAX_CONCURRENT_RUNS` | `storage.max_concurrent_runs` | `2` (0 for no limit): ingest and anonymise runs together, and record generations apart, each at most this many; `serve` only |

### `serve`

Opens the database (running pending migrations) and serves the REST API and
the MCP endpoint (`/mcp`) on one port. If it cannot listen (a port in use),
`serve` stops with an error. On SIGTERM (`docker stop`) or Ctrl-C it stops accepting requests, lets
those in flight finish for up to 30 seconds, and closes the database.

| Flag | Env var | Config | Default |
|------|---------|--------|---------|
| `--server-host` | `SERVER_HOST` | `server.host` | `0.0.0.0` |
| `--server-port` | `SERVER_PORT` | `server.port` | `8080` |
| `--mcp` | `MCP_ENABLED` | `mcp.enabled` | `true`: the MCP endpoint at `/mcp` on the server port (`--mcp=false` turns it off; see [mcp.md](mcp.md)) |
| `--anon-key-file` | `ANON_KEY_FILE` | `anonymiser.key_file` | none; or the key itself in `EXEMPLAR_ANON_KEY` |
| `--presidio-url` | `PRESIDIO_URL` | `anonymiser.presidio_url` | none |
| `--anon-profiles-dir` | `ANON_PROFILES_DIR` | `anonymiser.profiles_dir` | none: built-in profiles only |
| `--record-schemas-dir` | `RECORD_SCHEMAS_DIR` | `generate.record_schemas_dir` | none: built-in record schemas only; a file named like a built-in replaces it ([generation.md](generation.md#record-schema-files)) |

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

### `backup`

```bash
exemplar backup --output FILE
```

Writes the whole instance (database snapshot and contents) to a `.tar.gz`
archive. Safe while the server runs. It never migrates the database and
refuses one whose schema differs from its build. Never overwrites `FILE`. See
[backup-and-exchange.md](backup-and-exchange.md).

### `restore`

```bash
exemplar restore FILE [--overwrite]
```

Restores a backup into `--db-path` and `--blob-dir`. Stop the server first.
Refuses a target that already holds an instance unless `--overwrite` is
given; the existing data is then moved aside to `*.pre-restore-<time>`, never
deleted.

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
ends up in shell history. `--json` prints the API's JSON, for scripts. The
server can live in your [config file](#config-file) instead.

```bash
export EXEMPLAR_SERVER=https://exemplar.internal
export EXEMPLAR_API_KEY=exm_...
```

### Naming a dataset

Commands that take a `<dataset>` take its id or its exact name: `exemplar
generate crm-contacts --kind records`. A name means the newest version of a
global dataset, or a workspace, among the datasets your key can read. When
it names more than one (a global dataset and your fork of it, which keeps
its name, or, with an admin key, two accounts' workspaces), the command
stops and lists their ids; pass one of those. `datasets delete` takes an id
only.

`datasets list --query` and `fixtures list --query` find by words: each
word must appear, in any order, in a dataset's name, description or a tag,
or in a fixture's name. Whitespace and `-`, `_`, `.`, `/`, `,` separate
words, so `--query "crm contacts"` finds `crm-contacts`. ASCII letters
ignore case. A query is up to 200 characters and 8 words.

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
| `keys create --name N [--scopes read,write] [--account ID]` | Prints the secret once. Names follow the account name rules: 1-64 letters, digits, `.`, `_` or `-` |
| `keys revoke <key-id>` | Your own keys; any key with an admin key |

### `datasets`

| Command | Description |
|---------|-------------|
| `datasets list [--scope S] [--name N] [--query Q]` | Datasets you can read |
| `datasets create --name N [--scope workspace\|global\|quarantine] [--description D] [--tags a,b]` | Global and quarantine need an admin key |
| `datasets freeze <dataset>` | Make contents read-only |
| `datasets delete <dataset-id>` | Delete with all fixtures |

### `fixtures`

| Command | Description |
|---------|-------------|
| `fixtures list <dataset> [--kind] [--format] [--scenario] [--tag] [--review-state] [--record-schema] [--person ID] [--query Q] [--limit] [--offset]` | |
| `fixtures download <fixture-id> [--output FILE]` | To stdout, or a new file (never overwrites) |
| `fixtures download-all <dataset> --dir DIR [the list filters, without paging]` | Every match, as `DIR/<fixture name>` |

`download-all` creates `DIR` (mode 700) and writes each file with mode 600.
It checks every target first and writes nothing if one exists, so download
into a new or empty directory. A fixture that fails is reported and the rest
are still written; the command then exits non-zero. With an admin key it
writes unreviewed real-derived data too, and says so: delete it once
reviewed.

### `persons`

| Command | Description |
|---------|-------------|
| `persons list [--company C] [--limit] [--offset]` | People in the registry |
| `persons generate [--count 10] [--seed N]` | Add people; prints the seed |

### `schemas`

| Command | Description |
|---------|-------------|
| `schemas list` | The record schemas the server generates: name, step, source (`built-in`, `file`, or `api v<version>`), the file's hash, the lists a request must give and the fields (or `NOT LOADED:` and why, for an API schema that no longer loads); `--json` adds every list a schema picks from and the oldest exemplar it works with |
| `schemas show <name> [--api]` | The schema's file on stdout, so `> name.toml` saves it to edit; its source, version and hash on stderr; `--json` for all of it. A server file's (`--record-schemas-dir`) needs an admin key. `--api` shows the API schema of that name even where a server file has taken it |
| `schemas add <file>` | Add a schema from a file (admin key); every key can generate from it at once, with no restart |
| `schemas update <file> [--if-version N]` | Replace the API schema the file names (admin key); with `--if-version`, only if no one changed it since that version |
| `schemas remove <name>` | Remove an API schema (admin key), unless another uses its catalogue; fixtures made from it stay |

### `generate`

```bash
exemplar generate <dataset> --kind files|emails|records [--count 10] [--formats pdf,docx] [--shapes reply,bounce] [--from <fixture-id>,...] [--schemas contact,quote] [--schema-file a.toml,b.toml] [--lists lists.json] [--profile insurance|manufacturing] [--people N] [--seed N] [--scenario S] [--tags a,b]
exemplar generate --local --kind records [--schemas …] [--schema-file …] [--lists …] [--record-schemas-dir DIR] [--count 10] [--people N] [--seed N] [--profile …]
```

Fills a dataset with synthetic content about registry people and prints the
seed; see [generation.md](generation.md). `--from` renders emails from
template emails (approved real-derived ones, or any email with linked
people) instead of shapes.

For records, `--schema-file` sends record schema files with the request,
for it alone (with no `--schemas`, those are the ones generated), and
`--lists` a JSON object of lists by name, which replace the schemas' lists
or give the ones a schema says a request must (`request_lists`).
`--local` makes records in this process, about people made up from the
seed, with no server, key or dataset, and prints them on stdout as
`{"seed": N, "records": [...]}` (the seed also goes to stderr). It reads the
built-in schemas plus `--record-schemas-dir`, as `serve` does; without
`--local` that setting is ignored, since a server uses its own (so a config
file that sets it for `serve` does no harm). Numbers in `--lists` keep
their exact digits, locally and on a server. See
[generation.md](generation.md#records-for-another-system).

### `upload`

```bash
exemplar upload <dataset> <file-or-directory>... [--scenario S] [--tags a,b] [--kind K] [--source uploaded|synthetic|public-corpus] [--generator G]
```

Each file becomes a fixture named after the file. A directory uploads the
regular files directly inside it (not subdirectories), in name order. If some
files fail, the rest still upload; the failures are printed to stderr and the
command exits non-zero. Uploading declares the files hold no real data.

### `push-mailpit`

```bash
exemplar push-mailpit <dataset> [--smtp localhost:1025] [--run ID] [--to a@x.test,b@x.test] [--scenario S] [--tag T] [--limit N] [--raw] [--dry-run]
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
| `--smtp-user`, `--smtp-password` | `MAILPIT_SMTP_USER`, `MAILPIT_SMTP_PASSWORD` | | AUTH PLAIN, if Mailpit requires it. Sent without TLS, also to a non-local server such as `mailpit:1025` in CI (Mailpit needs `MP_SMTP_AUTH_ALLOW_INSECURE=true` for that); use test credentials only |
| `--run` | | | Run ID, 1–64 letters, digits, `.`, `_` or `-`; default a new one such as `20261027-091502-3fa1`, printed |
| `--to` | | | Deliver every message to these addresses instead of its own To, Cc and Bcc. Headers are unchanged; Mailpit lists the address as Bcc |
| `--scenario`, `--tag`, `--limit` | | | Which emails to send |
| `--raw` | | | Send the stored bytes exactly, without the added headers |
| `--dry-run` | | | Show what would be sent |

The envelope sender is the message's From address. Each message is sent on its
own connection; failures are listed per message and make the command exit
non-zero. Only `.eml` fixtures are sent (`.msg` cannot be sent over SMTP).
Real-derived emails that are not approved are never sent, even with an admin
key (which may read them only to review them); the command says how many it
skipped.

```bash
exemplar generate <dataset> --kind emails --count 24
exemplar push-mailpit <dataset> --run "$CI_JOB_ID"
# then in the test: GET http://mailpit:8025/api/v1/search?query=tag:exemplar-run-$CI_JOB_ID
```

### `fork`

```bash
exemplar fork <dataset> [--name N] [--description D]
```

Copies a dataset you can read into a new workspace you own. Contents are
shared, not duplicated.

### `promote`

```bash
exemplar promote <dataset> [--name N] [--description D]
```

Admin key. Copies a dataset into the global library as the next version of
that name, frozen. The name and description default to the source's; a
promotion from quarantine usually wants its own description, since the
quarantine one is about review and a frozen dataset cannot be described
again.

### `purge`

```bash
exemplar purge <ingest-batch> --yes
```

Admin key. Deletes every fixture from an ingest batch, in every dataset it was
copied to. It cannot be undone, so it needs `--yes`.

### `ingest`

```bash
exemplar ingest <dataset> <file-or-directory>... [--profile P] [--batch B] [--dry-run]
```

Admin key. Sends real emails and documents (`.eml`, `.txt`, `.md`, `.csv`,
`.json`, `.html`, `.twig`, `.docx`, `.rtf`, `.xlsx`, `.pdf` and the rest listed in
[anonymisation.md](anonymisation.md#documents)) to the server, which
anonymises them and stores them in the quarantine dataset as unreviewed
fixtures named `<batch>-001.eml`, `<batch>-002.csv`, …, keeping each file's
extension. Without `--batch` a new batch name is
made up and printed; purge takes it. Directories are read as for `upload`.

One run takes at most 1000 files and `storage.max_ingest_mb` in all. Send
more in several runs with the same `--batch`: each run numbers on after the
highest fixture the batch already holds in that dataset, so they stay one
batch to review and purge.

```bash
find ~/mail -name '*.eml' -print0 | xargs -0 -n 500 exemplar ingest q-inbox --batch inbox-2026-10
```

Fakes come from the server's key, so a name gets the same fake in every
run; but names are learned from the files of one run only (see
[anonymisation.md](anonymisation.md#workflow)), so keep a mail thread in
one run where you can.

`--dry-run` stores nothing and prints every replacement, original next to
fake. That output is original personal data: read it, don't save or share it.
A file that fails (a `.msg`, an unsupported charset) is reported and the rest
go ahead; the command then exits non-zero. See
[anonymisation.md](anonymisation.md).

### `anonymise`

```bash
exemplar anonymise <file-or-directory>... [--output FILE | --out-dir DIR] [--profile P] [--quiet] [--check] [--check-json FILE|-]
exemplar anonymise - --format F | --name N ...
exemplar anonymise ... --local [--presidio-url URL | --patterns-only] [--anon-key-file F] [--report]
exemplar anonymise ... --link-registry   # admin key
```

Any key, or none with `--local`. Anonymises the files and gives them back in
their own format, storing nothing. One file goes to stdout, or to `--output`
(never overwritten); several need `--out-dir` (empty or new) and are written
there under their fake names, as one batch sharing fakes. `-` reads stdin,
with `--format` (`md`, `csv`, `json`, …) or `--name` to say what it is.

Stdout carries only the file; a per-file summary (`--quiet` drops it) and a
warning that the output is not reviewed go to stderr, so the command pipes
cleanly into the next one (`… | exemplar anonymise - --format json | jq .`).
A file that fails writes nothing to stdout and makes the command exit
non-zero. `--report`, with
`--local` only, prints every replacement with its original on stderr.
`--check` lists on stderr what each output holds exactly as its input did
(addresses, long numbers, names, capitalised words), and `--check-json`
writes that list as JSON to a new file, or to stdout (`-`) when the files
go to `--output` or `--out-dir`; both work with a server too, as the
comparison runs on this machine. A `--check-json` file that exists, or
whose directory does not, fails the command before anything runs.
`--local` takes the anonymiser flags `serve` takes (`--presidio-url`,
`--anon-key-file`, `--anon-profiles-dir`), plus `--patterns-only` to run
without Presidio. `--link-registry` makes linked fakes from the server's key
and registry (admin key). A file that fails is reported and the rest go
ahead; the command then exits non-zero. See
[anonymisation.md](anonymisation.md#anonymise-without-ingesting).

### `approve`, `reject`

```bash
exemplar approve <fixture-id>...
exemplar reject <fixture-id>...
```

Admin key. Records the review of real-derived fixtures. Only approved fixtures
are copied by fork and promote; rejected ones stay in quarantine until the
dataset or batch is deleted. Each id is reviewed on its own; failures are
listed and make the command exit non-zero. `review` does the same one
fixture at a time, without copying ids.

### `review`

```bash
exemplar review <quarantine-dataset> [--kind K] [--query Q] [--no-open]
```

Admin key. Walks the dataset's unreviewed fixtures in name order. Each one
is downloaded to a new private temporary directory, opened in your system's
default app for its type (`open` on macOS, `xdg-open` on Linux, the file
association on Windows; an `.eml` opens in your mail client), and waits for
your answer:

| Answer | Does |
|--------|------|
| `a`, `approve` | Approve it, as `exemplar approve` would |
| `r`, `reject` | Reject it |
| `s`, `skip` | Leave it unreviewed for next time |
| `o`, `open` | Open it again |
| `q`, `quit` | Stop; the rest stay unreviewed |

Each local copy is deleted once you answer, and the directory when the walk
ends, however it ends. The app you open a file in may keep its own copy (a
mail client's import, a recent-files list), so check what yours does before
reviewing real data. `--no-open` prints each file's path instead, for
opening it yourself or over SSH; so does an opener that fails. The end of
input counts as quit. The walk ends with a tally: approved, rejected,
skipped and left.

### `export`, `import`

```bash
exemplar export <dataset>... --output FILE
exemplar import FILE
```

Export writes datasets the key can read, with their fixtures, linked people
and contents, to an archive; real-derived fixtures go only if approved.
Import adds an archive's datasets to the server, keeping IDs, so importing
twice adds nothing the second time. Global and quarantine datasets and
real-derived fixtures need an admin key to import; with a member key
real-derived fixtures are skipped and listed on stderr. See
[backup-and-exchange.md](backup-and-exchange.md).

## Example

```bash
exemplar datasets create --name replies
exemplar upload 01a1... ./samples/replies --scenario outlook-reply --tags reply
exemplar fixtures list 01a1...
exemplar fork <global-dataset> --name my-emails
```
