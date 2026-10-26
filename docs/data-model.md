# Data model

IDs are UUIDv7 strings, so they sort by creation time. Timestamps are RFC 3339
UTC strings with nanoseconds, so they sort as text. String lists (tags) are JSON
arrays. Foreign keys are enforced.

## Tables

### accounts

| Column | Notes |
|--------|-------|
| `id` | UUIDv7 |
| `name` | Unique; 1–64 letters, digits, `.`, `_`, `-` |
| `kind` | `developer`, `team` or `app` |
| `role` | `member` or `admin` |
| `created_at`, `disabled_at` | `disabled_at` set when the account is disabled |

### api_keys

| Column | Notes |
|--------|-------|
| `id`, `account_id` | Key belongs to one account |
| `name` | Label chosen by the creator |
| `prefix` | First 12 characters of the secret, for display |
| `hash` | SHA-256 of the secret; unique |
| `scopes` | Comma-separated: `read`, `write`, `admin` |
| `created_at`, `last_used_at`, `revoked_at` | |

### datasets

| Column | Notes |
|--------|-------|
| `id`, `name`, `description` | Name: 1–128 letters, digits, spaces, `.`, `_`, `-` |
| `scope` | `global`, `workspace` or `quarantine` |
| `owner_account_id` | The creator |
| `parent_id` | The dataset a fork or promotion was copied from |
| `version` | 1 for new datasets and forks; promotion takes the next free version of the global name |
| `tags` | JSON array, lower-cased and de-duplicated |
| `frozen_at` | Contents read-only once set |
| `created_at`, `updated_at` | |

Names are unique per `(name, version)` for global datasets and per
`(scope, owner, name, version)` otherwise.

### blobs

| Column | Notes |
|--------|-------|
| `hash` | SHA-256 hex; the file lives at `objects/<hash[0:2]>/<hash[2:4]>/<hash>` |
| `size`, `created_at` | |

### fixtures

| Column | Notes |
|--------|-------|
| `id`, `dataset_id`, `name` | Name unique within a dataset; no slashes |
| `kind` | `email`, `file` or `record`; inferred from the format when not given |
| `format`, `mime_type` | Format from the name's extension; MIME type inferred from the format |
| `blob_hash`, `size` | Content |
| `scenario`, `tags` | Free-form labels for search |
| `source` | `synthetic`, `uploaded`, `public-corpus` or `anonymised` |
| `generator`, `anon_profile`, `anon_key_id`, `ingest_batch` | Provenance |
| `imported_reviewer` | For a fixture imported already approved: the source instance's reviewer account ID (`reviewed_by` stays empty, as it must name a local account) |
| `review_state` | `not-required`, `unreviewed`, `approved` or `rejected` |
| `reviewed_by`, `reviewed_at` | Set by an admin's review |
| `created_by`, `created_at` | |
| `record_schema` | Records only: `contact`, `company`, `project`, `opportunity`, `address` or another lower-case name |

Deleting a dataset deletes its fixtures (`ON DELETE CASCADE`). Fork and
promote insert new fixture rows pointing at the same blobs, keeping
provenance and review fields.

### persons

The person registry: fake people that records, files and emails refer to.

| Column | Notes |
|--------|-------|
| `id` | UUIDv7 |
| `first_name`, `last_name`, `job_title`, `phone` | |
| `email` | Unique; on a `.example` domain; a clash gets a number (`jane.doe2@`) |
| `company` | Company name; the company's other details derive from it |
| `street`, `suburb`, `state`, `postcode`, `country` | Postal address |
| `created_by`, `created_at` | |

### fixture_persons

| Column | Notes |
|--------|-------|
| `fixture_id` | Deleted with the fixture |
| `person_id` | Must exist in `persons` |
| `role` | `from`, `to`, `cc`, `author`, `mentioned`, `contact`, `manager`, `resident`… |

The primary key is `(fixture_id, person_id, role)`. Fork and promote copy the
links with the fixture.

### identity_keys

Links real identities to registry people for the anonymiser, without storing
them.

| Column | Notes |
|--------|-------|
| `hash` | Primary key; HMAC of a real address or display name under the anonymisation key |
| `person_id` | The registry person it maps to; must exist in `persons` |
| `key_id` | The anonymisation key's id (`k-…`) |
| `created_at` | |

### name_tokens

| Column | Notes |
|--------|-------|
| `hash` | Primary key; HMAC of a real name token (`priya`, `shah`) |
| `value` | Its fake (`Tomasz`), so the same real name gets the same fake in every batch |
| `key_id`, `created_at` | |

Neither table holds a real value; see [anonymisation.md](anonymisation.md).

### audit_events

| Column | Notes |
|--------|-------|
| `seq` | Autoincrement; the order of events and the paging cursor |
| `id`, `at` | |
| `account_id` | Who acted; no foreign key, so events outlive accounts |
| `action`, `target_type`, `target_id` | See [security.md](security.md) for actions |
| `details` | JSON object |

Triggers reject every update and delete.

## Migrations

Migrations are embedded SQL files in `internal/db/migrations/`, listed in
`internal/db/migrations.go` and applied by `xdal/pkg/migrate` in the `core`
namespace when the database opens. Each runs in a transaction.

To change the schema, add a new file and a new entry with a later version
(`YYYYMMDDhhmmss`). Never edit a migration that has been applied: the migrator
records each one's checksum and refuses to run when they differ.
