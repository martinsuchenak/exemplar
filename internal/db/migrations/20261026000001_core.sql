-- Core tables: accounts, API keys, datasets, blobs, fixtures and the audit log.
-- IDs are UUIDv7 strings; timestamps are RFC 3339 UTC strings.

CREATE TABLE accounts (
    id          TEXT PRIMARY KEY,
    name        TEXT NOT NULL UNIQUE,
    kind        TEXT NOT NULL CHECK (kind IN ('developer', 'team', 'app')),
    role        TEXT NOT NULL CHECK (role IN ('member', 'admin')),
    created_at  TEXT NOT NULL,
    disabled_at TEXT
);

CREATE TABLE api_keys (
    id           TEXT PRIMARY KEY,
    account_id   TEXT NOT NULL REFERENCES accounts (id),
    name         TEXT NOT NULL,
    prefix       TEXT NOT NULL,
    hash         TEXT NOT NULL UNIQUE,
    scopes       TEXT NOT NULL,
    created_at   TEXT NOT NULL,
    last_used_at TEXT,
    revoked_at   TEXT
);
CREATE INDEX api_keys_account ON api_keys (account_id);

CREATE TABLE datasets (
    id               TEXT PRIMARY KEY,
    name             TEXT NOT NULL,
    description      TEXT NOT NULL DEFAULT '',
    scope            TEXT NOT NULL CHECK (scope IN ('global', 'workspace', 'quarantine')),
    owner_account_id TEXT NOT NULL REFERENCES accounts (id),
    parent_id        TEXT REFERENCES datasets (id) ON DELETE SET NULL,
    version          INTEGER NOT NULL DEFAULT 1,
    tags             TEXT NOT NULL DEFAULT '[]',
    frozen_at        TEXT,
    created_at       TEXT NOT NULL,
    updated_at       TEXT NOT NULL
);
-- Global names are unique across the instance; other scopes per owner.
CREATE UNIQUE INDEX datasets_global_name ON datasets (name, version) WHERE scope = 'global';
CREATE UNIQUE INDEX datasets_owned_name ON datasets (scope, owner_account_id, name, version) WHERE scope <> 'global';

CREATE TABLE blobs (
    hash       TEXT PRIMARY KEY,
    size       INTEGER NOT NULL,
    created_at TEXT NOT NULL
);

CREATE TABLE fixtures (
    id           TEXT PRIMARY KEY,
    dataset_id   TEXT NOT NULL REFERENCES datasets (id) ON DELETE CASCADE,
    name         TEXT NOT NULL,
    kind         TEXT NOT NULL CHECK (kind IN ('email', 'file', 'record')),
    format       TEXT NOT NULL,
    mime_type    TEXT NOT NULL,
    blob_hash    TEXT NOT NULL REFERENCES blobs (hash),
    size         INTEGER NOT NULL,
    scenario     TEXT NOT NULL DEFAULT '',
    tags         TEXT NOT NULL DEFAULT '[]',
    source       TEXT NOT NULL CHECK (source IN ('synthetic', 'uploaded', 'public-corpus', 'anonymised')),
    generator    TEXT NOT NULL DEFAULT '',
    anon_profile TEXT NOT NULL DEFAULT '',
    anon_key_id  TEXT NOT NULL DEFAULT '',
    ingest_batch TEXT NOT NULL DEFAULT '',
    review_state TEXT NOT NULL CHECK (review_state IN ('not-required', 'unreviewed', 'approved', 'rejected')),
    reviewed_by  TEXT REFERENCES accounts (id),
    reviewed_at  TEXT,
    created_by   TEXT NOT NULL REFERENCES accounts (id),
    created_at   TEXT NOT NULL,
    UNIQUE (dataset_id, name)
);
CREATE INDEX fixtures_blob ON fixtures (blob_hash);
CREATE INDEX fixtures_ingest_batch ON fixtures (ingest_batch) WHERE ingest_batch <> '';

-- Append-only: the triggers refuse updates and deletes. account_id has no
-- foreign key so events outlive anything they refer to.
CREATE TABLE audit_events (
    seq         INTEGER PRIMARY KEY AUTOINCREMENT,
    id          TEXT NOT NULL UNIQUE,
    at          TEXT NOT NULL,
    account_id  TEXT NOT NULL DEFAULT '',
    action      TEXT NOT NULL,
    target_type TEXT NOT NULL DEFAULT '',
    target_id   TEXT NOT NULL DEFAULT '',
    details     TEXT NOT NULL DEFAULT '{}'
);
CREATE INDEX audit_events_account ON audit_events (account_id);
CREATE INDEX audit_events_target ON audit_events (target_type, target_id);

CREATE TRIGGER audit_events_no_update BEFORE UPDATE ON audit_events
BEGIN
    SELECT RAISE(ABORT, 'audit_events is append-only');
END;

CREATE TRIGGER audit_events_no_delete BEFORE DELETE ON audit_events
BEGIN
    SELECT RAISE(ABORT, 'audit_events is append-only');
END;
