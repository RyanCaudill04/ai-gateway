// Talks to the router's /api routes (the UI only ever calls /api).

export interface ModelInfo {
  id: string
  provider: string
  local: boolean
  virtual: boolean
}

export interface ChatMessage {
  role: 'user' | 'assistant'
  content: string
}

/** The router's errors are `{"error": {"message": ...}}`; fall back to the status line. */
async function errorMessage(res: Response): Promise<string> {
  try {
    const body = await res.json()
    if (body?.error?.message) return body.error.message
  } catch {
    // not JSON
  }
  return `${res.status} ${res.statusText}`.trim()
}

/** The models the router can serve right now. */
export async function listModels(signal?: AbortSignal): Promise<ModelInfo[]> {
  const res = await fetch('/api/models', { signal })
  if (!res.ok) throw new Error(await errorMessage(res))
  return (await res.json()).data
}

/**
 * Sends the conversation and calls onToken with each piece of the reply as it
 * arrives. Resolves when the reply is finished; rejects with the router's
 * message on an error (including one that arrives mid-stream). Abort the
 * signal to stop early.
 */
export async function streamChat(
  model: string,
  messages: ChatMessage[],
  onToken: (text: string) => void,
  signal?: AbortSignal,
): Promise<void> {
  const res = await fetch('/api/chat/completions', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ model, messages, stream: true }),
    signal,
  })
  if (!res.ok || !res.body) throw new Error(await errorMessage(res))

  // Server-sent events: "data: {json}" lines, events separated by a blank line.
  const reader = res.body.pipeThrough(new TextDecoderStream()).getReader()
  let buffer = ''
  for (;;) {
    const { done, value } = await reader.read()
    if (done) return
    buffer += value

    let end: number
    while ((end = buffer.indexOf('\n\n')) >= 0) {
      const event = buffer.slice(0, end)
      buffer = buffer.slice(end + 2)
      for (const line of event.split('\n')) {
        if (!line.startsWith('data:')) continue
        const data = line.slice(5).trim()
        if (data === '[DONE]') return
        const chunk = JSON.parse(data)
        if (chunk.error) throw new Error(chunk.error.message)
        const text = chunk.choices?.[0]?.delta?.content
        if (text) onToken(text)
      }
    }
  }
}
