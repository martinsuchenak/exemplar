-- A fixture imported already approved keeps the source instance's reviewer
-- here. reviewed_by references a local account, which the source's reviewer
-- is not, so it stays empty for imported reviews.
ALTER TABLE fixtures ADD COLUMN imported_reviewer TEXT NOT NULL DEFAULT '';
