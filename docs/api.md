# API reference

Endpoints the router serves to clients. Everything except the health checks
requires an authenticated caller. Add new endpoints as a `##` section below.

## GET /v1/models

Lists the models a client can put in the `model` field. The shape follows
OpenAI's list-models response, so the official SDKs' `models.list()` works,
with extra fields for the UI.

- **Virtual models** (`virtual: true`) are the routes in `config/providers.yaml`
  (`fast`, `long-context`, `private`, ...). Listed first, sorted by id.
- **Real models** have ids like `groq/llama-3.3-70b-versatile`. Only models of
  enabled providers (a registered key, or `local: true`) are listed, sorted by id.
- `context_length` is the largest prompt, in tokens, the model accepts. For a
  route it is the largest among its enabled entries, since the router skips an
  entry the prompt does not fit in.
- `provider` is the provider name for a real model, and empty for a route.
- `local` is true when nothing leaves this machine. For a route, only if every
  enabled entry is local.
- A route with no enabled entries is left out. The list is recomputed on every
  request, so registering a key shows up without a restart.
- `created` is always `0` (there is no creation time to report).

```json
{
  "object": "list",
  "data": [
    {
      "id": "fast",
      "object": "model",
      "created": 0,
      "owned_by": "gateway",
      "context_length": 1048576,
      "provider": "",
      "local": false,
      "virtual": true
    },
    {
      "id": "ollama/llama3.2:3b",
      "object": "model",
      "created": 0,
      "owned_by": "ollama",
      "context_length": 131072,
      "provider": "ollama",
      "local": true,
      "virtual": false
    }
  ]
}
```
