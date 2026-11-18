import { ProgressBar } from '../../components/ui/ProgressBar.tsx'
import { HEADROOM, type ApiKey } from './sample.ts'

// Spec §2: a provider's bar and numbers turn red once it has used more than
// 85% of its free tier for the day.
const HOT = 0.85

/** How much of each provider's free tier is left today. */
export function Headroom() {
  return (
    <section className="well keys-headroom" aria-labelledby="keys-headroom-title">
      <div className="keys-headroom-head">
        <h2 id="keys-headroom-title" className="well-label">
          Free-tier headroom
        </h2>
        <span className="keys-headroom-sub">Requests used today, per provider</span>
      </div>
      {HEADROOM.map((p, i) => {
        const used = p.used / p.limit
        const hot = used > HOT
        return (
          <div key={p.name} className="keys-provider">
            <div className="keys-provider-top">
              <span className="keys-provider-name">{p.name}</span>
              <span className={hot ? 'keys-provider-count keys-fail' : 'keys-provider-count'}>
                {p.used} / {p.limit}
              </span>
            </div>
            <ProgressBar value={used} color={hot ? 'red' : 'green'} height={10} delay={0.3 + i * 0.1} />
          </div>
        )
      })}
    </section>
  )
}

/** The selected key's settings and today's error count. */
export function KeyFacts({ apiKey }: { apiKey: ApiKey }) {
  return (
    <section className="well" aria-label="Key details">
      <dl className="keys-facts">
        <div>
          <dt>Rate limit</dt>
          {/* TODO(api): the per-key rate limit. */}
          <dd>[LIMIT]/min</dd>
        </div>
        <div>
          <dt>Tiers allowed</dt>
          <dd>{apiKey.tiersAllowed}</dd>
        </div>
        <div>
          <dt>Created</dt>
          {/* TODO(api): the key's creation date. */}
          <dd>[DATE]</dd>
        </div>
        <div>
          <dt>Errors today</dt>
          <dd className="keys-fail">{apiKey.errorsToday}</dd>
        </div>
      </dl>
    </section>
  )
}
