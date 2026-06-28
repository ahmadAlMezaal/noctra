export interface Budget {
  SessionTokens: number
  SessionCostUSD: number
  DailyTokens: number
  DailyCostUSD: number
  MaxDailyTokens: number
  MaxDailyUSD: number
  Paused: boolean
  PausedUntil: string
  PauseReason: string
}

export interface ActiveEntry {
  identifier: string
  repo?: string
  agent?: string
  run_type?: string
  started_at?: string
}

export interface QueuedEntry {
  identifier: string
  repo?: string
  retries: number
}

export interface Snapshot {
  active: ActiveEntry[]
  queued: QueuedEntry[]
  skipped: string[]
  paused: boolean
  budget: Budget
  version?: string
}

export interface HistoryEntry {
  identifier: string
  ticket_id?: string
  pr_url?: string
  repo: string
  agent_backend?: string
  run_type: string
  status: string
  duration_s: number
  started_at: string
  finished_at?: string
  linear_url?: string
}

export interface CostBucket {
  date: string
  cost_usd: number
  total_tokens: number
}

export interface CostResponse {
  buckets: CostBucket[]
}

export interface SpendEntry {
  agent: string
  total_tokens: number
  cost_usd: number
}

export interface SweepEntry {
  repo: string
  task: string
  description: string
  cooldown_h: number
  last_run_at?: string
  cooldown_left_h: number
  eligible: boolean
}

export interface PREntry {
  pr_url: string
  ticket_id?: string
  iterations: number
  max_iterations: number
  capped: boolean
  last_ci_sha?: string
  last_ci_run_url?: string
  last_reasoning?: string
  linear_url?: string
}
