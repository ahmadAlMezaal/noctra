import { createServer } from 'node:http'
import { readFile } from 'node:fs/promises'
import { join, dirname, extname } from 'node:path'
import { fileURLToPath } from 'node:url'
import { bundle } from './build.mjs'

const root = dirname(fileURLToPath(import.meta.url))
const serveDir = join(root, '.dev')
const port = Number(process.env.PORT || 8080)

await bundle({ watch: true, outDir: serveDir })

const now = Date.now()
const iso = (ms) => new Date(ms).toISOString().slice(0, 19) + 'Z'
const repos = ['noctra', 'sandbox', 'acme/widgets']
const statuses = ['merged', 'pr_opened', 'no_change', 'blocked', 'failed']
const agents = ['claude', 'codex', 'copilot']

const history = Array.from({ length: 24 }, (_, i) => {
  const st = now - i * 3 * 3600 * 1000
  return {
    identifier: `ENG-${300 - i}`,
    ticket_id: `ENG-${300 - i}`,
    pr_url: i % 2 === 0 ? `https://github.com/o/${repos[i % 3].split('/').pop()}/pull/${100 + i}` : '',
    repo: repos[i % 3],
    agent_backend: agents[i % 3],
    run_type: 'ticket',
    status: statuses[i % 5],
    duration_s: 120 + i * 37,
    started_at: iso(st),
    finished_at: iso(st + (120 + i * 37) * 1000),
    linear_url: `https://linear.app/issue/ENG-${300 - i}`,
  }
})

const buckets = Array.from({ length: 14 }, (_, k) => {
  const d = 13 - k
  return {
    date: new Date(now - d * 86400000).toISOString().slice(0, 10),
    cost_usd: Math.round((0.5 + (13 - d) * 0.21 + (d % 3) * 0.4) * 100) / 100,
    total_tokens: 800000 + (13 - d) * 120000 + (d % 4) * 300000,
  }
})

const spend = [
  { agent: 'claude', total_tokens: 1840000, cost_usd: 6.42 },
  { agent: 'codex', total_tokens: 920000, cost_usd: 2.1 },
]

const tasks = ['lint-cleanup', 'dead-code', 'deps-update', 'test-coverage', 'doc-drift', 'modernize', 'bug-scan']
const sweeps = repos.flatMap((r) =>
  tasks.map((t, i) => {
    const cooling = (i + r.length) % 3 === 0
    return {
      repo: r,
      task: t,
      description: t,
      cooldown_h: 168,
      cooldown_left_h: cooling ? 53.5 : 0,
      last_run_at: iso(now - 2 * 86400000),
      eligible: !cooling,
    }
  }),
)

const prs = [
  { pr_url: 'https://github.com/o/noctra/pull/101', ticket_id: 'ENG-300', iterations: 2, max_iterations: 5, capped: false },
  { pr_url: 'https://github.com/o/sandbox/pull/55', ticket_id: 'ENG-298', iterations: 5, max_iterations: 5, capped: true },
]

const snapshot = {
  active: [{ identifier: 'ENG-301', repo: 'noctra', agent: 'claude', run_type: 'ticket', started_at: iso(now - 252000) }],
  queued: [{ identifier: 'ENG-299', repo: 'sandbox', retries: 1 }],
  skipped: ['ENG-295'],
  paused: false,
  budget: {
    SessionTokens: 420000,
    SessionCostUSD: 1.83,
    DailyTokens: 2760000,
    DailyCostUSD: 8.52,
    MaxDailyTokens: 8000000,
    MaxDailyUSD: 20,
    Paused: false,
    PausedUntil: '0001-01-01T00:00:00Z',
    PauseReason: '',
  },
  version: 'dev',
}

const routes = {
  '/api/admin-status': { admin_enabled: true },
  '/api/history': history,
  '/api/cost': { buckets },
  '/api/spend': spend,
  '/api/sweeps': sweeps,
  '/api/prs': prs,
}

const types = { '.html': 'text/html', '.woff2': 'font/woff2', '.js': 'text/javascript', '.css': 'text/css' }

createServer(async (req, res) => {
  const path = (req.url || '/').split('?')[0]

  if (path === '/api/events') {
    res.writeHead(200, { 'content-type': 'text/event-stream', 'cache-control': 'no-cache' })
    const send = () => res.write(`event: snapshot\ndata: ${JSON.stringify(snapshot)}\n\n`)
    send()
    const timer = setInterval(send, 2000)
    req.on('close', () => clearInterval(timer))
    return
  }
  if (path.startsWith('/api/logs/')) {
    res.writeHead(200, { 'content-type': 'text/event-stream', 'cache-control': 'no-cache' })
    res.write(`event: log\ndata: ${JSON.stringify('--- Attempt ---\nDEBUG: sample dev log\nrunning agent…\n')}\n\n`)
    return
  }
  if (path in routes) {
    res.writeHead(200, { 'content-type': 'application/json' })
    res.end(JSON.stringify(routes[path]))
    return
  }

  const rel = path === '/' ? 'index.html' : path.slice(1)
  try {
    const data = await readFile(join(serveDir, rel))
    res.writeHead(200, { 'content-type': types[extname(rel)] || 'application/octet-stream' })
    res.end(data)
  } catch {
    res.writeHead(404)
    res.end('not found')
  }
}).listen(port, () => {
  console.log(`dashboard dev server → http://localhost:${port}/?token=dev&admin_token=dev`)
})
