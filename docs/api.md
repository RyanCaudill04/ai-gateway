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

## POST /v1/chat/completions

OpenAI's chat completions endpoint, streaming and not. Point an OpenAI SDK at
the gateway (`base_url` = `https://<gateway>/v1`) and use it as usual.

### Request

The body is OpenAI's chat completions request. The router reads only `model`
and `stream`; everything else (messages, tools, images, provider-specific
extras) is forwarded unchanged, and the provider validates it.

- `model` is a virtual model (`fast`, `long-context`, `private`, ...) or a
  pinned `provider/model` ID from `GET /v1/models`, such as
  `groq/llama-3.3-70b-versatile`. Model IDs may contain slashes themselves:
  `groq/openai/gpt-oss-20b` is provider `groq`, model `openai/gpt-oss-20b`.
- A virtual model uses its first entry whose provider is enabled. (Falling
  back to the next entry when one fails comes with #11.) A pinned model only
  ever uses that model.
- The body may be at most 20 MiB (room for base64 images). Larger bodies get
  `413` `request_too_large`.

| Problem | Status | `type` / `code` |
|---------|--------|-----------------|
| Body is not a JSON object, `model` missing or not a string, `stream` not a boolean | 400 | `invalid_request` / `invalid_request` (`param` names the field) |
| Body over 20 MiB | 413 | `invalid_request` / `request_too_large` |
| `model` is neither a route nor a known `provider/model` | 404 | `invalid_request` / `model_not_found` (`param: "model"`), as OpenAI answers |
| Pinned model whose provider has no key registered | 503 | `provider_error` / `provider_not_enabled` |
| Virtual model none of whose providers is enabled | 503 | `provider_error` / `no_enabled_provider` |

The last two are 503, not 4xx: the request is fine and will work once a key
is registered.

### Response

Without `stream`, the provider's JSON body and status are returned as they
are.

With `"stream": true` the response is `text/event-stream`. Each chunk is sent
as `data: <chat.completion.chunk JSON>` and a blank line the moment the
provider produces it, and the stream ends with `data: [DONE]`.

Token usage: if the request sets `stream_options` itself, it is passed to the
provider and the usage chunk (empty `choices`, a `usage` object) reaches the
client as usual. If not, the router asks for usage anyway (for the request
log) and drops that chunk, so the client sees what it would from OpenAI.

### Which provider answered

Every response, success or error, streaming or not, carries:

| Header | Example | Meaning |
|--------|---------|---------|
| `x-router-provider` | `groq` | The provider that answered, or that was tried and failed |
| `x-router-model` | `llama-3.3-70b-versatile` | That provider's own model ID (the same as `model` in an error body) |

Together they give the pinned ID, `groq/llama-3.3-70b-versatile`. They are
absent only when the request never got as far as choosing a model (bad body,
unknown model, a route with no enabled provider).

### Errors before and during a stream

The router reads the provider's first event before it sends anything. So if
the provider fails before producing any output (an error status, or an error
as its first event), the client gets a normal error response with the right
status (429, 502, ...) in the format below, even for a streaming request.

Once the first chunk has been sent, the status (200) can't change. A later
failure arrives as one last event in the error format (see
[Errors after a stream has started](#errors-after-a-stream-has-started)), and
the stream ends **without** `data: [DONE]`: `[DONE]` means the answer is
complete. The OpenAI SDKs raise an `APIError` on that event.

If the client disconnects, the router stops the provider request at once.

## Errors

Every error the router returns has the same JSON body, in OpenAI's shape, so
the official SDKs parse it (`error.message`, `error.type`, `error.code` and
`error.param` are where they expect them):

```json
{
  "error": {
    "message": "groq/llama-3.3-70b-versatile hit its daily request limit. It resets at 2026-11-14T00:00:00Z.",
    "type": "rate_limited",
    "code": "rate_limit_exceeded",
    "provider": "groq",
    "model": "llama-3.3-70b-versatile",
    "limit_type": "requests",
    "window": "day",
    "resets_at": "2026-11-14T00:00:00Z"
  }
}
```

`message`, `type` and `code` are always present. The rest are left out when
they do not apply:

| Field | Meaning |
|-------|---------|
| `param` | The request field at fault (`invalid_request` only) |
| `provider`, `model` | Which provider and model failed (not the service-key cap) |
| `limit_type` | `requests` or `tokens` |
| `window` | `minute` or `day` |
| `resets_at` | When the limit clears, UTC RFC 3339. Absent when unknown (e.g. Gemini) |
| `attempts` | Only for `all_providers_exhausted`, see below |

Messages are safe to show to a person: they never contain keys, request
bodies or headers.

### Types and status codes

| `type` | HTTP status | When |
|--------|-------------|------|
| `rate_limited` | 429 | A provider's limit, or a service key's daily cap. `Retry-After` is set when `resets_at` is known |
| `context_length_exceeded` | 400 | The prompt is too long for the model. Retrying the same request will not help |
| `provider_error` | 502 | The provider failed (5xx, timeout, network error, or the router's provider key was rejected). 503 when the router itself can't serve the request (`code: auth_not_configured`, `provider_not_enabled`, `no_enabled_provider`) |
| `all_providers_exhausted` | 429 or 503 | Every provider a virtual model tried failed. 429 (with `Retry-After` when a reset is known) if every attempt was a rate limit, otherwise 503 |
| `invalid_request` | 400, 404, 405, 413 | The request is wrong: bad body, unknown endpoint (`code: not_found`), unknown model (`code: model_not_found`), wrong method (`code: method_not_allowed`, with an `Allow` header), body too large (`code: request_too_large`) |
| `unauthorized` | 401 or 403 | 401: no valid sign-in. 403 (`code: forbidden`, or `cross_origin_forbidden` for a cross-site browser request): signed in but not allowed |

The OpenAI SDKs retry 408, 409, 429 and 5xx on their own, and honour
`Retry-After` when it is 60 seconds or less. The 4xx codes above (400, 401,
403, 404) are never retried, which is right for them: sending the same request
again cannot succeed.

Examples of the other types:

```json
{"error": {"message": "Sign-in required.", "type": "unauthorized", "code": "unauthorized"}}
```

```json
{
  "error": {
    "message": "This API key has reached its daily request limit. It resets at 2026-11-14T00:00:00Z.",
    "type": "rate_limited",
    "code": "rate_limit_exceeded",
    "limit_type": "requests",
    "window": "day",
    "resets_at": "2026-11-14T00:00:00Z"
  }
}
```

```json
{
  "error": {
    "message": "Please reduce the length of the messages.",
    "type": "context_length_exceeded",
    "code": "context_length_exceeded",
    "provider": "groq",
    "model": "llama-3.3-70b-versatile"
  }
}
```

```json
{
  "error": {
    "message": "HTTP 503",
    "type": "provider_error",
    "code": "provider_error",
    "provider": "gemini",
    "model": "gemini-2.5-flash"
  }
}
```

### Pinned models

A pinned model (`provider/model`) never falls back, so the provider's failure
is the answer: a 429 carries that provider's `limit_type`, `window` and
`resets_at`, and the other types carry its `provider` and `model`.

### `all_providers_exhausted`

A virtual model (`fast`, `long-context`, ...) tries its entries in order. If
all fail, `attempts` lists each one in the order tried, with why it failed.
`type` uses the same words as an error's type, and the limit fields appear
when the attempt was a rate limit and they are known. The top-level
`resets_at` is the earliest reset among the attempts, the first moment a retry
could work.

```json
{
  "error": {
    "message": "All 2 providers tried for this request failed or are out of quota. See attempts for each reason.",
    "type": "all_providers_exhausted",
    "code": "all_providers_exhausted",
    "resets_at": "2026-11-13T12:01:00Z",
    "attempts": [
      {
        "provider": "groq",
        "model": "llama-3.3-70b-versatile",
        "type": "rate_limited",
        "message": "Rate limit reached",
        "limit_type": "requests",
        "window": "minute",
        "resets_at": "2026-11-13T12:01:00Z"
      },
      {
        "provider": "gemini",
        "model": "gemini-2.5-flash",
        "type": "provider_error",
        "message": "HTTP 503"
      }
    ]
  }
}
```

### Errors after a stream has started

Once a streamed response has begun, the status line (200) and any tokens are
already sent, so the error cannot change the status. It arrives as one last
server-sent event carrying the same object, then the stream ends:

```
data: {"error":{"message":"groq hit its per-minute token limit.","type":"rate_limited","code":"rate_limit_exceeded","provider":"groq","limit_type":"tokens","window":"minute"}}

```

A client should check each `data:` payload for a top-level `error` key.
