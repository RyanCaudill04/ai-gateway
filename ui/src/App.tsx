import { useEffect, useState } from 'react'
import './App.css'

type Status = 'checking' | 'ok' | 'down'

const STATUS_LABEL: Record<Status, string> = {
  checking: 'Checking…',
  ok: 'Router is up',
  down: 'Router is unreachable',
}

function App() {
  const [status, setStatus] = useState<Status>('checking')

  useEffect(() => {
    const controller = new AbortController()
    fetch('/api/health', { signal: controller.signal, cache: 'no-store' })
      .then((res) => (res.ok ? res.json() : Promise.reject(res.status)))
      .then((body: { status?: string }) =>
        setStatus(body.status === 'ok' ? 'ok' : 'down'),
      )
      .catch((err: unknown) => {
        if (!controller.signal.aborted) {
          console.error('health check failed', err)
          setStatus('down')
        }
      })
    return () => controller.abort()
  }, [])

  return (
    <main className="home">
      <h1>AI Gateway</h1>
      <p className="lede">
        One OpenAI-compatible endpoint that spreads requests across free-tier
        LLM providers, with fallback to local models.
      </p>

      <p className={`status status-${status}`} role="status">
        <span className="dot" aria-hidden="true" />
        {STATUS_LABEL[status]}
      </p>

      <p className="note">
        The chat UI and the <code>/v1</code> API are on the way.
      </p>

      <footer>
        <a href="https://github.com/RyanCaudill04/ai-gateway">Source on GitHub</a>
      </footer>
    </main>
  )
}

export default App
