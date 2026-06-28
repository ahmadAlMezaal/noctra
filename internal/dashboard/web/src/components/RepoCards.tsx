import type { HistoryEntry, PREntry } from '../types'
import { withinDays, fmtAgo, repoFromPR } from '../format'

interface Props {
  history: HistoryEntry[]
  prs: PREntry[]
}

interface Stat {
  runs7: number
  openPrs: number
  last: string | null
}

export function RepoCards({ history, prs }: Props) {
  const runs7src = history.filter((r) => withinDays(r.started_at, 7))
  const stats: Record<string, Stat> = {}
  const order: string[] = []
  history.forEach((r) => {
    const k = r.repo
    if (!k) return
    if (!(k in stats)) {
      stats[k] = { runs7: 0, openPrs: 0, last: null }
      order.push(k)
    }
    if (!stats[k].last || new Date(r.started_at) > new Date(stats[k].last as string)) stats[k].last = r.started_at
  })
  runs7src.forEach((r) => {
    if (r.repo && stats[r.repo]) stats[r.repo].runs7++
  })
  prs.forEach((p) => {
    const rp = repoFromPR(p.pr_url)
    // PR repos are owner/name; match by suffix against history repo slugs.
    order.forEach((k) => {
      if (rp && (rp === k || rp.split('/').pop() === k)) stats[k].openPrs++
    })
  })
  order.sort((a, b) => new Date(stats[b].last as string).getTime() - new Date(stats[a].last as string).getTime())

  return (
    <section class="repos">
      <span class="repos-title">Repositories</span>
      <div class="repo-grid">
        {!order.length ? (
          <div class="empty">No repository activity yet.</div>
        ) : (
          order.map((k) => {
            const s = stats[k]
            return (
              <div class="repo-card">
                <div class="repo-card-head">
                  <span class="dot" style={{ width: '7px', height: '7px', background: 'var(--blue)' }} />
                  <span class="repo-card-name">{k}</span>
                </div>
                <div class="repo-stats">
                  <div class="repo-stat">
                    <span class="repo-stat-num">{s.openPrs}</span>
                    <span class="repo-stat-label">open PRs</span>
                  </div>
                  <div class="repo-stat">
                    <span class="repo-stat-num">{s.runs7}</span>
                    <span class="repo-stat-label">runs / wk</span>
                  </div>
                </div>
                <div class="repo-card-foot">
                  <span style={{ marginLeft: 'auto' }}>{'updated ' + fmtAgo(s.last)}</span>
                </div>
              </div>
            )
          })
        )}
      </div>
    </section>
  )
}
