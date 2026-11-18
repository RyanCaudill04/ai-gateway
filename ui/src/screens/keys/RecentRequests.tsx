import { TierDot } from '../../components/ui/Dot.tsx'
import type { KeyRequest } from './sample.ts'

/** The key's latest requests. Rows rise in one after another. */
export function RecentRequests({ requests }: { requests: KeyRequest[] }) {
  return (
    <section className="well keys-recent" aria-labelledby="keys-recent-title">
      <h2 id="keys-recent-title" className="keys-recent-title">
        Recent requests
      </h2>
      {/* Scrolls sideways on narrow screens instead of squashing the columns. */}
      <div className="keys-table-scroll">
        <table className="keys-table">
          <thead>
            <tr>
              <th scope="col">Time</th>
              <th scope="col">Tier</th>
              <th scope="col">Model</th>
              <th scope="col">Status</th>
              <th scope="col" className="keys-num">
                Latency
              </th>
            </tr>
          </thead>
          <tbody>
            {requests.map((r, i) => {
              const ok = r.status === 200
              return (
                <tr
                  key={`${r.time}-${r.model}`}
                  className="keys-table-row"
                  style={{ animationDelay: `${0.1 + i * 0.06}s` }}
                >
                  <td className="keys-time">{r.time}</td>
                  <td>
                    <span className="keys-tier">
                      <TierDot tier={r.tier} />
                      {r.tier}
                    </span>
                  </td>
                  <td className="keys-model">{r.model}</td>
                  <td className={ok ? 'keys-status keys-ok' : 'keys-status keys-fail'}>
                    {ok ? '200 OK' : `${r.status} → fallback`}
                  </td>
                  <td className="keys-num keys-latency">{r.ms} ms</td>
                </tr>
              )
            })}
          </tbody>
        </table>
      </div>
    </section>
  )
}
