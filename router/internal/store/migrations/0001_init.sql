-- Baseline. schema_migrations itself is created by the migrator.
-- Feature tables arrive with the issue that needs them (see docs/schema.md):
--   0002 api_keys (#14), 0003 request_log (#15), 0004 rate_limits (#16),
--   0005 provider_keys (#40).
CREATE TABLE meta (
    key   TEXT PRIMARY KEY,
    value TEXT NOT NULL
) WITHOUT ROWID;

INSERT INTO meta (key, value) VALUES ('created_at', strftime('%Y-%m-%dT%H:%M:%SZ','now'));
