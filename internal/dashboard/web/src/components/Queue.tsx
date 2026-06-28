import type { Snapshot } from '../types'
import { Control, requeueAction, retryAction } from './Control'

interface Props {
  snapshot: Snapshot | null
  adminEnabled: boolean
  onLog: (id: string) => void
}

export function Queue({ snapshot, adminEnabled, onLog }: Props) {
  const queued = (snapshot && snapshot.queued) || []
  const skipped = (snapshot && snapshot.skipped) || []
  const show = queued.length > 0 || skipped.length > 0

  return (
    <section class={'panel queue-panel' + (show ? ' show' : '')} style={{ flexDirection: 'column' }}>
      <div class="phead">
        <span class="ptitle">Queue &amp; skipped</span>
        <span class="kicker right">retry / requeue</span>
      </div>
      <div class="queue-cols">
        <div class="queue-col">
          {queued.length > 0 && (
            <>
              <h4>{'Queued · ' + queued.length}</h4>
              {queued.map((e) => (
                <div class="queue-row" key={e.identifier}>
                  <span class="queue-id" onClick={() => onLog(e.identifier)}>
                    {e.identifier}
                  </span>
                  <span class="queue-meta">
                    {(e.retries || 0) + ' ' + (e.retries === 1 ? 'retry' : 'retries') + (e.repo ? ' · ' + e.repo : '')}
                  </span>
                  {adminEnabled && <Control label="Requeue" action={requeueAction(e.identifier)} />}
                </div>
              ))}
            </>
          )}
        </div>
        <div class="queue-col">
          {skipped.length > 0 && (
            <>
              <h4>{'Skipped · ' + skipped.length}</h4>
              {skipped.map((id) => (
                <div class="queue-row" key={id}>
                  <span class="queue-id" onClick={() => onLog(id)}>
                    {id}
                  </span>
                  {adminEnabled && <Control label="Retry" action={retryAction(id)} />}
                </div>
              ))}
            </>
          )}
        </div>
      </div>
    </section>
  )
}
