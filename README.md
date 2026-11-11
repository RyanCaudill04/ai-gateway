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
2. Create your environment file and add API keys for the providers you want (empty = disabled):
   ```bash
   cp .env.example .env
   ```
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
5. Open <http://localhost:8080> (or the `HTTP_PORT` you set). Check the API with:
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

## Authentication
Every router endpoint except the health checks (`GET /api/health`, and the internal `/healthz`)
requires a signed-in caller. That includes paths that don't exist: an anonymous request gets `401`, not `404`.

Sign-in is handled by [Cloudflare Access](https://developers.cloudflare.com/cloudflare-one/policies/access/).
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
| `503` `auth_not_configured` | The settings above are missing or invalid; the reason is in `docker compose logs router` |

For local development without Cloudflare, set `AUTH_MODE=none`. That turns authentication off entirely,
so only use it on your own machine.

## Updating
Prebuilt images:
```bash
docker compose pull && docker compose up -d
```
From source: `git pull && docker compose up -d --build`.

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
- The database is **not** backed up before updates yet (waiting on `router backup`, issue #19), so
  back it up yourself (see Data) if a release changes the schema.
- **Security:** the updater mounts `/var/run/docker.sock`, which gives that container root-equivalent
  access to the host. Only enable it if you accept that.

## Data
The SQLite database lives in the `router-data` Docker volume (`/data/gateway.db` inside the router).

Back it up (stop the router first so the database and its `-wal` file are consistent):
```bash
docker compose stop router
```
```bash
docker compose cp router:/data ./backup
```
```bash
docker compose start router
```

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
# router on :8081
cd router && LISTEN_ADDR=:8081 DATABASE_PATH=./dev.db go run ./cmd/router
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
