# AI Gateway

A self-hosted, OpenAI-compatible gateway that spreads requests across free-tier LLM providers
(Groq, Gemini, Cerebras, Mistral, OpenRouter, Workers AI, …) with automatic fallback, falls back to
local models via Ollama, and includes a chat UI that shows the usage left for each provider and model.

> **Status:** early setup. Docker setup is in place; the router is a stub that only serves a health check.
> See [plan.md](plan.md) for the design.

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
4. Start it:
   ```bash
   docker compose up -d --build
   ```
5. Open <http://localhost:8080> (or the `HTTP_PORT` you set).

### Optional add-ons
Local models with Ollama:
```bash
docker compose --profile ollama up -d
docker compose exec ollama ollama pull llama3.2:3b
```

Public access through a Cloudflare Tunnel: create a tunnel in the Cloudflare dashboard, point its
public hostname at `http://proxy:80`, put the token in `CLOUDFLARE_TUNNEL_TOKEN`, then:
```bash
docker compose --profile tunnel up -d
```

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
config/    providers.example.yaml (copy to providers.yaml)
plan.md    design notes and roadmap
```
