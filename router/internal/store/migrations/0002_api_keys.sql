-- Per-service API keys and daily request caps (#14).
--
-- The plain key is never stored: only its SHA-256 (key_hash), which is what a
-- presented key is looked up by, and a short display prefix so a person can
-- tell keys apart in `router keys list`. name is the service's name; it is
-- what the request log records (#15) and what `keys revoke` takes.
--
-- A revoked key keeps its row (revoked_at set) so its usage history stays.
-- Creating a key with a revoked key's name reuses that row with the new hash,
-- so the service keeps one row and its daily count carries over.
CREATE TABLE api_keys (
    id          INTEGER PRIMARY KEY,
    name        TEXT NOT NULL UNIQUE,
    prefix      TEXT NOT NULL,
    key_hash    BLOB NOT NULL UNIQUE,
    daily_cap   INTEGER CHECK (daily_cap IS NULL OR daily_cap > 0), -- NULL = no cap
    created_at  TEXT NOT NULL,
    revoked_at  TEXT                                                -- NULL = active
);

-- Requests admitted per key per UTC day. A new day is a new row, so "resets
-- daily" needs no background job.
CREATE TABLE api_key_usage (
    key_id   INTEGER NOT NULL REFERENCES api_keys(id) ON DELETE CASCADE,
    day      TEXT NOT NULL,             -- YYYY-MM-DD (UTC)
    requests INTEGER NOT NULL DEFAULT 0,
    PRIMARY KEY (key_id, day)
) WITHOUT ROWID;
