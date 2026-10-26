-- Links from real identities to registry people, for anonymisation. Only
-- keyed HMACs of real values are stored, never the values themselves, so the
-- tables cannot be read back into real names or addresses without the key.

CREATE TABLE identity_keys (
    hash       TEXT PRIMARY KEY,
    person_id  TEXT NOT NULL REFERENCES persons (id),
    key_id     TEXT NOT NULL,
    created_at TEXT NOT NULL
);
CREATE INDEX identity_keys_person ON identity_keys (person_id);

-- The fake name token chosen for a real first or last name, so "Sarah" maps
-- to the same fake name in every email and every run.
CREATE TABLE name_tokens (
    hash       TEXT PRIMARY KEY,
    value      TEXT NOT NULL,
    key_id     TEXT NOT NULL,
    created_at TEXT NOT NULL
);
