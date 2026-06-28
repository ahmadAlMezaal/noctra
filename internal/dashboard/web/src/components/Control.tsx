import { useState } from 'preact/hooks'
import { adminPost } from '../api'

export interface ControlResult {
  ok: boolean
  msg: string
}

// A control thunk returns null to cancel (e.g. the user dismissed a confirm),
// or a promise resolving to the transient result to flash beside the button.
export type ControlAction = () => Promise<ControlResult> | null

interface ControlProps {
  label: string
  cls?: string
  action: ControlAction
  // Kill/requeue/retry leave the button disabled (their row re-renders on the
  // next snapshot); the pause toggle re-enables itself.
  reenable?: boolean
}

export function Control({ label, cls, action, reenable }: ControlProps) {
  const [disabled, setDisabled] = useState(false)
  const [result, setResult] = useState<ControlResult | null>(null)

  function flash(r: ControlResult) {
    setResult(r)
    if (reenable) setDisabled(false)
    setTimeout(() => setResult(null), 3000)
  }

  function onClick() {
    const p = action()
    if (!p) return
    setDisabled(true)
    p.then(flash).catch(() => flash({ ok: false, msg: 'Request failed' }))
  }

  return (
    <>
      <button type="button" class={'ctrl-btn ' + (cls || '')} disabled={disabled} onClick={onClick}>
        {label}
      </button>
      {result && (
        <span class={'action-result ' + (result.ok ? 'action-ok' : 'action-err')}>{result.msg}</span>
      )}
    </>
  )
}

export function killAction(id: string): ControlAction {
  return () => {
    if (!confirm('Kill run ' + id + '?')) return null
    return adminPost('/api/kill/' + encodeURIComponent(id)).then((r) => ({
      ok: !r.error,
      msg: r.error || 'Killed',
    }))
  }
}

export function requeueAction(id: string): ControlAction {
  return () => {
    const ctx = prompt('Requeue ' + id + ' with extra context (optional):')
    if (ctx === null) return null
    return adminPost('/api/requeue/' + encodeURIComponent(id), { context: ctx }).then((r) => ({
      ok: !r.error,
      msg: r.error || 'Requeued',
    }))
  }
}

export function retryAction(id: string): ControlAction {
  return () => {
    if (!confirm('Retry ' + id + '?')) return null
    return adminPost('/api/retry/' + encodeURIComponent(id)).then((r) => ({
      ok: !r.error,
      msg: r.error || 'Cleared',
    }))
  }
}
