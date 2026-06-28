import type { Snapshot } from '../types'
import { fmtMoney } from '../format'
import { adminPost } from '../api'
import { Control } from './Control'

interface Props {
  snapshot: Snapshot | null
  conn: { state: string; text: string }
  adminEnabled: boolean
}

export function TopBar({ snapshot, conn, adminEnabled }: Props) {
  const b = (snapshot && snapshot.budget) || ({} as Snapshot['budget'])
  const paused = !!(snapshot && snapshot.paused)
  const version = (snapshot && snapshot.version) || '—'
  const cap = b.MaxDailyUSD > 0 ? '/ ' + fmtMoney(b.MaxDailyUSD) : ''

  const pauseAction = () => {
    const action = paused ? 'resume' : 'pause'
    if (!confirm(action === 'pause' ? 'Pause dispatching?' : 'Resume dispatching?')) return null
    return adminPost('/api/' + action).then((r) => ({
      ok: !r.error,
      msg: r.error || (action === 'pause' ? 'Paused' : 'Resumed'),
    }))
  }

  return (
    <header class="topbar">
      <div class="brand">
        <span class="brand-moon">&#127769;</span>
        <span class="brand-name">Noctra</span>
        <span class="pill-version">{'v' + version}</span>
        <span class={'conn ' + conn.state}>{conn.text}</span>
      </div>
      <div class="capsule">
        {!paused && (
          <span class="dot dot-lamp lamp-dot" />
        )}
        <span class="cap-label">dispatch</span>
        <span class="cap-value" style={{ color: paused ? 'var(--muted)' : 'var(--lamp)' }}>
          {paused ? 'paused' : 'running'}
        </span>
      </div>
      {adminEnabled && (
        <Control label={paused ? 'Resume dispatch' : 'Pause dispatch'} action={pauseAction} reenable />
      )}
      <div class="spent">
        <span class="spent-kicker">spent today</span>
        <span class="spent-value">{fmtMoney(b.DailyCostUSD || 0)}</span>
        <span class="spent-cap">{cap}</span>
      </div>
    </header>
  )
}
