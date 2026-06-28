import type { SpendEntry } from '../types'
import { fmtTokens, fmtMoney, monoAttr } from '../format'
import { agentInfo } from '../lookups'

interface Props {
  spend: SpendEntry[]
}

export function Spend({ spend }: Props) {
  return (
    <section class="panel chart" style={{ flex: '1 1 300px' }}>
      <div class="phead">
        <span class="ptitle">Spend by agent</span>
        <span class="kicker right">today · est.</span>
      </div>
      <div>{!spend.length ? <div class="empty">No spend recorded today.</div> : <Chart spend={spend} />}</div>
    </section>
  )
}

function Chart({ spend }: { spend: SpendEntry[] }) {
  const rows = spend.slice().sort((a, b) => b.total_tokens - a.total_tokens)
  const max = Math.max.apply(null, rows.map((r) => r.total_tokens).concat([1]))
  const barMax = 300,
    x0 = 116
  const h = Math.max(60, 22 + rows.length * 46)

  return (
    <svg viewBox={'0 0 510 ' + h}>
      {rows.map((a, i) => {
        const ai = agentInfo(a.agent)
        const y = 22 + i * 46
        const w = (a.total_tokens / max) * barMax
        return (
          <g key={a.agent}>
            <title>{`${ai.name}: ${fmtTokens(a.total_tokens)} tokens · ${fmtMoney(a.cost_usd)} today (est.)`}</title>
            <text x="0" y={y} dominant-baseline="hanging" font-family={monoAttr} font-size="12" fill="#C7CEDF">
              {ai.name}
            </text>
            <rect x={x0} y={y} width={barMax} height="14" rx="7" fill="#1C2433" />
            <rect x={x0} y={y} width={w.toFixed(1)} height="14" rx="7" fill={ai.color} />
            <text x="424" y={y} dominant-baseline="hanging" font-family={monoAttr} font-size="11" fill="#A6AFC4">
              {fmtTokens(a.total_tokens)}
            </text>
            <text x="424" y={y} dy="15" dominant-baseline="hanging" font-family={monoAttr} font-size="11" fill="#F4B860">
              {fmtMoney(a.cost_usd)}
            </text>
          </g>
        )
      })}
    </svg>
  )
}
