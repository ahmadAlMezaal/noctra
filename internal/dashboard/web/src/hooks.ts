import { useEffect, useState } from 'preact/hooks'

// useNow returns a timestamp that ticks every `ms` while `active` — the single
// 1s heartbeat driving live elapsed timers (active runs) and the pause
// countdown. Scoped to the components that need it so charts don't re-render
// each second.
export function useNow(active: boolean, ms = 1000): number {
  const [now, setNow] = useState(() => Date.now())
  useEffect(() => {
    if (!active) return
    const id = setInterval(() => setNow(Date.now()), ms)
    return () => clearInterval(id)
  }, [active, ms])
  return now
}
