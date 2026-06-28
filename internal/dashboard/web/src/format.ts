export function pad2(n: number): string {
  return String(n).padStart(2, '0')
}

export function fmtTokens(n: number): string {
  n = n || 0
  if (n >= 1e6) return (n / 1e6).toFixed(n % 1e6 === 0 ? 0 : 1) + 'M'
  if (n >= 1e3) return (n / 1e3).toFixed(n % 1e3 === 0 ? 0 : 1) + 'K'
  return String(Math.round(n))
}

export function fmtMoney(n: number): string {
  return '$' + (n || 0).toFixed(2)
}

export function fmtDur(secs: number): string {
  secs = Math.round(secs || 0)
  if (secs <= 0) return '—'
  let m = Math.floor(secs / 60)
  const s = secs % 60
  if (m < 60) return m + 'm ' + pad2(s) + 's'
  const h = Math.floor(m / 60)
  m = m % 60
  return h + 'h ' + pad2(m) + 'm'
}

export function fmtElapsed(secs: number): string {
  secs = Math.max(0, Math.floor(secs))
  return pad2(Math.floor(secs / 60)) + ':' + pad2(secs % 60)
}

export function fmtClock(secs: number): string {
  secs = Math.max(0, Math.floor(secs))
  return pad2(Math.floor(secs / 3600)) + ':' + pad2(Math.floor((secs % 3600) / 60)) + ':' + pad2(secs % 60)
}

export function fmtAgo(iso: string | null | undefined): string {
  if (!iso) return '—'
  const then = new Date(iso).getTime()
  if (isNaN(then)) return '—'
  const s = Math.max(0, (Date.now() - then) / 1000)
  if (s < 60) return Math.round(s) + 's ago'
  let m = Math.floor(s / 60)
  if (m < 60) return m + 'm ago'
  const h = Math.floor(m / 60)
  m = m % 60
  if (h < 24) return h + 'h ' + (m ? pad2(m) + 'm ' : '') + 'ago'
  return Math.floor(h / 24) + 'd ago'
}

export function fmtCooldown(hours: number): string {
  const min = Math.round((hours || 0) * 60)
  if (min < 1) return 'ready'
  if (min < 60) return min + 'm'
  const h = Math.floor(min / 60)
  const m = min % 60
  if (h < 24) return m === 0 ? h + 'h' : h + 'h ' + pad2(m) + 'm'
  const d = Math.floor(h / 24)
  const rh = h % 24
  return rh === 0 ? d + 'd' : d + 'd ' + rh + 'h'
}

export function repoFromPR(url: string | undefined): string {
  const m = /github\.com\/([^/]+\/[^/]+)\/pull\//.exec(url || '')
  return m ? m[1] : ''
}

export function prNum(url: string | undefined): string {
  const m = /\/pull\/(\d+)/.exec(url || '')
  return m ? '#' + m[1] : '#'
}

export function clip(s: string, n: number): string {
  return s.length > n ? s.slice(0, n - 1) + '…' : s
}

export function todayKey(): string {
  return new Date().toISOString().slice(0, 10)
}

export function dayKey(d: Date): string {
  return d.toISOString().slice(0, 10)
}

export function withinDays(iso: string | undefined, days: number): boolean {
  if (!iso) return false
  const t = new Date(iso).getTime()
  return !isNaN(t) && Date.now() - t <= days * 86400000
}

export function niceMax(v: number): number {
  if (v <= 0) return 1
  const exp = Math.pow(10, Math.floor(Math.log10(v)))
  const f = v / exp
  const nice = f <= 1 ? 1 : f <= 2 ? 2 : f <= 5 ? 5 : 10
  return nice * exp
}

export const serifAttr = "'Fraunces','Hoefler Text',Palatino,Georgia,serif"
export const monoAttr = 'ui-monospace,monospace'
