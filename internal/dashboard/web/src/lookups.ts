// Agent / outcome / sweep-task lookup tables, ported from the original
// index.html. SWEEP_TASKS mirrors the catalog in internal/sweep/task_*.go.

export interface AgentInfo {
  name: string
  color: string
}

const AGENT: Record<string, AgentInfo> = {
  claude: { name: 'Claude', color: '#B79BE8' },
  codex: { name: 'Codex', color: '#7FD8B0' },
  copilot: { name: 'Copilot', color: '#7FA8E8' },
  antigravity: { name: 'Antigravity', color: '#E8A66B' },
}

export function agentInfo(id: string | undefined | null): AgentInfo {
  if (!id) return { name: '—', color: '#828BA3' }
  const key = String(id).toLowerCase()
  if (AGENT[key]) return AGENT[key]
  return { name: id.charAt(0).toUpperCase() + id.slice(1), color: '#828BA3' }
}

export interface OutcomeInfo {
  label: string
  color: string
  bg: string
}

const OUT: Record<string, OutcomeInfo> = {
  merged: { label: 'merged', color: '#6EE7A8', bg: 'rgba(110,231,168,0.13)' },
  pr_opened: { label: 'PR opened', color: '#8FB4FF', bg: 'rgba(143,180,255,0.13)' },
  blocked: { label: 'blocked', color: '#F4B860', bg: 'rgba(244,184,96,0.13)' },
  failed: { label: 'failed', color: '#FB7185', bg: 'rgba(251,113,133,0.13)' },
  no_change: { label: 'no change', color: '#828BA3', bg: 'rgba(130,139,163,0.13)' },
}

export function outInfo(status: string | undefined): OutcomeInfo {
  return OUT[status || ''] || { label: status || '—', color: '#828BA3', bg: 'rgba(130,139,163,0.13)' }
}

// Donut segment ordering.
export const OUTCOME_ORDER = ['merged', 'pr_opened', 'no_change', 'blocked', 'failed']

export interface SweepTaskDef {
  name: string
  label: string
}

export const SWEEP_TASKS: SweepTaskDef[] = [
  { name: 'lint-cleanup', label: 'lint' },
  { name: 'dead-code', label: 'dead-code' },
  { name: 'deps-update', label: 'deps' },
  { name: 'test-coverage', label: 'test-cov' },
  { name: 'doc-drift', label: 'doc-drift' },
  { name: 'modernize', label: 'modernize' },
  { name: 'bug-scan', label: 'bug-scan' },
]
