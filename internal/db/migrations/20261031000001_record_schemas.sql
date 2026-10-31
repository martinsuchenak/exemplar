-- Record schemas added through the API. definition is the TOML file as
-- given; version counts its changes from 1. The server checks a file before
-- storing it and loads every row when it starts.
CREATE TABLE record_schemas (
    name       TEXT PRIMARY KEY,
    version    INTEGER NOT NULL,
    definition TEXT NOT NULL,
    created_at TEXT NOT NULL,
    created_by TEXT NOT NULL REFERENCES accounts (id),
    updated_at TEXT NOT NULL,
    updated_by TEXT NOT NULL REFERENCES accounts (id)
);
