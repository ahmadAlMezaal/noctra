import type { HistoryEntry } from '../types'
import { dayKey, monoAttr } from '../format'

interface Props {
  history: HistoryEntry[]
}

export function Throughput({ history }: Props) {
  const days: { key: string; label: string }[] = []
  const byDay: Record<string, number> = {}
  for (let i = 9; i >= 0; i--) {
    const d = new Date(Date.now() - i * 86400000)
    const key = dayKey(d)
    days.push({ key, label: String(d.getUTCDate()) })
    byDay[key] = 0
  }
  history.forEach((r) => {
    if (r.status !== 'pr_opened' && r.status !== 'merged') return
    const k = (r.started_at || '').slice(0, 10)
    if (k in byDay) byDay[k]++
  })
  const vals = days.map((d) => byDay[d.key])
  const max = Math.max.apply(null, vals.concat([1]))
  const base = 158,
    top = 24

  return (
    <section class="panel chart" style={{ flex: '1 1 300px' }}>
      <div class="phead">
        <span class="ptitle">PR throughput</span>
        <span class="kicker right">PRs / day</span>
      </div>
      <div>
        <svg viewBox="0 0 570 180">
          <line x1="32" y1="158" x2="560" y2="158" stroke="#1C2433" stroke-width="1" />
          {days.map((d, i) => {
            const v = vals[i]
            const h = (v / max) * (base - top)
            const x = 38 + i * 53
            const y = base - h
            return (
              <g>
                <rect x={x} y={y.toFixed(1)} width="30" height={Math.max(0, h).toFixed(1)} rx="4" fill="#6EE7A8" fill-opacity="0.85" />
                <text x={x + 15} y={(y - 6).toFixed(1)} text-anchor="middle" font-family={monoAttr} font-size="11" fill="#8B93A8">
                  {v}
                </text>
                <text x={x + 15} y="174" text-anchor="middle" font-family={monoAttr} font-size="9" fill="#5E677D">
                  {d.label}
                </text>
              </g>
            )
          })}
        </svg>
      </div>
    </section>
  )
}
