import type { HistoryEntry } from '../types'
import { fmtDur, prNum, withinDays } from '../format'
import { agentInfo, outInfo } from '../lookups'

interface Props {
  history: HistoryEntry[]
  onLog: (id: string) => void
}

export function RunHistory({ history, onLog }: Props) {
  const rows = history.filter((r) => withinDays(r.started_at, 0.5))

  if (rows.length === 0) {
    return (
      <section class="panel" style={{ flex: '2 1 460px' }}>
        <Head />
        <div class="empty">No completed runs in the last 12 hours.</div>
      </section>
    )
  }

  const groups: Record<string, HistoryEntry[]> = {}
  const order: string[] = []
  rows.forEach((r) => {
    const k = r.repo || '(unknown)'
    if (!groups[k]) {
      groups[k] = []
      order.push(k)
    }
    groups[k].push(r)
  })

  return (
    <section class="panel" style={{ flex: '2 1 460px' }}>
      <Head />
      <div>
        {order.map((repo) => {
          const runs = groups[repo]
          return (
            <div class="hist-group">
              <div class="hist-grouphead">
                <span class="hist-repo">{repo}</span>
                <span class="hist-count">{runs.length + (runs.length === 1 ? ' run' : ' runs')}</span>
                <span class="hist-rule" />
              </div>
              {runs.map((r) => {
                const ai = agentInfo(r.agent_backend)
                const oi = outInfo(r.status)
                return (
                  <div class="hist-row">
                    <span class="hist-id" onClick={() => onLog(r.identifier)}>
                      {r.identifier}
                    </span>
                    <span class="hist-agent">
                      <span class="agent-dot" style={{ background: ai.color }} />
                      {ai.name}
                    </span>
                    <span class="outcome-pill" style={{ color: oi.color, background: oi.bg }}>
                      {oi.label}
                    </span>
                    <span class="hist-dur">{fmtDur(r.duration_s)}</span>
                    <span class="hist-ci" style={{ color: 'var(--faint3)' }}>
                      —
                    </span>
                    {r.pr_url ? (
                      <a class="hist-pr" href={r.pr_url} target="_blank" rel="noopener">
                        {'PR ' + prNum(r.pr_url)}
                      </a>
                    ) : (
                      <span class="hist-pr" style={{ color: 'var(--faint3)' }}>
                        —
                      </span>
                    )}
                  </div>
                )
              })}
            </div>
          )
        })}
      </div>
    </section>
  )
}

function Head() {
  return (
    <div class="phead">
      <span class="ptitle">Run history</span>
      <span class="kicker right">completed · last 12h</span>
    </div>
  )
}
