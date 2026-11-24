-- Per-service API keys (#14). Only the SHA-256 of a key is stored.
CREATE TABLE api_keys (
    id          INTEGER PRIMARY KEY,
    name        TEXT NOT NULL UNIQUE,
    prefix      TEXT NOT NULL,          -- first chars, to recognise a key in lists
    key_hash    BLOB NOT NULL UNIQUE,   -- SHA-256 of the key; lookups are by hash
    daily_cap   INTEGER,                -- NULL = no cap (enforced by a later change)
    created_at  TEXT NOT NULL,
    revoked_at  TEXT                    -- NULL = active
);

CREATE TABLE api_key_usage (
    key_id   INTEGER NOT NULL REFERENCES api_keys(id) ON DELETE CASCADE,
    day      TEXT NOT NULL,             -- YYYY-MM-DD (UTC)
    requests INTEGER NOT NULL DEFAULT 0,
    PRIMARY KEY (key_id, day)
) WITHOUT ROWID;
