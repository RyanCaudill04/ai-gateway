# Providers

What each upstream's OpenAI-compatible chat endpoint expects, and how it reports failures. The router's
client (`router/internal/upstream`) appends `/chat/completions` to `base_url` and sends the key as
`Authorization: Bearer <key>` (no header for keyless local providers).

Checked on 2026-11-13 against each provider's docs (links below). Error shapes marked *reported* come
from user reports, not official docs; they are covered by tests but may drift.

| Provider | `base_url` | Auth | Rate-limit headers | Context-too-long looks like | Quirks |
|---|---|---|---|---|---|
| Groq | `https://api.groq.com/openai/v1` | Bearer | Every response: `x-ratelimit-limit-requests`, `x-ratelimit-limit-tokens`, `x-ratelimit-remaining-requests`, `x-ratelimit-remaining-tokens`, `x-ratelimit-reset-requests`, `x-ratelimit-reset-tokens` (durations like `2m59.56s`). `retry-after` (seconds) only on 429. | 400 `{"error":{"message":"Please reduce the length of the messages or completion.","type":"invalid_request_error","code":"context_length_exceeded"}}` (*reported*). 413 when one request exceeds the per-minute token limit. | 400 on `logprobs`, `logit_bias`, `top_logprobs`, `messages[].name`, `n` ≠ 1. 498 = flex tier out of capacity. `stream_options` not mentioned in docs. |
| OpenRouter | `https://openrouter.ai/api/v1` | Bearer | `X-RateLimit-Limit`, `X-RateLimit-Remaining`, `X-RateLimit-Reset` (Unix **ms**), only on OpenRouter's own 429s (free-model / key limits), not on successful responses. `Retry-After` on some 429/503. | 400, `error.code` is a **number** (400) and the typed code is in `error.metadata.error_type` = `context_length_exceeded`. | Optional attribution headers `HTTP-Referer`, `X-OpenRouter-Title`. 402 = out of credits, 403 = moderation/guardrail. Errors after generation starts arrive with HTTP 200: an SSE chunk with a top-level `error` (`code` = real status) and `finish_reason: "error"`. Sends `: OPENROUTER PROCESSING` SSE comments as keep-alives. |
| Gemini | `https://generativelanguage.googleapis.com/v1beta/openai` | Bearer (Gemini API key) | None documented for the OpenAI endpoint. 429 bodies can carry a `google.rpc.RetryInfo` detail with `retryDelay` (e.g. `"37s"`). | 400 `INVALID_ARGUMENT`, message "The input token count (N) exceeds the maximum number of tokens allowed (M)." (*reported*). Error bodies are often wrapped in a JSON **array**: `[{"error":{...}}]`. | A bad key is a 400 ("API key not valid"), not 401. Endpoint is beta. `stream_options.include_usage` appears in the docs' examples. |
| Workers AI | `https://api.cloudflare.com/client/v4/accounts/{account_id}/ai/v1` | Bearer (API token with Workers AI permission) | None documented. 429 with code 3036 (daily free allowance used) or 3040 (out of capacity). | Not documented. Context windows are validated in tokens since Feb 2025; the error code/message isn't published. 413 code 3006 = request too large. Errors use Cloudflare's `{"success":false,"errors":[{"code","message"}]}` envelope (*assumed* for the `/ai/v1` endpoint). | Model IDs are `@cf/<vendor>/<model>`. `options.rejectIfBusy` in the body fails fast instead of queueing. |
| GitHub Models | — | — | — | — | **Retired.** GitHub shut the inference API (`models.github.ai`) down on 2026-07-30; it can't be used as a provider. |
| Ollama (local) | `http://<host>:11434/v1` | None (a key is ignored) | None | Usually **no error**: input beyond `num_ctx` is silently truncated (logged server-side only). | `num_ctx` can't be set through the OpenAI API; set `OLLAMA_CONTEXT_LENGTH` or a Modelfile. Unsupported: `tool_choice`, `logit_bias`, `user`, `n`, logprobs. `stream_options.include_usage` is supported. Ollama's hosted API (`https://ollama.com/v1`) does need a key. |

## How the router classifies them

See `router/internal/upstream/errors.go`. In short: 429 is `rate_limited`; a `context_length_exceeded`
code or a known context-length phrase (any status), or a 413, is `context_too_long`; other 400/422 is
`bad_request`; everything else (5xx, timeouts, network errors, 401/403 and Gemini's bad-key 400, 404,
402, …) is `provider_error`.

On streaming requests the router adds `stream_options: {"include_usage": true}` (unless the client set
`stream_options`) so token counts reach the request log. A provider that rejects it with a 400 naming
`stream_options` is retried once without it.

## Sources

- Groq: [rate limits](https://console.groq.com/docs/rate-limits), [errors](https://console.groq.com/docs/errors), [OpenAI compatibility](https://console.groq.com/docs/openai), context-length body as reported in [OpenDevin #1902](https://github.com/OpenDevin/OpenDevin/issues/1902)
- OpenRouter: [quickstart](https://openrouter.ai/docs/quickstart), [errors](https://openrouter.ai/docs/api-reference/errors), [limits](https://openrouter.ai/docs/api-reference/limits)
- Gemini: [OpenAI compatibility](https://aistudio.google.com/docs/openai), token-count error as reported on the [Google AI forum](https://discuss.ai.google.dev/t/help-with-error-the-input-token-count-35868-exceeds-the-maximum-number-of-tokens-allowed-32768/70784)
- Workers AI: [OpenAI compatibility](https://developers.cloudflare.com/workers-ai/configuration/open-ai-compatibility/), [errors](https://developers.cloudflare.com/workers-ai/platform/errors/), [token-based context windows changelog](https://developers.cloudflare.com/changelog/2025-02-24-context-windows)
- GitHub Models: [retirement notice](https://github.blog/changelog/2026-07-30-github-models-is-now-retired/), [docs](https://docs.github.com/en/github-models/use-github-models/prototyping-with-ai-models)
- Ollama: [OpenAI compatibility](https://docs.ollama.com/api/openai-compatibility)
