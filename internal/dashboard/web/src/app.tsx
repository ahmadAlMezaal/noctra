import { useEffect, useState } from 'preact/hooks'
import { authURL, fetchJSON, adminToken } from './api'
import type { Snapshot, HistoryEntry, CostResponse, SpendEntry, SweepEntry, PREntry } from './types'
import { TopBar } from './components/TopBar'
import { Kpis } from './components/Kpis'
import { ActiveRuns } from './components/ActiveRuns'
import { Queue } from './components/Queue'
import { RunHistory } from './components/RunHistory'
import { Donut } from './components/Donut'
import { TokenCost } from './components/TokenCost'
import { Spend } from './components/Spend'
import { RunsByRepo } from './components/RunsByRepo'
import { Throughput } from './components/Throughput'
import { Budget } from './components/Budget'
import { RepoCards } from './components/RepoCards'
import { SweepMatrix } from './components/SweepMatrix'
import { LogOverlay } from './components/LogOverlay'

export function App() {
  const [snapshot, setSnapshot] = useState<Snapshot | null>(null)
  const [history, setHistory] = useState<HistoryEntry[]>([])
  const [cost, setCost] = useState<CostResponse>({ buckets: [] })
  const [spend, setSpend] = useState<SpendEntry[]>([])
  const [sweeps, setSweeps] = useState<SweepEntry[]>([])
  const [prs, setPrs] = useState<PREntry[]>([])
  const [adminEnabled, setAdminEnabled] = useState(false)
  const [conn, setConn] = useState({ state: 'reconnecting', text: 'Connecting' })
  const [errorMsg, setErrorMsg] = useState<string | null>(null)
  const [logId, setLogId] = useState<string | null>(null)

  useEffect(() => {
    let es: EventSource | null = null
    let auxTimer: ReturnType<typeof setTimeout> | null = null

    function loadAux() {
      Promise.all([
        fetchJSON<HistoryEntry[]>(authURL('/api/history?limit=300')),
        fetchJSON<CostResponse>(authURL('/api/cost?days=14')),
        fetchJSON<SpendEntry[]>(authURL('/api/spend')),
        fetchJSON<SweepEntry[]>(authURL('/api/sweeps')),
        fetchJSON<PREntry[]>(authURL('/api/prs')),
      ])
        .then(([h, c, sp, sw, pr]) => {
          setErrorMsg(null)
          setHistory(h || [])
          setCost(c || { buckets: [] })
          setSpend(sp || [])
          setSweeps(sw || [])
          setPrs(pr || [])
        })
        .catch((e) => {
          setErrorMsg('Failed to load dashboard data: ' + (e as Error).message)
        })
    }

    function scheduleAux() {
      if (auxTimer) return
      auxTimer = setTimeout(() => {
        auxTimer = null
        loadAux()
      }, 750)
    }

    function connect() {
      if (es) es.close()
      setConn({ state: 'reconnecting', text: 'Connecting' })
      es = new EventSource(authURL('/api/events'))
      es.addEventListener('open', () => setConn({ state: 'live', text: 'Live' }))
      es.addEventListener('snapshot', (ev) => {
        setConn({ state: 'live', text: 'Live' })
        try {
          setSnapshot(JSON.parse((ev as MessageEvent).data) as Snapshot)
        } catch {
          return
        }
        scheduleAux()
      })
      es.addEventListener('error', () => setConn({ state: 'reconnecting', text: 'Reconnecting' }))
    }

    fetchJSON<{ admin_enabled?: boolean }>(authURL('/api/admin-status'))
      .then((s) => setAdminEnabled(!!(s && s.admin_enabled) && adminToken !== ''))
      .catch(() => {})
      .then(() => {
        connect()
        loadAux()
      })

    return () => {
      if (es) es.close()
      if (auxTimer) clearTimeout(auxTimer)
    }
  }, [])

  return (
    <>
      <TopBar snapshot={snapshot} conn={conn} adminEnabled={adminEnabled} />
      <div class={'error' + (errorMsg ? ' show' : '')}>{errorMsg}</div>
      <LogOverlay id={logId} onClose={() => setLogId(null)} />
      <Kpis snapshot={snapshot} history={history} cost={cost} />
      <ActiveRuns snapshot={snapshot} adminEnabled={adminEnabled} onLog={setLogId} />
      <Queue snapshot={snapshot} adminEnabled={adminEnabled} onLog={setLogId} />
      <div class="row">
        <RunHistory history={history} onLog={setLogId} />
        <Donut history={history} />
      </div>
      <div class="row">
        <TokenCost cost={cost} />
        <Spend spend={spend} />
      </div>
      <div class="row">
        <RunsByRepo history={history} />
        <Throughput history={history} />
        <Budget snapshot={snapshot} />
      </div>
      <RepoCards history={history} prs={prs} />
      <SweepMatrix sweeps={sweeps} history={history} />
    </>
  )
}
