import { Icon } from '../../components/Icon.tsx'
import { Dot } from '../../components/ui/Dot.tsx'
import { PROVIDERS } from '../../sample.ts'
import { screenHref } from '../../screens.ts'
import { REQUESTS_TODAY, STARTERS } from './sample.ts'

/** The landing page's right-hand card: starters, provider health, today's count. */
export function NewInspector() {
  const up = PROVIDERS.filter((p) => p.up).length

  return (
    <>
      <section className="well new-starters">
        <h2 className="well-label">Start with</h2>
        {STARTERS.map((prompt) => (
          // TODO(api): open a new chat with this prompt instead of the chats screen.
          <a key={prompt} className="new-starter" href={screenHref('chats')}>
            {prompt}
            <Icon name="arrowUpRight" />
          </a>
        ))}
      </section>

      <section className="well new-providers">
        <div className="new-providers-header">
          <h2 className="well-label">Providers</h2>
          <span className={up === PROVIDERS.length ? 'new-providers-up' : 'new-providers-up new-some-down'}>
            {up} of {PROVIDERS.length} up
          </span>
        </div>
        {PROVIDERS.map((p) => (
          <div key={p.name} className="new-provider">
            <span className="new-provider-name">
              <Dot color={p.up ? 'green' : 'red'} />
              {p.name}
            </span>
            <span className="mono new-provider-ms">{p.latencyMs} ms</span>
          </div>
        ))}
      </section>

      <section className="well new-requests-today">
        <h2 className="well-label">Requests today</h2>
        <span className="big-number">{REQUESTS_TODAY}</span>
      </section>
    </>
  )
}
