# Backup, restore, export and import

Two jobs share one archive format:

| | Backup / restore | Export / import |
|---|---|---|
| For | Recovering a whole instance | Moving datasets between instances (local ↔ shared) |
| Run | On the host, like `bootstrap` | Remotely, with an API key, over REST |
| Holds | Everything: accounts, key hashes, audit log, identity tables, every dataset and content | Chosen datasets, their fixtures, the people they link to, their contents |
| Access rules | None: it is the operator | The usual: export what you can read, import where you may write |
| IDs | Kept exactly | Kept, so importing an archive twice adds nothing the second time |
| Never included | The anonymisation key | Accounts, keys, audit log, identity tables, the anonymisation key |

Neither is encrypted by exemplar. Keep archives on encrypted storage: a backup
holds API key hashes, the audit log and quarantined (anonymised, unreviewed)
emails.

## The archive

A gzip-compressed tar (`.tar.gz`). The first entry is `manifest.json`; then
record files, an optional database, and contents named by their SHA-256:

```
manifest.json      format, version, kind (export or backup), created_at,
                   app_version, schema_version, counts
datasets.jsonl     export: one JSON record per line
persons.jsonl
fixtures.jsonl
exemplar.db        backup: SQLite snapshot
blobs/<sha256>     contents
```

Readers reject anything else: other names, paths, links, newer format
versions, and contents whose bytes do not match their name. The manifest's
counts let a reader tell a truncated archive from a complete one. The CLI
writes archives through `<file>.partial`, renaming only when complete, and
never overwrites a file.

## Export and import

```bash
exemplar export <dataset>... --output team-fixtures.tar.gz
exemplar import team-fixtures.tar.gz        # against another server
```

**Export** takes up to 50 datasets the key can read, with every fixture in
them, the registry people those fixtures link to, and their contents. A
`dataset.export` audit event records each dataset; reading real-derived
content is logged as a download, as usual.

**Import** adds the datasets, keeping their IDs, names, versions, tags and
frozen state; the importing account becomes the owner. Then:

- A dataset already here (same ID) gets only the fixtures it lacks. Adding
  fixtures to one that is frozen here is refused, and so is a fixture whose
  ID is already here in another dataset.
- Datasets are added parents first, so fork and promote links between
  datasets in the same archive survive whatever order they were exported in.
- A different dataset with the same name and version is a conflict; rename
  one first.
- Global and quarantine datasets need an admin key, as when creating them.
- People are matched first by ID, then by email (two instances generating with
  one seed make the same people), and added otherwise. Only people a fixture
  links are added, but every person record is checked: a record with an
  unsafe ID, no first name or an address off `.example`, or one that appears
  twice, refuses the whole archive (`person.CheckImport`), so a hand-made
  archive is told about it rather than the record being left out silently.
- Fixtures keep their provenance: source, generator, anonymisation profile and
  key id, ingest batch. `exemplar purge <batch>` on the importing instance
  removes imported real-derived fixtures too.
- A `dataset.import` audit event records each dataset with what was added,
  what was already here and what was skipped.

Contents are stored as they stream in, without pausing garbage collection
(a slow upload would otherwise stall every other write); they are registered
just before the records are added, in one transaction at the end. A failed
import adds no records, and the contents it stored are removed by the next
garbage collection.

The records transaction of a large import (up to 100,000 records per file)
can hold the database's write lock for many seconds; other writers queue for
it rather than fail, because SQLite's `busy_timeout` is 60 seconds. On slow
storage, keep imports well below the record caps. Content stored for an
upload whose registration fails anyway is removed at once, so it cannot be
left behind where garbage collection would never find it.

Restore refuses a backup whose database holds a malformed content hash, in
the `blobs` table or a fixture, so a hand-made backup cannot plant rows that
break garbage collection.

An archive is capped at `storage.max_import_mb` (default 2048 MiB; `413` over
it), each record file at 64 MiB and 100,000 records. Because that limit is on
the compressed upload, the archive may also expand to at most 10 times it
uncompressed, and each content entry must fit the fixture limit
(`storage.max_fixture_mb`), checked from its header before any of it is read.
So a small, highly compressed archive (a gzip bomb) is refused in moments
instead of keeping the server decompressing.

### Real-derived fixtures

- Only approved ones are exported; others are counted as skipped.
- They arrive **approved**: the review made on the exporting instance is
  trusted. The reviewer is not an account here, so `reviewed_by` stays empty
  and `imported_reviewer` holds the source instance's reviewer account ID;
  `reviewed_at` is kept.
- Importing them needs an admin key. With a member key they are skipped and
  listed, and the rest of the archive is imported.
- A real-derived fixture that is not approved fails the whole import, since
  exemplar never writes one.

## Backup

```bash
exemplar backup --output /backups/exemplar-$(date +%F).tar.gz
```

Runs on the host with the storage flags (`--db-path`, `--blob-dir`), and is
safe while the server runs:

- It never migrates the database: it refuses one whose schema is not exactly
  its own build's, so a newly installed binary cannot change the schema under
  a server still running the old one. Back up with the build the server runs.
- The database is copied with SQLite's `VACUUM INTO`, a consistent snapshot
  taken in one transaction.
- Every content the snapshot's fixtures use is copied and checked against its
  hash.
- While it runs, a `backup.lock` file in the blob directory stops content
  garbage collection in every process, the server included; collections
  catch up afterwards. A lock older than 24 hours is treated as left over
  from a crash. Only one backup runs at a time.
- An `instance.backup` audit event records it.

The anonymisation key is not in the backup. Keep it with your other secrets:
without it, emails ingested after a restore get new fake people instead of
matching the ones already there.

## Restore

```bash
exemplar restore /backups/exemplar-2026-10-26.tar.gz             # into an empty instance
exemplar restore /backups/exemplar-2026-10-26.tar.gz --overwrite # replace this instance
```

Stop the server first. Restore:

1. Refuses a target that already holds an instance, unless `--overwrite` is
   given. A non-empty write-ahead log (`exemplar.db-wal`) counts: a server
   stopped before its first checkpoint keeps its data there. Empty leftover
   `-wal` and `-shm` files are removed, so SQLite never reads them as the
   restored database's log.
2. Refuses a backup whose schema is newer than this build; older ones are migrated.
3. Unpacks everything beside the target, checking every content's hash, the
   database's integrity, and that every fixture's content is present.
4. Only then moves the existing database and blob directory aside, to
   `<path>.pre-restore-<time>`, and moves the restored ones into place. If a
   move fails, the old instance is put back.
5. Records `instance.restore` in the restored audit log.

Nothing is deleted: remove the `.pre-restore-*` copies once you no longer
need them. A blob directory that holds other files but no content is never
removed; restore stops instead.
