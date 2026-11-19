-- Latest known rate-limit windows per provider/model (#16). Only the newest
-- value per window is kept (upsert); the router holds a copy in memory for
-- routing decisions and loads this table back at startup.
CREATE TABLE rate_limits (
    provider   TEXT NOT NULL,
    model      TEXT NOT NULL,
    kind       TEXT NOT NULL CHECK (kind IN ('requests','tokens')),
    window     TEXT NOT NULL CHECK (window IN ('minute','day')),
    remaining  INTEGER,
    "limit"    INTEGER,
    resets_at  TEXT,
    checked_at TEXT NOT NULL,
    PRIMARY KEY (provider, model, kind, window)
) WITHOUT ROWID;
