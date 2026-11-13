# Database schema plan

SQLite, owned by the router. Migrations are numbered files in
`router/internal/store/migrations/`, applied at startup and tracked in
`schema_migrations`. **Each table is created by the issue that needs it**, so
#6 only ships the baseline.

Conventions
- Timestamps: UTC ISO-8601 text (`2026-11-11T12:00:00Z`). Sorts correctly and is readable in `sqlite3`.
- Primary keys: `INTEGER PRIMARY KEY` unless a natural key exists.
- Never store prompt or completion text (#15). Never store a plain API key (#14, #40).
- All writes go through the single writer pool; the UI reads via `/api`.

| Migration | Issue | Table | Purpose |
|---|---|---|---|
| 0001 | #6 | `schema_migrations`, `meta` | Migration tracking; key/value for install info |
| 0002 | #14 | `api_keys`, `api_key_usage` | Per-service keys and daily caps |
| 0003 | #15 | `request_log` | One row per request, no content |
| 0004 | #16 | `rate_limits` | Latest known limit windows per provider/model |
| 0005 | #40 | `provider_keys` | Encrypted upstream provider keys |

## 0002: api_keys, api_key_usage (#14)
```sql
CREATE TABLE api_keys (
    id          INTEGER PRIMARY KEY,
    name        TEXT NOT NULL UNIQUE,
    prefix      TEXT NOT NULL,          -- first chars, to recognise a key in lists
    key_hash    BLOB NOT NULL UNIQUE,   -- SHA-256 of the key; lookups are by hash
    daily_cap   INTEGER,                -- NULL = no cap
    created_at  TEXT NOT NULL,
    revoked_at  TEXT                    -- NULL = active
);
CREATE TABLE api_key_usage (
    key_id   INTEGER NOT NULL REFERENCES api_keys(id) ON DELETE CASCADE,
    day      TEXT NOT NULL,             -- YYYY-MM-DD (UTC)
    requests INTEGER NOT NULL DEFAULT 0,
    PRIMARY KEY (key_id, day)
) WITHOUT ROWID;
```
Cap check is one upsert-and-read on the writer. A new day is a new row, so "resets daily" needs no job.

## 0003: request_log (#15)
```sql
CREATE TABLE request_log (
    id                INTEGER PRIMARY KEY,
    ts                TEXT NOT NULL,
    service           TEXT NOT NULL,    -- key name (copied, so it survives key deletion)
    requested_model   TEXT NOT NULL,
    provider          TEXT,             -- NULL if no upstream was reached
    model             TEXT,
    fallback_attempts INTEGER NOT NULL DEFAULT 0,
    prompt_tokens     INTEGER,
    completion_tokens INTEGER,
    latency_ms        INTEGER NOT NULL,
    status            INTEGER NOT NULL,
    error_type        TEXT
);
CREATE INDEX request_log_ts ON request_log (ts);
CREATE INDEX request_log_service_ts ON request_log (service, ts);
```
Written in batches by one goroutine. `service` is text rather than a foreign key on purpose: revoking or deleting a key must not rewrite history.

## 0004: rate_limits (#16)
```sql
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
```
Only the latest value per window is kept (upsert); history lives in `request_log`. The router keeps an in-memory copy for routing decisions.

## 0005: provider_keys (#40)
```sql
CREATE TABLE provider_keys (
    provider   TEXT PRIMARY KEY,
    ciphertext BLOB NOT NULL,           -- AES-256-GCM
    nonce      BLOB NOT NULL,           -- fresh random per encryption
    last4      TEXT NOT NULL,
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL
) WITHOUT ROWID;
```
Provider name is the GCM additional authenticated data, so a row can't be moved to another provider.

## Later (not in Phase 3)
Chat history for the UI (Phase 4) gets its own tables; `private` chats stay in this database only.
