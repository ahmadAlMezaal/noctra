import type { HistoryEntry, SweepEntry } from '../types'
import { clip, fmtCooldown } from '../format'
import { SWEEP_TASKS } from '../lookups'

interface Props {
  sweeps: SweepEntry[]
  history: HistoryEntry[]
}

export function SweepMatrix({ sweeps, history }: Props) {
  const byRepo: Record<string, Record<string, SweepEntry>> = {}
  const repoOrder: string[] = []
  sweeps.forEach((s) => {
    if (!(s.repo in byRepo)) {
      byRepo[s.repo] = {}
      repoOrder.push(s.repo)
    }
    byRepo[s.repo][s.task] = s
  })
  history.forEach((r) => {
    if (r.repo && !(r.repo in byRepo)) {
      byRepo[r.repo] = {}
      repoOrder.push(r.repo)
    }
  })

  return (
    <section class="panel">
      <div class="phead">
        <span class="ptitle">Maintenance sweeps</span>
        <span class="kicker">cooldown status</span>
        <span class="legend-inline">
          <span style={{ color: 'var(--ok)' }}>
            <span class="dot" style={{ width: '7px', height: '7px', background: 'var(--ok)' }} />
            eligible
          </span>
          <span style={{ color: 'var(--muted)' }}>
            <span class="dot" style={{ width: '7px', height: '7px', background: '#3A4356' }} />
            cooling down
          </span>
        </span>
      </div>
      <div>
        {!repoOrder.length ? (
          <div class="empty">No repositories tracked for sweeps yet.</div>
        ) : (
          <div class="sweep-scroll">
            <div class="sweep-inner">
              <div class="sweep-headrow">
                <span />
                {SWEEP_TASKS.map((t) => (
                  <span class="sweep-task" key={t.name}>{t.label}</span>
                ))}
              </div>
              {repoOrder.map((repo) => (
                <div class="sweep-datarow" key={repo}>
                  <span class="sweep-repo" title={repo}>
                    {clip(repo, 22)}
                  </span>
                  {SWEEP_TASKS.map((t) => {
                    const s = byRepo[repo][t.name]
                    const eligible = !s || s.eligible
                    const color = eligible ? '#6EE7A8' : '#828BA3'
                    const dotColor = eligible ? '#6EE7A8' : '#3A4356'
                    const label = eligible ? 'ready' : fmtCooldown(s.cooldown_left_h)
                    return (
                      <span class="sweep-cell" key={t.name} style={{ color }}>
                        <span class="sweep-celldot" style={{ background: dotColor }} />
                        {label}
                      </span>
                    )
                  })}
                </div>
              ))}
            </div>
          </div>
        )}
      </div>
    </section>
  )
}
