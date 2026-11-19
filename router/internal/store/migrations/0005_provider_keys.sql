-- Upstream provider API keys (#40), encrypted with AES-256-GCM under the
-- master key GATEWAY_SECRET_KEY. The plain key is never stored. The provider
-- name is part of the GCM additional authenticated data, so a ciphertext
-- copied into another provider's row fails to decrypt. last4 is the only part
-- of a key that is ever shown (`router providers list`).
CREATE TABLE provider_keys (
    provider   TEXT PRIMARY KEY,
    ciphertext BLOB NOT NULL,
    nonce      BLOB NOT NULL,
    last4      TEXT NOT NULL,
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL
) WITHOUT ROWID;
