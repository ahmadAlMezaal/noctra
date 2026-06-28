import type { Snapshot } from '../types'
import { fmtElapsed } from '../format'
import { agentInfo } from '../lookups'
import { useNow } from '../hooks'
import { Control, killAction } from './Control'

interface Props {
  snapshot: Snapshot | null
  adminEnabled: boolean
  onLog: (id: string) => void
}

export function ActiveRuns({ snapshot, adminEnabled, onLog }: Props) {
  const active = (snapshot && snapshot.active) || []
  const lit = active.length > 0
  const now = useNow(lit)

  const kicker = lit
    ? active.length === 1
      ? 'one in flight'
      : active.length + ' in flight'
    : 'idle — waiting for tickets'

  return (
    <section class={'panel active-panel' + (lit ? ' lamp-glow' : ' idle')}>
      <div class="phead" style={{ alignItems: 'center' }}>
        <span
          class={'dot' + (lit ? ' lamp-dot' : '')}
          style={{
            width: '9px',
            height: '9px',
            background: lit ? 'var(--lamp)' : '#3A4356',
            boxShadow: lit ? '0 0 12px 2px rgba(244,184,96,0.75)' : 'none',
          }}
        />
        <span class="ptitle">Active runs</span>
        <span class="kicker" style={{ color: 'var(--lamp)' }}>
          {kicker}
        </span>
      </div>
      <div class="active-cards">
        {!lit ? (
          <div class="empty">No active runs. Waiting for tickets.</div>
        ) : (
          active.map((r) => {
            const ai = agentInfo(r.agent)
            const type = (r.run_type || 'run').toUpperCase()
            const elapsed = r.started_at
              ? fmtElapsed((now - new Date(r.started_at).getTime()) / 1000)
              : '—'
            return (
              <div class="run-card" key={r.identifier}>
                <div class="run-card-top">
                  <span class="run-id" onClick={() => onLog(r.identifier)}>
                    {r.identifier}
                  </span>
                  <span class="type-pill">{type}</span>
                  <span class="running-pill lamp-dot">running</span>
                </div>
                <div class="run-repo">{r.repo || '—'}</div>
                <div class="run-card-foot">
                  <span class="run-agent">
                    <span class="agent-dot" style={{ background: ai.color }} />
                    {ai.name}
                  </span>
                  {adminEnabled && <Control label="Kill" cls="danger" action={killAction(r.identifier)} />}
                  <span class="run-elapsed">{elapsed}</span>
                </div>
              </div>
            )
          })
        )}
      </div>
    </section>
  )
}
