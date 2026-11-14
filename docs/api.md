# API reference

Endpoints the router serves to clients. Everything except the health checks
requires an authenticated caller (see [Authentication](#authentication)). Add
new endpoints as a `##` section below.

## Authentication

| Paths | Who calls them | Credential |
|-------|----------------|------------|
| `/v1/*` | Services (the OpenAI-compatible API) | A service API key: `Authorization: Bearer <key>` |
| `/api/*` | The browser UI | Cloudflare Access sign-in (`Cf-Access-Jwt-Assertion`), plus a same-origin check for state-changing requests |
| `GET /api/health`, `GET /healthz` | Anyone | None |

### Service API keys (`/v1/*`)

Each calling service has its own key, created with `router keys create <name>`
(see the README). Send it on every request:

```
Authorization: Bearer sk-mc-bot-...
```

With the OpenAI SDKs, pass it as the API key and point `base_url` at the
gateway, e.g. `OpenAI(base_url="https://ai.example.com/v1", api_key="sk-mc-bot-...")`.

- The key is the only thing `/v1/*` checks. It is required in every
  `AUTH_MODE`, including `none`, and it doesn't depend on the Cloudflare Access
  settings. A Cloudflare Access sign-in alone does not open `/v1/*`, and a key
  alone does not open `/api/*`.
- Unknown paths under `/v1/` also need a key (without one they are `401`, not
  `404`).
- A key that is missing, malformed, unknown or revoked gets the same `401`;
  the response doesn't say which. A revoked key stops working on the very next
  request.

```
HTTP/1.1 401 Unauthorized
WWW-Authenticate: Bearer realm="ai-gateway"

{"error": {"message": "A valid API key is required: send it as \"Authorization: Bearer <key>\".", "type": "unauthorized", "code": "unauthorized"}}
```

### Daily cap

A key may have a daily request cap. Every request that a valid key makes to
`/v1/*` counts, whatever the endpoint (so `GET /v1/models` counts as much as a
chat completion), with one exception: `GET /v1/usage` is not counted and is
answered even when the key is over its cap (see [Usage](#get-v1usage)).
Requests refused for being over the cap don't count. The day
is the UTC day: the count goes back to 0 at midnight UTC. Over the cap:

```
HTTP/1.1 429 Too Many Requests
Retry-After: 30600

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

There is no `provider` or `model`: the limit belongs to the caller. The
OpenAI SDKs only honour `Retry-After` up to 60 seconds, so they will retry a
few times and then give up; check for `type: rate_limited` with no `provider`
to tell the cap apart from a provider limit.

## GET /v1/models

Lists the models a client can put in the `model` field. The shape follows
OpenAI's list-models response, so the official SDKs' `models.list()` works,
with extra fields for the UI.

- **Virtual models** (`virtual: true`) are the routes in `config/providers.yaml`
  (`fast`, `long-context`, `private`, ...). Listed first, sorted by id.
- **Real models** have ids like `groq/openai/gpt-oss-120b`. Only models of
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

## GET /v1/usage

How much of each provider's free tier is left, for the UI's usage bars and
model picker. A read-only view of what the router already knows: the
rate-limit windows it recorded from provider responses and from the usage
poller, the poller's health check of local providers, and the calling key's
own cap. It sends nothing to any provider, so calling it costs no quota, and
the answer carries `Cache-Control: no-store`. Only `GET` is allowed.

**It does not count toward the calling key's daily cap**, and a key that is
over its cap can still call it. The UI polls this endpoint (after every reply
and when the picker opens), and counting those polls would let a dashboard
left open use up the quota it is displaying; an over-cap key is exactly the
one that needs to see why it is refused. This is the only endpoint with that
exemption, it needs a valid key like every other, and `HEAD` or any other
path still counts.

All times are UTC RFC 3339 with whole seconds (like `resets_at` in errors).
Every field is always present; where a value does not apply it is `null`
(or `[]` for a list) rather than missing. Exceptions are marked below.

```json
{
  "object": "usage",
  "now": "2026-11-14T12:00:00Z",
  "key": {
    "name": "chat-ui",
    "daily_cap": 1000,
    "used_today": 37,
    "remaining": 963,
    "resets_at": "2026-11-15T00:00:00Z"
  },
  "routes": [
    {
      "id": "fast",
      "status": "available",
      "next": { "id": "groq/openai/gpt-oss-20b", "provider": "groq", "model": "openai/gpt-oss-20b" },
      "resets_at": null
    },
    {
      "id": "long-context",
      "status": "exhausted",
      "next": null,
      "resets_at": "2026-11-14T12:05:00Z"
    },
    {
      "id": "private",
      "status": "available",
      "next": { "id": "ollama/llama3.2:3b", "provider": "ollama", "model": "llama3.2:3b" },
      "resets_at": null
    }
  ],
  "providers": [
    {
      "provider": "gemini",
      "local": false,
      "enabled": true,
      "status": "unknown",
      "models": [
        {
          "id": "gemini/3.5-flash-lite",
          "model": "3.5-flash-lite",
          "context_length": 1048576,
          "status": "unknown",
          "exhausted": false,
          "resets_at": null,
          "observed_at": null,
          "windows": []
        }
      ]
    },
    {
      "provider": "groq",
      "local": false,
      "enabled": true,
      "status": "known",
      "models": [
        {
          "id": "groq/openai/gpt-oss-20b",
          "model": "openai/gpt-oss-20b",
          "context_length": 131072,
          "status": "known",
          "exhausted": false,
          "resets_at": null,
          "observed_at": "2026-11-14T11:58:12Z",
          "windows": [
            { "kind": "requests", "window": "minute", "remaining": 21, "limit": 30,
              "resets_at": "2026-11-14T12:00:42Z", "checked_at": "2026-11-14T11:58:12Z",
              "exhausted": false, "reset": false },
            { "kind": "requests", "window": "day", "remaining": 580, "limit": 1000,
              "resets_at": "2026-11-15T00:00:00Z", "checked_at": "2026-11-14T11:58:12Z",
              "exhausted": false, "reset": false },
            { "kind": "tokens", "window": "minute", "remaining": 6000, "limit": 6000,
              "resets_at": null, "checked_at": "2026-11-14T11:30:00Z",
              "exhausted": false, "reset": true },
            { "kind": "tokens", "window": "day", "remaining": 130000, "limit": 200000,
              "resets_at": "2026-11-15T00:00:00Z", "checked_at": "2026-11-14T11:58:12Z",
              "exhausted": false, "reset": false }
          ]
        },
        {
          "id": "groq/openai/gpt-oss-120b",
          "model": "openai/gpt-oss-120b",
          "context_length": 131072,
          "status": "known",
          "exhausted": true,
          "limit_type": "requests",
          "window": "day",
          "resets_at": "2026-11-15T00:00:00Z",
          "observed_at": "2026-11-14T11:59:30Z",
          "windows": [
            { "kind": "requests", "window": "day", "remaining": 0, "limit": 1000,
              "resets_at": "2026-11-15T00:00:00Z", "checked_at": "2026-11-14T11:59:30Z",
              "exhausted": true, "reset": false }
          ]
        }
      ]
    },
    {
      "provider": "ollama",
      "local": true,
      "enabled": true,
      "status": "unlimited",
      "health": { "online": true, "checked_at": "2026-11-14T11:50:00Z" },
      "models": [
        {
          "id": "ollama/llama3.2:3b",
          "model": "llama3.2:3b",
          "context_length": 131072,
          "status": "unlimited",
          "exhausted": false,
          "resets_at": null,
          "observed_at": null,
          "windows": []
        }
      ]
    },
    {
      "provider": "openrouter",
      "local": false,
      "enabled": false,
      "status": "disabled",
      "models": [
        {
          "id": "openrouter/gemma-4-26b:free",
          "model": "gemma-4-26b:free",
          "context_length": 262144,
          "status": "disabled",
          "exhausted": false,
          "resets_at": null,
          "observed_at": null,
          "windows": []
        }
      ]
    }
  ]
}
```

The example is trimmed (a real answer lists every configured model).

### Top level

| Field | Meaning |
|-------|---------|
| `object` | Always `"usage"`. |
| `now` | The router's clock when it answered. Compute "resets in 42s" and "updated 3 min ago" against this, not the browser's clock, so the two can't disagree. |
| `key` | The calling key (below), or `null` if the key store has no such key. |
| `routes` | One entry per virtual model, sorted by `id`. |
| `providers` | One entry per configured provider, sorted by name. |

The numbers are the router's *global* view: remaining usage is shared by every
service that uses the providers. `key` is the caller's own daily cap, separate
from them.

### `key`

| Field | Meaning |
|-------|---------|
| `name` | The key's name (the calling service). |
| `daily_cap` | Requests per UTC day, or `null` for no cap. |
| `used_today` | Requests counted today. Reads of this endpoint are not counted. |
| `remaining` | `daily_cap - used_today`, never below 0; `null` for no cap. |
| `resets_at` | Next midnight UTC, when `used_today` goes back to 0. |

### `routes[]`

The "Auto" entries of the picker. Every route is listed, including ones
`/v1/models` leaves out because no provider is enabled.

| Field | Meaning |
|-------|---------|
| `id` | The route name: the `model` to send (`fast`, `long-context`, `private`, ...). |
| `status` | `available`: a request sent now would try `next` first. `exhausted`: every enabled entry is known to be out of quota. `unavailable`: no entry has an enabled provider (no key registered). |
| `next` | The entry Auto would use now, or `null` unless `status` is `available`. `id` is the `provider/model` id; `provider` and `model` are its parts. Look it up in `providers` (same `id`) for the bars to show under an Auto model. |
| `resets_at` | Only when `exhausted`: when the soonest entry is usable again (the earliest reset among the exhausted entries). `null` otherwise. |

`next` is computed with the same two checks the chat handler makes before it
tries an entry (provider enabled, model not known to be out of quota), so it is
what a request sent right now would call first. It is a prediction: the call
can still fail, and a prompt too long for that entry is only found out by
asking. Providers that report nothing are never skipped here, only after a
real 429.

### `providers[]` and `models[]`

Every provider in `config/providers.yaml` is listed, with its models in the
config's order.

**Disabled providers are listed**, with `enabled: false` and `status:
"disabled"`, where `/v1/models` leaves them out. `/v1/models` says what a
client may send, so it hides what would fail. This endpoint says what state
things are in: the picker shows unavailable providers greyed out, and a
missing provider could not tell "no key registered yet" from "misspelled in the
config". A disabled model has no windows (anything recorded before the key was
removed is not reported), and `exhausted` is `false`.

| Field | Meaning |
|-------|---------|
| `provider` | Provider name. |
| `local` | `true` if requests never leave this machine (Ollama). |
| `enabled` | `true` if the provider can be used now: local, or a key is registered. |
| `status` | `disabled` (not enabled), `unlimited` (local), `known` (at least one of its models has reported windows), `unknown` (none has). |
| `health` | Local providers only (the field is absent otherwise). See below. |
| `models` | The provider's models. |

`health` has `online` (`true`/`false`) and `checked_at`, from the usage
poller's health check. Before the first check has run (the poller waits one
interval-tick after the router starts, and is off with
`USAGE_POLL_INTERVAL=off`) both are `null`: show "checking", not "offline".

| Model field | Meaning |
|-------------|---------|
| `id` | `provider/model`, the same id as `/v1/models`. |
| `model` | The provider's own id for the model. |
| `context_length` | Largest prompt in tokens. |
| `status` | `known`: the provider has reported at least one window. `unknown`: it has reported none, either because it never does (Gemini, Workers AI) or because the model has not been used or probed yet: show "Usage remaining unknown" and no bars. `unlimited`: local, no quota: show "Unlimited (local)" and the provider's `health`. `disabled`: see above. |
| `exhausted` | `true` if the router knows the model is out of quota right now and skips it. This is what the picker greys out. |
| `limit_type`, `window` | Only when `exhausted` (absent otherwise): which limit is used up, as `requests`/`tokens` and `minute`/`day`. If several are, the one that lasts longest. |
| `resets_at` | When an exhausted model is usable again; `null` if not exhausted. |
| `observed_at` | The last time the router saw a response from this model, with or without rate-limit headers; `null` if never. |
| `windows` | The reported limit windows, `[]` when `status` is not `known`. Ordered requests/minute, requests/day, tokens/minute, tokens/day (only those the provider reported). |

A model can be `exhausted` without being `known`: after a 429 the router backs
off for the time the provider asked (one minute if it said nothing), and that
shows up as a requests window. Local models can be exhausted the same way.

### `windows[]`: one bar

| Field | Meaning |
|-------|---------|
| `kind` | `requests` or `tokens`. |
| `window` | `minute` or `day`. |
| `remaining` | What is left, or `null` if the provider did not report it (never `-1`). |
| `limit` | The window's size, or `null` if not reported. |
| `resets_at` | When the window is full again, or `null` if the provider did not say, or if `reset` is `true`. |
| `checked_at` | When this window was last read from the provider (a response, or the poller). "Updated X ago" is `now - checked_at`. |
| `exhausted` | `true` if this window is used up and `resets_at` is still ahead. A token window counts as used up below 100 tokens, since no real request fits. |
| `reset` | `true` if the reset time passed since the reading. |

Drawing a bar:

- **% used** is `(limit - remaining) / limit` when both are present. When
  either is `null` there is nothing to draw a percentage from: show the
  number you have (for example "850 left") or nothing.
- **A window that has reset** (`reset: true`) is known to be full again
  without sending a probe, so `remaining` is already set to `limit` (or `null`
  if the size was never reported; treat that as 0% used), and `resets_at` is
  `null` because the next reset is not known until the next reading. Show it
  as 0% used with no reset time. `checked_at` still tells how old the real
  reading is.
- **The bar closest to running out** is the one with the highest % used among
  a model's windows (or any `exhausted` one).
- **Resets in / at**: for a minute window show `resets_at - now` in seconds,
  for a day window the time of day (a rolling reset time as the
  provider reports it).

### What the UI shows, and where it comes from

| UI element | Field |
|------------|-------|
| One bar per window, per model | `windows[]` of that model |
| % used | `limit`, `remaining` |
| "resets in 42s" / "resets 00:00 UTC" | `resets_at` against `now` |
| Per minute / per day, requests / tokens | `window`, `kind` |
| Highlight the bar closest to running out | highest % used in `windows[]`, or `exhausted` |
| Provider summary in the picker (tightest bar) | the tightest of its models' `windows[]` |
| Auto models show the bars of the provider Auto would use next | `routes[].next.id`, looked up in `providers[].models[].id` |
| Auto model with nothing usable | `routes[].status` `exhausted`/`unavailable`, `routes[].resets_at` |
| Greyed out with a reset time | `models[].exhausted`, `models[].resets_at` |
| "Usage remaining unknown" | `models[].status == "unknown"` |
| "Unlimited (local)" and online/offline | `status == "unlimited"`, `providers[].health.online` |
| "No key" / disabled | `enabled`, `status == "disabled"` |
| "Updated 3 min ago" | `now - windows[].checked_at` |
| Show as reset without a probe | `windows[].reset` |
| The UI key's own cap, shown separately | `key` |

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
- A virtual model tries its entries in order and answers from the first that
  works (see [Fallback](#fallback)). A pinned model only ever uses that model.
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
| `x-router-provider` | `groq` | The provider that answered, or the last one called when none did |
| `x-router-model` | `llama-3.3-70b-versatile` | That provider's own model ID (the same as `model` in an error body) |
| `x-router-fallback` | `groq/openai/gpt-oss-20b=rate_limited_day` | Only for a virtual model: the entries that did not answer, in order (see [Fallback](#fallback)). Absent when the first entry answered |

The first two together give the pinned ID, `groq/llama-3.3-70b-versatile`.
They are absent when no provider was called: the request never got as far as
choosing a model (bad body, unknown model, a route with no enabled provider),
or every entry of a route was skipped.

All three are set before the status line, so a streaming client has them
before the first token.

### Fallback

A **virtual model** (`fast`, `long-context`, ...) is an ordered list of
`provider/model` entries. The router tries them in order and answers from the
first that works. Entries whose provider is not enabled (no key registered)
are left out silently.

Before calling an entry, the router skips it, without spending a request, if

- it already knows the model is out of quota (from the provider's rate-limit
  headers or an earlier 429) and the reset time has not passed yet, or
- a model with a context window at least as large has already rejected this
  request as too long.

When a call fails, what happens next depends on why:

| The entry failed with | The route then |
|-----------------------|----------------|
| A rate limit (429) | Tries the next entry. The model is skipped from now until its reset |
| A provider error: 5xx, timeout, network error, an unexpected status, a rejected or missing provider key | Tries the next entry |
| Prompt too long for the model's context window | Tries the next entry **with a bigger context window**; entries no bigger are skipped |
| Request too large for the provider (413) | Tries the next entry. This is the provider's own size cap, so no entry is ruled out |
| Invalid request (400/422) | Tries the next entry, because providers accept different parameters. When a second, different provider also rejects it, the route stops and returns that 400 |

Each entry is tried at most once per request. A route also stops starting new
attempts once it has spent 90 seconds on a request; that only matters when
providers hang instead of failing, and an attempt already running is never
cut short. If the client disconnects, the router stops at once: no further
entry is called.

**Fallback only happens before the first token.** The router reads a
provider's first stream event before it sends anything, so a provider that
fails with an error status, with an error as its first event, or by closing
the stream before sending anything is simply replaced by the next entry, and
the client sees one clean stream from the provider named in
`x-router-provider`. Once a chunk has been forwarded the route is over: a
later failure is reported inside the stream (see below) and no other provider
is called.

A **pinned model** (`provider/model`) never falls back and is never skipped.
The request is always sent to the provider, even when the router believes the
model is out of quota, so retrying a pinned model always asks the provider
again; its failure is the answer (see [Pinned models](#pinned-models)).

#### `x-router-fallback`

Set on any response to a virtual model, success or error, in which at least
one entry did not answer. It lists those entries in the order they were
tried, so a UI can say "Groq hit its daily limit → switched to Gemini":

```
x-router-fallback: groq/openai/gpt-oss-20b=rate_limited_day, gemini/gemini-3.5-flash-lite=provider_error
```

On a success, the entry that answered is not in the list (it is in
`x-router-provider` / `x-router-model`). On an error, every entry tried is in
it, the last one included.

```
value   = attempt *( ", " attempt )
attempt = provider "/" model "=" reason
```

| `reason` | Meaning |
|----------|---------|
| `rate_limited_minute` | A per-minute limit: the provider answered 429, or the entry was skipped because the router knew |
| `rate_limited_day` | The same for a per-day limit |
| `rate_limited` | A rate limit whose window is not known |
| `context_length_exceeded` | The prompt is too long for the model, or (413) the request too large for the provider; also an entry skipped as too small |
| `invalid_request` | The provider rejected the request (400/422) |
| `provider_error` | 5xx, timeout, network error, a rejected or missing provider key |

The reasons are a closed set, in lower-case letters and `_`.

To parse it: split at `,` and trim spaces; split each item at its only `=`;
split the left side at its **first** `/` (provider names never contain one,
model IDs often do); percent-decode the provider and the model. Model IDs are
written as they are, including their `/`, `:` and `@`
(`openrouter/google/gemma-4-26b-a4b-it:free`, `workers-ai/@cf/meta/llama-3`).
Any other character outside `A-Z a-z 0-9 - . _ ~` is percent-encoded, so a
comma, `=` or space can only ever be a separator.

### Errors before and during a stream

The router reads the provider's first event before it sends anything. So if
the provider fails before producing any output (an error status, an error as
its first event, or a stream that ends without one), the client gets a normal
error response with the right status (429, 502, ...) in the format below,
even for a streaming request, or, for a virtual model, the answer of the next
entry.

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
    "message": "groq/openai/gpt-oss-120b hit its daily request limit. It resets at 2026-11-14T00:00:00Z.",
    "type": "rate_limited",
    "code": "rate_limit_exceeded",
    "provider": "groq",
    "model": "openai/gpt-oss-120b",
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
| `resets_at` | When the limit clears, UTC RFC 3339. Absent when unknown (a provider that reports neither a reset time nor how long to wait, e.g. Gemini at times) |
| `attempts` | Only for `all_providers_exhausted`, see below |

Messages are safe to show to a person: they never contain keys, request
bodies or headers.

### Types and status codes

| `type` | HTTP status | When |
|--------|-------------|------|
| `rate_limited` | 429 | A provider's limit, or a service key's daily cap. `Retry-After` is set when `resets_at` is known |
| `context_length_exceeded` | 400 | The prompt is too long for the model (for a virtual model: for every entry). Retrying the same request will not help |
| `provider_error` | 502 | The provider failed (5xx, timeout, network error, or the router's provider key was rejected). 503 when the router itself can't serve the request (`code: auth_not_configured`, `provider_not_enabled`, `no_enabled_provider`) |
| `all_providers_exhausted` | 429 or 503 | Every entry of a virtual model failed or was skipped. 429 if every attempt was a rate limit, otherwise 503; `Retry-After` is set when a reset is known |
| `invalid_request` | 400, 404, 405, 413 | The request is wrong: bad body, a provider rejecting the request (with `provider` and `model`), unknown endpoint (`code: not_found`), unknown model (`code: model_not_found`), wrong method (`code: method_not_allowed`, with an `Allow` header), body too large (`code: request_too_large`) |
| `unauthorized` | 401 or 403 | 401: no valid API key (`/v1/*`) or sign-in (`/api/*`). 403 (`code: forbidden`, or `cross_origin_forbidden` for a cross-site browser request): signed in but not allowed |

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
    "model": "openai/gpt-oss-120b"
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
    "model": "gemini-3.5-flash-lite"
  }
}
```

### Pinned models

A pinned model (`provider/model`) never falls back, so the provider's failure
is the answer: a 429 carries that provider's `limit_type`, `window` and
`resets_at`, and the other types carry its `provider` and `model`.

The limit fields come from what the router has learned about the model: the
window the provider's rate-limit headers showed as used up, or, when it sent
only `Retry-After`, a `requests` limit lasting that long (`minute` for up to
two minutes, otherwise `day`). When the provider gave neither headers nor a
time to wait, all three are left out rather than guessed; a UI then has no
countdown to show.

The request is sent to the provider every time, whatever the router believes
about its quota. Retrying a pinned model therefore always gets the provider's
own, current answer.

### `all_providers_exhausted`

A virtual model (`fast`, `long-context`, ...) tries its entries in order (see
[Fallback](#fallback)). If none answers, `attempts` lists every entry in the
order tried, with why it did not answer. `type` uses the same words as an
error's type, and the limit fields appear when the attempt was a rate limit.
An entry that was skipped without being called is listed too, with a
`message` that starts with `Not tried:`.

For an attempt, `resets_at` is when the router will next try that model: the
provider's reset time when it gave one, otherwise the end of the router's own
back-off (the provider's `Retry-After`, or one minute). The top-level
`resets_at` is the earliest of them, the first moment a retry could reach a
provider, and `Retry-After` is set from it.

The status is 429 when every attempt was a rate limit, otherwise 503. A route
whose entries are all known to be out of quota answers 429 at once, without
calling anyone.

Not every failed route is `all_providers_exhausted`. When the problem is the
request, the client gets an ordinary 400 naming the provider that judged it,
exactly as for a pinned model, and `x-router-fallback` still lists the
entries:

- `context_length_exceeded`: every entry rejected the prompt as too long, or
  was too small to be worth asking.
- `invalid_request`: two different providers rejected the request, or every
  entry did.

A 503 would tell an SDK to retry a request that can never succeed. If even
one entry failed for another reason (rate limited, down), the answer stays
`all_providers_exhausted`, because that entry might serve the request later.

If a route runs out of time (see [Fallback](#fallback)), the entries it never
reached are not listed.

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
        "model": "openai/gpt-oss-120b",
        "type": "rate_limited",
        "message": "Not tried: already known to be out of quota.",
        "limit_type": "requests",
        "window": "minute",
        "resets_at": "2026-11-13T12:01:00Z"
      },
      {
        "provider": "gemini",
        "model": "gemini-3.5-flash-lite",
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
