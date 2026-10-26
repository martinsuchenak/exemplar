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
- They start `unreviewed`; an admin key approves or rejects them (`fixture.approve` / `fixture.reject`).
- Unapproved real-derived content is served only to admins, so they can review it.
- Every read of real-derived content is audit-logged (`fixture.download`).
- Uploads through the API or CLI are declared as containing no real data; real data must come through ingest.
- Copies made by fork or promote keep the source's provenance (`ingest_batch`, review state, reviewer).
- `exemplar purge <batch>` (admin) deletes a batch's fixtures from every dataset, frozen ones included, for removal on request.

## Audit log

`audit_events` is append-only: SQLite triggers reject every `UPDATE` and
`DELETE`. Events are written in the same transaction as the change they record.
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
| `fixture.purge` | An ingest batch is purged; details record how many fixtures |
| `fixture.generate` | Synthetic content is generated into a dataset; details record kind, count, seed and people |
| `person.generate` | People are added to the registry, directly or for generated content |

## Files on disk

- The database file is created with mode 600, its directory 700.
- Blob directories are 700 and blob files 600.
- Blob paths are derived only from validated SHA-256 hashes, so no request can reach other paths.
- Fixture content is capped (`storage.max_fixture_mb`, default 100 MiB); larger uploads stop before they are stored.

## HTTP

- Downloads are attachments with `X-Content-Type-Options: nosniff` and `Content-Security-Policy: sandbox`, so fixture HTML or scripts never run in a browser.
- Key creation responses carry `Cache-Control: no-store`.
- The CLI reads the API key from `--api-key` or `EXEMPLAR_API_KEY`, never from the config file, so keys are not committed with config.

## SOC 2 good practice

No formal sign-off is required yet; the service follows these practices:

- Least privilege through key scopes and the admin rule above.
- Secrets stored only as hashes; shown once.
- An append-only audit trail of security-relevant actions.
- Real-derived data quarantined until a named admin approves it.
- Owner-only file permissions. Run instances that hold real-derived data on encrypted disks, and behind TLS whenever they listen beyond localhost.
