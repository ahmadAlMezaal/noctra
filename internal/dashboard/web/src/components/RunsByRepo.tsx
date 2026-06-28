import type { HistoryEntry } from '../types'
import { withinDays, clip, monoAttr } from '../format'

interface Props {
  history: HistoryEntry[]
}

export function RunsByRepo({ history }: Props) {
  const rows = history.filter((r) => withinDays(r.started_at, 14))
  const counts: Record<string, number> = {}
  const order: string[] = []
  rows.forEach((r) => {
    const k = r.repo || '(unknown)'
    if (!(k in counts)) {
      counts[k] = 0
      order.push(k)
    }
    counts[k]++
  })
  order.sort((a, b) => counts[b] - counts[a])

  return (
    <section class="panel chart" style={{ flex: '1 1 300px' }}>
      <div class="phead">
        <span class="ptitle">Runs by repo</span>
        <span class="kicker right">14 days</span>
      </div>
      <div>
        {!order.length ? (
          <div class="empty">No runs in the last 14 days.</div>
        ) : (
          <Chart order={order} counts={counts} />
        )}
      </div>
    </section>
  )
}

function Chart({ order, counts }: { order: string[]; counts: Record<string, number> }) {
  const max = Math.max.apply(null, order.map((k) => counts[k]).concat([1]))
  const barMax = 320,
    x0 = 150
  const shown = order.slice(0, 6)
  const h = Math.max(60, 20 + shown.length * 40)

  return (
    <svg viewBox={'0 0 510 ' + h}>
      {shown.map((k, i) => {
        const y = 20 + i * 40
        const w = (counts[k] / max) * barMax
        return (
          <g>
            <text x="0" y={y} dominant-baseline="hanging" font-family={monoAttr} font-size="11.5" fill="#C7CEDF">
              {clip(k, 20)}
            </text>
            <rect x={x0} y={y} width={barMax} height="13" rx="6.5" fill="#1C2433" />
            <rect x={x0} y={y} width={w.toFixed(1)} height="13" rx="6.5" fill="#8FB4FF" />
            <text x="478" y={y} dominant-baseline="hanging" font-family={monoAttr} font-size="12" fill="#A6AFC4">
              {counts[k]}
            </text>
          </g>
        )
      })}
    </svg>
  )
}
