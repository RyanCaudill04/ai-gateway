-- One row per chat request (#15), written in batches by the request log.
--
-- Metadata only. There is deliberately no column for prompt or completion
-- text, and error_type holds the router's error category ("rate_limited"),
-- never a message: provider error messages can quote the prompt.
CREATE TABLE request_log (
    id                INTEGER PRIMARY KEY,
    ts                TEXT NOT NULL,    -- when the request arrived (UTC, whole seconds)
    service           TEXT NOT NULL,    -- key name (copied, so it survives key deletion)
    requested_model   TEXT NOT NULL,
    provider          TEXT,             -- NULL if no upstream was reached
    model             TEXT,
    fallback_attempts INTEGER NOT NULL DEFAULT 0, -- providers tried after the first; 0 = none
    prompt_tokens     INTEGER,          -- NULL = the provider didn't report counts
    completion_tokens INTEGER,
    latency_ms        INTEGER NOT NULL,
    status            INTEGER NOT NULL, -- HTTP status sent; 499 = client left first
    error_type        TEXT              -- NULL on success
);
CREATE INDEX request_log_ts ON request_log (ts);
CREATE INDEX request_log_service_ts ON request_log (service, ts);
