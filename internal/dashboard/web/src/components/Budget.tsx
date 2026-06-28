import type { Snapshot } from '../types'
import { fmtTokens, fmtMoney, fmtClock } from '../format'
import { useNow } from '../hooks'

interface Props {
  snapshot: Snapshot | null
}

interface Row {
  scope: string
  metric: string
  used: string
  capN: number
  money: boolean
  val: number
}

export function Budget({ snapshot }: Props) {
  const b = (snapshot && snapshot.budget) || ({} as Snapshot['budget'])

  const until = b.PausedUntil
  const pausedWithDeadline = !!(snapshot && snapshot.paused && until && until !== '0001-01-01T00:00:00Z')
  const now = useNow(pausedWithDeadline)
  const ms = pausedWithDeadline ? new Date(until).getTime() - now : 0

  const rows: Row[] = [
    { scope: 'Session', metric: 'tokens', used: fmtTokens(b.SessionTokens || 0), capN: 0, money: false, val: b.SessionTokens || 0 },
    { scope: 'Session', metric: 'spend', used: fmtMoney(b.SessionCostUSD || 0), capN: 0, money: true, val: b.SessionCostUSD || 0 },
    { scope: 'Daily', metric: 'tokens', used: fmtTokens(b.DailyTokens || 0), capN: b.MaxDailyTokens || 0, money: false, val: b.DailyTokens || 0 },
    { scope: 'Daily', metric: 'spend', used: fmtMoney(b.DailyCostUSD || 0), capN: b.MaxDailyUSD || 0, money: true, val: b.DailyCostUSD || 0 },
  ]

  return (
    <section class="panel" style={{ flex: '1 1 300px' }}>
      <div class="phead">
        <span class="ptitle">Budget</span>
        <span class="kicker right">caps</span>
      </div>
      <div>
        {rows.map((r) => {
          const hasCap = r.capN > 0
          const pct = hasCap ? Math.min(100, Math.round((r.val / r.capN) * 100)) : 0
          const color = pct >= 80 ? 'var(--lamp)' : 'var(--blue)'
          const capTxt = hasCap ? '/ ' + (r.money ? fmtMoney(r.capN) : fmtTokens(r.capN)) : '/ no cap'
          const tip = hasCap
            ? `${r.scope} · ${r.metric}: ${r.used} of ${r.money ? fmtMoney(r.capN) : fmtTokens(r.capN)} (${pct}%)`
            : `${r.scope} · ${r.metric}: ${r.used} (no cap)`
          return (
            <div class="budget-row" key={r.scope + r.metric} title={tip}>
              <div class="budget-head">
                <span class="budget-label">{r.scope + ' · ' + r.metric}</span>
                <span class="budget-used">{r.used}</span>
                <span class="budget-cap">{capTxt}</span>
              </div>
              <div class="budget-track">
                {hasCap && <div class="budget-fill" style={{ width: pct + '%', background: color }} />}
              </div>
            </div>
          )
        })}
        {pausedWithDeadline && ms > 0 && (
          <div class="pause-callout">
            <div class="pause-l1">
              <span class="dot lamp-dot" style={{ width: '7px', height: '7px', background: 'var(--lamp)' }} />
              <span class="pause-title">dispatch paused</span>
            </div>
            <div class="pause-l2">
              <span class="pause-label">resuming in</span>
              <span class="pause-clock">{fmtClock(ms / 1000)}</span>
            </div>
          </div>
        )}
      </div>
    </section>
  )
}
