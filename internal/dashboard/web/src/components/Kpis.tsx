import type { Snapshot, HistoryEntry, CostResponse } from '../types'
import { fmtTokens, fmtMoney, withinDays, todayKey } from '../format'
import { agentInfo } from '../lookups'

interface Props {
  snapshot: Snapshot | null
  history: HistoryEntry[]
  cost: CostResponse
}

interface Kpi {
  label: string
  value: string
  sub: string
  accent: string
}

export function Kpis({ snapshot, history, cost }: Props) {
  const b = (snapshot && snapshot.budget) || ({} as Snapshot['budget'])
  const active = (snapshot && snapshot.active) || []

  const prsOpened = history.filter((r) => r.status === 'pr_opened' || r.status === 'merged')
  const prs14 = prsOpened.filter((r) => withinDays(r.started_at, 14))

  const last7 = history.filter((r) => withinDays(r.started_at, 7))
  const ok7 = last7.filter((r) => r.status === 'pr_opened' || r.status === 'merged')
  const rate = last7.length ? Math.round((ok7.length / last7.length) * 100) + '%' : '—'

  const hist14 = history.filter((r) => withinDays(r.started_at, 14))
  const repoSet: Record<string, boolean> = {}
  hist14.forEach((r) => {
    if (r.repo) repoSet[r.repo] = true
  })
  const repoCount = Object.keys(repoSet).length

  const tk = todayKey()
  let todayTokens = 0
  const prior: number[] = []
  cost.buckets.forEach((bk) => {
    if (bk.date === tk) todayTokens = bk.total_tokens || 0
    else prior.push(bk.total_tokens || 0)
  })
  const avg = prior.length ? prior.reduce((a, c) => a + c, 0) / prior.length : 0
  let pctSub = 'vs avg'
  if (avg > 0) {
    const d = Math.round(((todayTokens - avg) / avg) * 100)
    pctSub = (d >= 0 ? '+' : '') + d + '% vs avg'
  }

  const agentName = active.length ? agentInfo(active[0].agent).name : ''
  const activeSub =
    active.length === 0 ? 'idle' : active.length === 1 ? agentName + ' live' : active.length + ' agents live'

  const costSub = b.MaxDailyUSD > 0 ? 'est. of ' + fmtMoney(b.MaxDailyUSD) + ' cap' : 'est. today'

  const kpis: Kpi[] = [
    { label: 'PRs opened', value: String(prs14.length), sub: 'last 14 days', accent: 'var(--blue)' },
    { label: 'Active runs', value: String(active.length), sub: activeSub, accent: 'var(--lamp)' },
    { label: 'Success rate', value: rate, sub: 'last 7 days', accent: 'var(--ok)' },
    { label: 'Tokens today', value: fmtTokens(todayTokens), sub: pctSub, accent: 'var(--moon)' },
    { label: 'Cost today', value: fmtMoney(b.DailyCostUSD || 0), sub: costSub, accent: 'var(--lamp)' },
    { label: 'Repos active', value: String(repoCount), sub: hist14.length + ' runs / 14d', accent: 'var(--moon)' },
  ]

  return (
    <section class="kpis">
      {kpis.map((k) => (
        <div class="kpi" key={k.label}>
          <span class="kpi-label">{k.label}</span>
          <span class="kpi-value" style={{ color: k.accent }}>
            {k.value}
          </span>
          <span class="kpi-sub">{k.sub}</span>
        </div>
      ))}
    </section>
  )
}
