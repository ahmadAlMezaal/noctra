// Auth + transport. Read token via ?token= (also forwarded onto EventSource
// URLs, which can't send headers); admin token via Authorization header only
// (the server rejects it on the query string for CSRF safety).

const params = new URLSearchParams(window.location.search)
export const token = params.get('token') || ''
export const adminToken = params.get('admin_token') || ''

const headers = { Authorization: 'Bearer ' + token }
const adminHeaders = {
  Authorization: 'Bearer ' + (adminToken || token),
  'Content-Type': 'application/json',
}

export function authURL(path: string): string {
  if (!token) return path
  const sep = path.indexOf('?') === -1 ? '?' : '&'
  return path + sep + 'token=' + encodeURIComponent(token)
}

export function fetchJSON<T>(path: string): Promise<T> {
  return fetch(path, { headers }).then((r) => {
    if (!r.ok) throw new Error('HTTP ' + r.status)
    return r.json() as Promise<T>
  })
}

export interface AdminResult {
  status?: string
  error?: string
  [k: string]: unknown
}

export function adminPost(path: string, body?: unknown): Promise<AdminResult> {
  return fetch(path, {
    method: 'POST',
    headers: adminHeaders,
    body: body ? JSON.stringify(body) : undefined,
  }).then((r) => r.json().catch(() => ({})) as Promise<AdminResult>)
}
