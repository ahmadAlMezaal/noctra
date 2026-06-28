import type { ComponentChildren } from 'preact'
import type { HistoryEntry } from '../types'
import { withinDays, serifAttr, monoAttr } from '../format'
import { outInfo, OUTCOME_ORDER } from '../lookups'

interface Props {
  history: HistoryEntry[]
}

interface Seg {
  label: string
  color: string
  value: number
}

export function Donut({ history }: Props) {
  const rows = history.filter((r) => withinDays(r.started_at, 14))
  const counts: Record<string, number> = {}
  rows.forEach((r) => {
    counts[r.status] = (counts[r.status] || 0) + 1
  })
  const segs: Seg[] = OUTCOME_ORDER.filter((s) => counts[s]).map((s) => ({
    label: outInfo(s).label,
    color: outInfo(s).color,
    value: counts[s],
  }))
  Object.keys(counts).forEach((s) => {
    if (OUTCOME_ORDER.indexOf(s) === -1) {
      segs.push({ label: outInfo(s).label, color: outInfo(s).color, value: counts[s] })
    }
  })
  const total = segs.reduce((a, c) => a + c.value, 0)

  if (total === 0) {
    return (
      <Wrap>
        <div class="empty">No runs in the last 14 days.</div>
      </Wrap>
    )
  }

  const R = 70
  const C = 2 * Math.PI * R
  let acc = 0
  const arcs = segs.map((s) => {
    const portion = (s.value / total) * C
    const len = Math.max(0, portion - 2.5)
    const arc = (
      <circle
        cx="100"
        cy="100"
        r="70"
        fill="none"
        stroke={s.color}
        stroke-width="22"
        stroke-dasharray={len.toFixed(2) + ' ' + (C - len).toFixed(2)}
        stroke-dashoffset={(-acc).toFixed(2)}
      />
    )
    acc += portion
    return arc
  })

  return (
    <Wrap>
      <div class="donut-wrap">
        <svg viewBox="0 0 200 200">
          <g transform="rotate(0 100 100)">
            <circle cx="100" cy="100" r="70" fill="none" stroke="#1C2433" stroke-width="22" />
            <g transform="rotate(-90 100 100)">{arcs}</g>
          </g>
          <text x="100" y="94" text-anchor="middle" font-family={serifAttr} font-size="34" fill="#E9EDF6">
            {total}
          </text>
          <text x="100" y="116" text-anchor="middle" font-family={monoAttr} font-size="10" letter-spacing="2" fill="#828BA3">
            RUNS
          </text>
        </svg>
        <div class="donut-legend">
          {segs.map((s) => (
            <div class="legend-row">
              <span class="legend-sw" style={{ background: s.color }} />
              <span class="legend-label">{s.label}</span>
              <span class="legend-pct">{Math.round((s.value / total) * 100) + '%'}</span>
            </div>
          ))}
        </div>
      </div>
    </Wrap>
  )
}

function Wrap({ children }: { children: ComponentChildren }) {
  return (
    <section class="panel" style={{ flex: '1 1 280px' }}>
      <div class="phead">
        <span class="ptitle">Run outcomes</span>
        <span class="kicker right">14 days</span>
      </div>
      <div>{children}</div>
    </section>
  )
}
