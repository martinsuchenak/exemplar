-- Person registry and the links from fixtures to the people they mention.

CREATE TABLE persons (
    id         TEXT PRIMARY KEY,
    first_name TEXT NOT NULL,
    last_name  TEXT NOT NULL,
    email      TEXT NOT NULL UNIQUE,
    phone      TEXT NOT NULL DEFAULT '',
    job_title  TEXT NOT NULL DEFAULT '',
    company    TEXT NOT NULL DEFAULT '',
    street     TEXT NOT NULL DEFAULT '',
    suburb     TEXT NOT NULL DEFAULT '',
    state      TEXT NOT NULL DEFAULT '',
    postcode   TEXT NOT NULL DEFAULT '',
    country    TEXT NOT NULL DEFAULT '',
    created_by TEXT NOT NULL REFERENCES accounts (id),
    created_at TEXT NOT NULL
);
CREATE INDEX persons_company ON persons (company);

ALTER TABLE fixtures ADD COLUMN record_schema TEXT NOT NULL DEFAULT '';

CREATE TABLE fixture_persons (
    fixture_id TEXT NOT NULL REFERENCES fixtures (id) ON DELETE CASCADE,
    person_id  TEXT NOT NULL REFERENCES persons (id),
    role       TEXT NOT NULL,
    PRIMARY KEY (fixture_id, person_id, role)
);
CREATE INDEX fixture_persons_person ON fixture_persons (person_id);
