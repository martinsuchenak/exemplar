# Security model

## Accounts, roles and keys

An **account** is whoever owns data: a developer, a team or an app (`kind`). Its
**role** is `member` or `admin`. Accounts act through **API keys**, sent in the
`X-API-Key` header.

| Key scope | Allows |
|-----------|--------|
| `read` | Reading datasets and fixtures the account can see |
| `write` | `read` plus creating and changing its own data and keys |
| `admin` | Admin actions; only valid on admin accounts |

A caller is an admin only when **both** the account role is `admin` and the key
carries the `admin` scope. An admin's everyday `read`/`write` key cannot manage
accounts, approve data or read the audit log.

Key rules enforced by `account.Service`:

- Members create keys only for themselves, and only with scopes the key they use already has. A read-only key cannot create a write key.
- Only an admin key can create keys for other accounts or accounts at all.
- The `admin` scope is only granted to admin accounts.
- Members can list and revoke only their own keys; revoking needs a write key.
- Disabling an account revokes all its keys. The last active admin cannot be disabled.

### Key format and storage

Keys are `exm_` followed by 52 lowercase base32 characters (256 random bits).
Only the SHA-256 of the key is stored; the high entropy is why a fast hash is
safe here. The first 12 characters (`prefix`) are stored to tell keys apart in
listings. The full key is shown once, when it is created.

`last_used_at` is updated at most once a minute per key.

## Bootstrap and recovery

`exemplar bootstrap` creates the first admin account and prints its key once.
It opens the database directly, so it only works on the host or in the
container (`docker exec`), never over the network. It refuses to run once any
active admin exists.

`exemplar bootstrap --recover --name <admin>` replaces a lost admin key: it
revokes every key of that admin account and prints a new admin key. Both actions
are audit-logged.

## Dataset visibility

| Scope | Who can read | Who can change | Holds |
|-------|--------------|----------------|-------|
| `global` | Every key with `read` | Admin keys | The curated library |
| `workspace` | Owner and admins | Owner and admins (with `write`) | An account's own data |
| `quarantine` | Admin keys | Admin keys | Unreviewed real-derived data |

A dataset a key cannot read is reported as not found. A **frozen** dataset's
contents cannot change; it can still be deleted by someone allowed to change it.

**Fork** copies a dataset the key can read into a new workspace owned by the
key's account (write key). **Promote** copies a dataset into the global
library as the next version of its name, frozen (admin key). Both skip
real-derived fixtures that are not approved, so unreviewed real data never
leaves quarantine. Members cannot promote yet; a proposal workflow may follow.

## Synthetic data and the person registry

Generated content is `source: synthetic` and needs no review. Every generated
address is on a reserved `.example` domain. Registry people are fake, so any
key that can read may list them; adding people or generating content needs a
write key, and generating into a dataset needs the right to change it.

## Real-derived data

Only fixtures with source `anonymised` are derived from real data.

- They can only be added to a quarantine dataset, and quarantine accepts nothing else.
- They start `unreviewed`; an admin key approves or rejects them (`fixture.approve` / `fixture.reject`). Freezing a quarantine dataset stops new content, not reviews.
- `push-mailpit` never sends real-derived emails that are not approved, even with an admin key.
- Unapproved real-derived content is served only to admins, so they can review it.
- Every read of real-derived content is audit-logged (`fixture.download`).
- Uploads through the API or CLI are declared as containing no real data; real data must come through ingest.
- Ingest (admin key, quarantine datasets only) keeps raw input in memory for the request and stores only anonymised output. Dry-run reports hold original data: they are returned with `Cache-Control: no-store` and never stored or logged; the audit event records counts only. Ingest and the anonymise tool share a cap on how many runs the server holds at once (`storage.max_concurrent_runs`, default 2; `429` beyond it, before any upload is read), so concurrent uploads cannot exhaust the server's memory together. Records generation has a cap of its own of the same size (`429` before any record is made), so a long anonymise batch never leaves generation busy.
- The anonymisation key is read from a file that group and others cannot read (the server refuses otherwise) or from `EXEMPLAR_ANON_KEY`, never from the config file. The registry keeps real identities only as HMACs under that key (`identity_keys`, `name_tokens`).
- Email attachments are replaced by generated files by default, so their content never survives. A profile with `attachments = "anonymise"` rewrites the ones in a format the anonymiser reads in place instead; they then keep their content, and are reviewed with the email in quarantine. Choosing that profile is a deliberate, per-ingest decision.
- The anonymise tool (`POST /api/v1/anonymise`, `exemplar anonymise`) is open to any key that can read, and stores nothing. Its fakes come from a key made for each request and thrown away, so they match nothing in the library: a caller cannot test whether a real name is there by comparing fakes. Linked fakes, from the server's key and registry, need an admin key. The response never holds original data (`--report` exists only in `--local` mode, where the user owns the files; dropped document parts are listed by Office's names or keyed ones, never the sender's), each key runs one at a time, claimed before its upload is read so one key cannot hold several uploads in memory, and the audit event records counts only. Its output skips review, so the CLI warns that it is unreviewed.
- Presidio receives email and document text in plain form; run it next to the server on a private network or loopback. When it fails, callers of ingest and the anonymise tool learn only that the detector is unavailable: the underlying error, which names Presidio's URL, host and network, is never returned. The server logs it (the status, network error or timeout) for its operators, never the file, its name, or what Presidio said back. See [anonymisation.md](anonymisation.md#presidio).
- Copies made by fork or promote keep the source's provenance (`ingest_batch`, review state, reviewer).
- `exemplar purge <batch>` (admin) deletes a batch's fixtures from every dataset, frozen ones included, for removal on request. That includes emails generated from the batch's emails as templates, which keep its `ingest_batch`.
- Export carries approved real-derived fixtures only. Importing them needs an admin key, because they arrive approved: the review made on the exporting instance is trusted, and its reviewer is recorded as `imported_reviewer`.
- Backups are host-only and hold everything except the anonymisation key, including key hashes and quarantined data. exemplar does not encrypt them; keep them on encrypted storage. See [backup-and-exchange.md](backup-and-exchange.md).
- Only approved real-derived emails can be templates, even for admins, since rendering copies their wording out of quarantine.

