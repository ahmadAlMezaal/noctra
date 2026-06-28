import { useEffect, useRef } from 'preact/hooks'
import { authURL } from '../api'

interface Props {
  id: string | null
  onClose: () => void
}

export function LogOverlay({ id, onClose }: Props) {
  const ref = useRef<HTMLPreElement>(null)

  useEffect(() => {
    if (!id) return
    const out = ref.current
    if (out) out.textContent = ''
    const es = new EventSource(authURL('/api/logs/' + encodeURIComponent(id) + '?follow=1'))
    es.addEventListener('log', (ev) => {
      const el = ref.current
      if (!el) return
      try {
        el.textContent += JSON.parse((ev as MessageEvent).data)
      } catch {
        return
      }
      el.scrollTop = el.scrollHeight
    })
    es.addEventListener('error', () => {
      const el = ref.current
      if (el) el.textContent += '\n[log stream reconnecting]\n'
    })
    return () => es.close()
  }, [id])

  return (
    <section class={'panel log-panel' + (id ? ' open' : '')}>
      <div class="log-head">
        <span class="ptitle">{id ? 'Log · ' + id : 'Log'}</span>
        <button type="button" class="log-close" onClick={onClose}>
          Close
        </button>
      </div>
      <pre class="log-out" ref={ref}></pre>
    </section>
  )
}
