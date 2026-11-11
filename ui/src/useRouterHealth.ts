import { useEffect, useState } from 'react'

export type RouterHealth = 'checking' | 'ok' | 'down'

export const HEALTH_LABEL: Record<RouterHealth, string> = {
  checking: 'Checking…',
  ok: 'Router is up',
  down: 'Router is unreachable',
}

/** Asks the router's health endpoint once, when the app loads. */
export function useRouterHealth(): RouterHealth {
  const [health, setHealth] = useState<RouterHealth>('checking')

  useEffect(() => {
    const controller = new AbortController()
    fetch('/api/health', { signal: controller.signal, cache: 'no-store' })
      .then((res) => (res.ok ? res.json() : Promise.reject(res.status)))
      .then((body: { status?: string }) =>
        setHealth(body.status === 'ok' ? 'ok' : 'down'),
      )
      .catch((err: unknown) => {
        if (!controller.signal.aborted) {
          console.error('health check failed', err)
          setHealth('down')
        }
      })
    return () => controller.abort()
  }, [])

  return health
}