## Audit log

`audit_events` is append-only: SQLite triggers reject every `UPDATE` and
`DELETE`, and connections enable `recursive_triggers`, so the implicit delete
of a `REPLACE INTO` is rejected too. Events are written in the same transaction as the change they record.
Only admin keys can read the log.

| Action | Recorded when |
|--------|---------------|
| `account.bootstrap`, `account.recover` | The first admin is created; an admin key is recovered |
| `account.create`, `account.disable` | An admin creates or disables an account |
| `key.create`, `key.revoke` | A key is issued or revoked |
| `dataset.create`, `dataset.freeze`, `dataset.delete` | Dataset lifecycle |
| `dataset.fork`, `dataset.promote` | A dataset is copied; details record the source and counts |
| `fixture.create`, `fixture.delete` | Fixture lifecycle |
| `fixture.download` | Real-derived content is read |
| `fixture.approve`, `fixture.reject` | A real-derived fixture is reviewed |
| `fixture.ingest` | Real emails or documents are ingested; details record batch, profile, key id, dry run and counts |
| `file.anonymise` | Files are run through the anonymise tool; details record profile, whether fakes were linked (and then the key id), files, failures and formats |
| `dataset.export`, `dataset.import` | Datasets leave or arrive in an archive; details record counts, and for imports the source build and time |
| `instance.backup`, `instance.restore` | A backup is taken; an instance is restored (recorded in the restored log) |
| `fixture.purge` | An ingest batch is purged; details record how many fixtures |
| `fixture.generate` | Synthetic content is generated into a dataset; details record kind, count, seed and people |
| `person.generate` | People are added to the registry, directly or for generated content |
| `record_schema.create`, `record_schema.update`, `record_schema.delete` | An admin adds, changes or removes a record schema through the API; details record the version and the file's hash (and the one an update replaced), never the file |

## Record schemas

Only admin keys add, change or remove record schemas through the API, and
every change is audited. Any key that can read lists every schema and reads
the built-in files (they are public) and API schemas' files, so an API
schema should hold nothing a reader may not see. The server's own files
(`--record-schemas-dir`) belong to the deployment: only their names, fields
and list names are shown to every key, and their text to admin keys only.

Generation from any schema is bounded before the work is done: a `lines`
count is capped at 100 like a list's, every entry a `lines` field draws is
charged to the record's own value cap (100000 values), and a records
generation holds one of its own run slots (`storage.max_concurrent_runs` of them). A cancelled
caller stops the build at the next record.

Every parser is bounded by absolute budgets, not only by the size of its
input: RTF and Office XML by token, node and expansion budgets, and PDFs,
whose pages are rewritten in place, by how far a page's or form's content
may decode and by how many grid cells its text objects may be bucketed
into, with the same run-slot cap and deadlines over all of it. However well
a file compresses, it cannot multiply into many times its size in the
server's memory, and a file that takes too long is cut off mid-parse, not
only between engine calls.

## Files on disk

- The database file is created with mode 600, its directory 700.
- Blob directories are 700 and blob files 600.
- Blob paths are derived only from validated SHA-256 hashes, so no request can reach other paths.
- Fixture content is capped (`storage.max_fixture_mb`, default 100 MiB); larger uploads stop before they are stored.

## HTTP

- Downloads are attachments with `X-Content-Type-Options: nosniff` and `Content-Security-Policy: sandbox`, so fixture HTML or scripts never run in a browser.
- Key creation responses carry `Cache-Control: no-store`.
- The CLI reads the API key from `--api-key` or `EXEMPLAR_API_KEY`, never from the config file, so keys are not committed with config.
- Keys are accepted as `X-API-Key` or `Authorization: Bearer exm_…`; bearer tokens without the `exm_` prefix are not treated as keys.
- A key revoked (or an account disabled) while a large upload or download is
  still streaming stops that transfer: the authentication middleware re-checks
  the key roughly every 10 seconds for the duration of the transfer. A check
  that cannot run never interrupts a transfer on its own.

## MCP

The MCP server uses the same keys and the same services, so an agent can see
what its key can see over REST, except quarantine: the endpoint marks every
request `NoQuarantine`, and the dataset service then hides quarantine even
from an admin key, so agents never read unreviewed real-derived data. Its
tools only read, and the anonymise tool is not one of them, since real data
would pass through the agent first. Give agents a read-only key. Reading real-derived content through `get_fixture` is
audit-logged like a REST download. Details in [mcp.md](mcp.md).

## SOC 2 good practice

No formal sign-off is required yet; the service follows these practices:

- Least privilege through key scopes and the admin rule above.
- Secrets stored only as hashes; shown once.
- An append-only audit trail of security-relevant actions.
- Real-derived data quarantined until a named admin approves it.
- Owner-only file permissions. Run instances that hold real-derived data on encrypted disks, and behind TLS whenever they listen beyond localhost.
