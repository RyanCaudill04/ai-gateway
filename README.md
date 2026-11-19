# AI Gateway

A self-hosted, OpenAI-compatible gateway that spreads requests across free-tier LLM providers
(Groq, Gemini, OpenRouter, Workers AI, …) with automatic fallback, falls back to
local models via Ollama, and includes a chat UI that shows the usage left for each provider and model.

> **Status:** early setup. The router serves a health check (`GET /api/health`) and, behind sign-in, `GET /api/me`. The UI is a
> mockup of the chat interface running on placeholder data: it doesn't send messages or load real usage yet.
> The roadmap is tracked in [GitHub issues](https://github.com/RyanCaudill04/ai-gateway/issues), grouped by [milestone](https://github.com/RyanCaudill04/ai-gateway/milestones).

## How it fits together

```
                ┌──────────────── docker compose ────────────────┐
browser ───┐    │                                                │
           ├──► │  proxy (Caddy) ──/v1/*, /api/*──► router (Go) ─┼──► providers
services ──┘    │        │                            │          │
                │        └──── everything else ──► ui │ (nginx)  │
                │                                     ▼          │
                │                            SQLite (volume)     │
                │                            ollama (optional) ◄─┤
                └────────────────────────────────────────────────┘
```

| Service | What it does |
|---------|--------------|
| `router` | Go. OpenAI-compatible API at `/v1`, UI endpoints at `/api`. The only service that touches the database or provider keys. |
| `ui` | React + Vite chat UI, served as static files. Talks only to the router. |
| `proxy` | Caddy. The single entry point; routes paths to `router` or `ui`. |
| `cloudflared` | *Optional* (`tunnel` profile). Publishes the gateway through a Cloudflare Tunnel. |
| `ollama` | *Optional* (`ollama` profile). Local models for the `private` route. |
| `updater` | *Optional* (`autoupdate` profile). Pulls and applies new images, rolling back on failure. |

## Install

### Requirements
- Docker with Compose v2 (`docker compose version`)
- Works on x86_64 and arm64 (e.g. Raspberry Pi 5)

### Steps
1. Clone the repo:
   ```bash
   git clone https://github.com/RyanCaudill04/ai-gateway.git && cd ai-gateway
   ```
2. Create your environment file and generate the master key that encrypts provider keys:
   ```bash
   cp .env.example .env
   openssl rand -base64 32
   ```
   Paste the output after `GATEWAY_SECRET_KEY=` in `.env`, and save a copy in a password manager.
   The router won't start without it (see [Provider keys](#provider-keys)). Provider API keys
   don't go in `.env`; you register them in step 5.
3. Create your provider config:
   ```bash
   cp config/providers.example.yaml config/providers.yaml
   ```
4. Start it, using one of:
   ```bash
   # Prebuilt images from GHCR (no build; fast on a Raspberry Pi)
   docker compose pull && docker compose up -d
   ```
   ```bash
   # Or build from source (development, or before any release is published)
   docker compose up -d --build
   ```
   Prebuilt images use the tag from `IMAGE_TAG` in `.env` (default `stable`, the latest release).
5. Register an API key for each provider you want to use (you're prompted for it; input is hidden):
   ```bash
   docker compose exec router /router providers set-key --test groq
   docker compose exec router /router providers list
   ```
   See [Provider keys](#provider-keys) for the other commands.
6. Create an API key for each service that will call `/v1/*` (printed once; store it in that service's config):
   ```bash
   docker compose exec router /router keys create mc-bot --daily-cap 500
   ```
   See [Service API keys](#service-api-keys).
7. Open <http://localhost:8080> (or the `HTTP_PORT` you set). Check the API with:
   ```bash
   curl http://localhost:8080/api/health
   ```
   It should return `{"status":"ok"}`.

### Optional add-ons
Local models with Ollama:
```bash
docker compose --profile ollama up -d
docker compose exec ollama ollama pull llama3.2:3b
```

Public access through a Cloudflare Tunnel (no ports opened on the host):
1. In the Cloudflare dashboard, go to **Zero Trust → Networks → Tunnels** and create a
   **Cloudflared** tunnel. Copy its token. You don't need to install a connector, because compose runs it.
2. On the tunnel's **Public Hostname** tab, add your hostname (e.g. `ai.example.com`) with service
   type `HTTP` and URL `proxy:80`. Don't add a hostname for any other service.
3. Put the token in `.env` as `CLOUDFLARE_TUNNEL_TOKEN`, then:
   ```bash
   docker compose --profile tunnel up -d
   ```
4. Check it from outside the network:
   ```bash
   curl https://ai.example.com/api/health
   ```

## Provider keys
Provider API keys (Groq, Gemini, OpenRouter, Workers AI, …) are registered in the running router and
stored **encrypted** (AES-256-GCM) in its SQLite database, never in `.env` or `providers.yaml`. A provider
is enabled once it has a key (local providers such as Ollama need none). Changes apply to the very
next request; no restart.

```bash
# Register or replace a key. Prompts with hidden input; --test first checks the key
# with one free call (listing the provider's models) and saves nothing if that fails.
docker compose exec router /router providers set-key --test gemini

# Or pipe it in (note -T), e.g. from a file you delete afterwards
docker compose exec -T router /router providers set-key openrouter < openrouter-key.txt

# Provider, enabled or not, key registered (last 4 characters only), last updated
docker compose exec router /router providers list

docker compose exec router /router providers remove-key groq
```
The provider name must be one in `config/providers.yaml`. Keys are never printed or logged; only their
last 4 characters are shown.

**The master key.** Stored keys are encrypted with `GATEWAY_SECRET_KEY` from `.env`, so a copied
database or backup is useless without it. Keep a copy in a password manager. If you lose it (or change
it), the stored keys can't be decrypted: the router still starts, `providers list` shows them as
`unreadable`, and you register each one again.

These commands talk to the running router over an admin API that listens only on `127.0.0.1:8081`
inside the router container (`ADMIN_ADDR`). It isn't published, Caddy never routes to it, and the
router refuses to start it on a non-loopback address. If the router isn't running, the commands say so.

## Usage poller

Providers only report rate-limit numbers when a request goes through, so they go stale while a provider
sits idle. A background poller inside the router refreshes them, spending as little quota as it can:

- **OpenRouter**: reads its free key endpoint (no quota used). Never sends it a completion.
- **Ollama / local**: a reachability check only, never a generation.
- **Groq and other providers that send rate-limit headers**: a one-word, one-token request, only for a
  model that is in a route or was used in the last 24 hours, and only if nothing refreshed it within the
  interval. It is skipped when the model is exhausted or nearly exhausted (5 requests or a tenth of the
  window left), or when every window's reset time has passed.
- **Gemini and Workers AI**: never probed. They send no rate-limit headers, so a probe would use quota and
  learn nothing.

`USAGE_POLL_INTERVAL` in `.env` sets the interval (default `1h`, minimum `1m`, `off` to disable). That is
at most 24 probes a day per probed model. The router logs the day's probe count after each round of
probes (`usage poller: probes today`), naming only provider and model.

## Service API keys
Every service that calls the OpenAI-compatible API (`/v1/*`) gets its own key, so its usage is
attributed to it, you can revoke it alone, and a daily cap stops one service from using up the
free-tier quota the others need. Like provider keys, changes apply to the very next request.

```bash
# Create a key. It's printed once, on stdout, and can't be shown again (only its hash is stored).
# --daily-cap is optional: the most requests per UTC day; leave it out for no cap.
docker compose exec router /router keys create mc-bot --daily-cap 500

# Name, prefix, cap, requests today, created, revoked. Never the key itself.
docker compose exec router /router keys list

# Stop a key working, from the next request. Its row and usage stay in the list.
docker compose exec router /router keys revoke mc-bot
```
- Services send it as `Authorization: Bearer <key>` (the OpenAI SDKs' `api_key`). See
  [docs/api.md](docs/api.md#authentication) for the `401` and `429` responses.
- Names are 1-32 lowercase letters, digits and `-`, starting with a letter. The name is part of the
  key (`sk-mc-bot-...`) and is what logs record as the service.
- **Rotating a key:** `keys revoke mc-bot`, then `keys create mc-bot`. Creating a name that already has
  an *active* key is refused, so a working key is never replaced by accident. Re-creating a revoked
  name gives a new key (the old one never works again) and keeps today's count, so rotating doesn't
  reset the cap. Revoking an already revoked key changes nothing and says so.
- Every request a key makes to `/v1/*` counts toward its cap, including `GET /v1/models`. Requests
  refused for being over the cap don't. The count resets at midnight UTC.
- Counts are written to the database in the background and survive restarts. If the router crashes
  (rather than being stopped), the last few milliseconds of counts can be lost, so a key may get that
  many extra requests that day.

## Request log
Every chat completion (`POST /v1/chat/completions`) adds one row to the `request_log` table, and
no response ever waits for it: rows go through a queue to a background writer that saves them in
batches (every 100 rows or every second).

- **Recorded:** time, service (the key's name), the model asked for, the provider and model that
  answered, how many fallbacks were needed (`0` = the first provider answered), prompt and
  completion token counts (empty when the provider doesn't report them), latency, HTTP status
  (`499` = the client left first) and the error category (`rate_limited`, `provider_error`, ...).
- **Never recorded:** prompts, completions, request or response bodies, or error messages (a
  provider's message can quote the prompt). Tests fail if a field that could hold text is added.
- **Not recorded:** `GET /v1/models`, and requests refused before reaching the chat handler (missing
  or revoked key, over the daily cap).
- If the disk can't keep up and the queue (4096 rows) fills, new rows are dropped, not queued, and the
  router logs a `request log: queue full, rows dropped` warning with the count (at most once a
  minute). A normal stop writes every queued row; a crash can lose about a second of rows.
- Rows are kept forever for now; there is no cleanup job yet.

Look at it from a backup (never open the live file with other tools while the router runs):
```bash
docker compose exec router /router backup /data/backups/look.db
docker compose cp router:/data/backups/look.db ./look.db
sqlite3 -header -column look.db "
  SELECT service, provider, COUNT(*) AS requests,
         SUM(status >= 400) AS errors, AVG(latency_ms) AS avg_ms,
         SUM(COALESCE(prompt_tokens,0) + COALESCE(completion_tokens,0)) AS tokens
  FROM request_log WHERE ts >= date('now', '-1 day') GROUP BY service, provider;"
```

## Authentication
Every router endpoint except the health checks (`GET /api/health`, and the internal `/healthz`)
requires an authenticated caller. That includes paths that don't exist: an anonymous request gets `401`,
not `404`. There are two kinds of caller:

- **Services, on `/v1/*`:** a [service API key](#service-api-keys), and nothing else. This is checked
  in every `AUTH_MODE` (including `none`) and doesn't depend on the Cloudflare Access settings below.
- **The browser UI, on `/api/*`:** a Cloudflare Access sign-in, as described below. A service key
  alone doesn't open `/api/*`.

Browser sign-in is handled by [Cloudflare Access](https://developers.cloudflare.com/cloudflare-one/policies/access/).
Access adds a signed token (`Cf-Access-Jwt-Assertion`) to each request it lets through, and the router
verifies it on every request: signature (against your team's public keys), issuer, audience, expiry,
and that the email is in `AUTH_ALLOWED_EMAILS`. A request that reaches the router without passing
through Cloudflare (for example on the LAN port) has no valid token and is refused.

1. In **Zero Trust → Access → Applications**, add a **Self-hosted** application for your hostname with a
   policy that allows only your email.
2. Copy the application's **AUD tag** and your team domain into `.env`:
   ```
   CF_ACCESS_TEAM_DOMAIN=myteam.cloudflareaccess.com
   CF_ACCESS_AUD=<aud tag>
   AUTH_ALLOWED_EMAILS=you@example.com
   ```
3. Restart the router: `docker compose up -d router`. Its log line `auth: Cloudflare Access` confirms the settings loaded.

| Response | Meaning |
|----------|---------|
| `401` | No token, or one that didn't verify |
| `403` | A genuine token for an email that isn't allowed |
| `503` `auth_not_configured` | The settings above are missing or invalid; the reason is in `docker compose logs router`. Only `/api/*` is locked; `/v1/*` keeps working with service keys |

For local development without Cloudflare, set `AUTH_MODE=none`. That turns sign-in off for `/api/*`
entirely, so only use it on your own machine. `/v1/*` still needs a service key.

If your Cloudflare Access application covers the whole hostname, Cloudflare itself stops services
before they reach the router. Either add an Access policy for the `/v1/*` path with the **Bypass**
action (the router's key check then protects it), or give each service an Access service token as a
second layer in front of its key.

## Updating
Prebuilt images:
```bash
docker compose pull && docker compose up -d
```
From source: `git pull && docker compose up -d --build`.

**Upgrading from a version that read provider keys from `.env`:** add `GATEWAY_SECRET_KEY` to `.env`
first (install step 2), or the new router won't start (the auto-updater will roll back). Then register
your keys with `providers set-key` and delete the old `*_API_KEY` lines from `.env`.

**Upgrading to the version with service API keys:** `/v1/*` now needs `Authorization: Bearer <key>`.
A Cloudflare Access sign-in no longer opens it, and neither does `AUTH_MODE=none`. Anything calling
`/v1/*` gets `401` until you create a key for it (`router keys create <name>`, see
[Service API keys](#service-api-keys)) and configure it there. `/api/*` and the health checks are unchanged.
The database migration (new tables `api_keys` and `api_key_usage`) runs on startup.

### Automatic updates (optional)
The `autoupdate` profile adds an `updater` service that checks GHCR every `UPDATE_INTERVAL` seconds
(default 300). When a new image is published it applies the `router` and `ui` updates together, waits up
to 2 minutes for the router to report healthy, and puts the previous images back if it doesn't. Each
attempt is logged (old digest -> new digest, result), and if `UPDATE_WEBHOOK_URL` is set (Discord-compatible)
it posts a one-line message on success and on rollback.

```bash
docker compose pull && docker compose --profile autoupdate up -d
docker compose logs -f updater
```
- It follows `IMAGE_TAG` (default `stable`, which only moves on release tags). Use `edge` to follow every push to `main`.
- **Pause:** `docker compose stop updater` (resume with `docker compose --profile autoupdate up -d`),
  or remove it with `docker compose rm -sf updater`. Do this before manual changes.
- The updater mounts the project directory at the same absolute path as on the host (`GATEWAY_DIR`,
  defaulting to the directory you ran compose from). Set `GATEWAY_DIR` in `.env` if you start compose from elsewhere.
- Only `router` and `ui` are updated; changes to `docker-compose.yml` itself still need a manual `git pull`.
- The database is **not** backed up automatically before updates, so run a backup yourself (see Data)
  if a release changes the schema.
- **Security:** the updater mounts `/var/run/docker.sock`, which gives that container root-equivalent
  access to the host. Only enable it if you accept that.

## Data
The SQLite database lives in the `router-data` Docker volume (`/data/gateway.db` inside the router).

### Backup
Back up while the router is running. `router backup` writes a consistent copy with SQLite's
`VACUUM INTO`, so nothing has to stop:
```bash
docker compose exec router /router backup /data/backups/gateway-$(date +%Y%m%d).db
```
The copy is written to `/data/backups/` inside the volume. Copy it to the host with:
```bash
docker compose cp router:/data/backups ./backups
```
A backup never overwrites an existing file, so give each one a new name (the date works for daily backups).

### Restore
Restoring replaces the live database, so stop the stack first. Put the backup you want back in the
volume (skip the first command if it's already in `/data/backups`), then swap the database file:
```bash
docker compose stop
```
```bash
docker compose cp ./backups/gateway-20261113.db router:/data/backups/
```
```bash
docker run --rm -v ai-gateway_router-data:/data alpine sh -c 'cat /data/backups/gateway-20261113.db > /data/gateway.db && rm -f /data/gateway.db-wal /data/gateway.db-shm'
```
```bash
docker compose start
```
The `cat >` keeps the existing file's owner and permissions, so the router can still write to it. The
`rm` clears any leftover `-wal`/`-shm` files, which would otherwise be applied on top of the restored
database. Check the volume name with `docker volume ls` if your project directory isn't `ai-gateway`.

The provider keys in a backup can only be read with the same `GATEWAY_SECRET_KEY` that was in `.env`
when they were registered. Restored with a different one, they show as `unreadable` in
`providers list` and must be registered again.

Run only **one** router at a time against a database (don't use `--scale router=N`), and keep the
volume on local disk, not a network share. SQLite's file locking isn't reliable on network filesystems.

## Releasing
CI publishes multi-arch (amd64 + arm64) images to GHCR as `ghcr.io/ryancaudill04/ai-gateway-router` and `-ui`.

| Trigger | Image tags |
|---------|-----------|
| push to `main` | `edge`, `sha-<short>` |
| tag `vX.Y.Z` | `X.Y.Z`, `X.Y`, `stable` |

To release: make sure `main` is green, then
```bash
git tag v0.1.0 && git push origin v0.1.0
```
Installs following `stable` (including the auto-updater) pick it up on their next pull.

GHCR packages start **private**. After the first publish, the owner must open each package
(`ai-gateway-router`, `ai-gateway-ui`) on GitHub, go to Package settings, and set visibility to **Public**
once, so other installs can pull without logging in.

## Development
Run the pieces outside Docker for faster iteration:
```bash
# router on :8081 (its admin API moves to :8082 so the two don't collide). Use the same
# GATEWAY_SECRET_KEY every time, or keys registered in dev.db become unreadable.
export GATEWAY_SECRET_KEY=<output of openssl rand -base64 32>
cd router && LISTEN_ADDR=:8081 ADMIN_ADDR=127.0.0.1:8082 DATABASE_PATH=./dev.db \
  PROVIDERS_CONFIG=../config/providers.yaml go run ./cmd/router
```
```bash
# register a key with the dev router, and create a service key to call /v1/* with
cd router && ADMIN_ADDR=127.0.0.1:8082 go run ./cmd/router providers set-key groq
ADMIN_ADDR=127.0.0.1:8082 go run ./cmd/router keys create dev
```
```bash
# UI on :5173, proxies /v1 and /api to the router on :8081
cd ui && npm install && npm run dev
```

## Repo layout
```
router/    Go service (Dockerfile, cmd/router)
ui/        React + Vite app (Dockerfile, nginx.conf)
proxy/     Caddyfile
updater/   update.sh (optional auto-updater)
config/    providers.example.yaml (copy to providers.yaml)
```
