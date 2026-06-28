import type { CostResponse } from '../types'
import { niceMax, monoAttr } from '../format'

interface Props {
  cost: CostResponse
}

export function TokenCost({ cost }: Props) {
  const bks = cost.buckets.slice(-14)

  return (
    <section class="panel chart" style={{ flex: '2 1 460px' }}>
      <div class="phead">
        <span class="ptitle">Token &amp; cost</span>
        <span class="kicker">last 14 days</span>
        <span class="legend-inline">
          <span style={{ color: 'var(--blue)' }}>
            <span class="legend-bar" style={{ background: 'var(--blue)' }} />
            tokens
          </span>
          <span style={{ color: 'var(--lamp)' }}>
            <span class="legend-bar" style={{ background: 'var(--lamp)' }} />
            cost
          </span>
        </span>
      </div>
      <div>{bks.length === 0 ? <div class="empty">No usage recorded yet.</div> : <Chart bks={bks} />}</div>
    </section>
  )
}

function Chart({ bks }: { bks: CostResponse['buckets'] }) {
  const tok = bks.map((b) => (b.total_tokens || 0) / 1e6)
  const dol = bks.map((b) => b.cost_usd || 0)
  const n = bks.length
  const padL = 38,
    padR = 564,
    topY = 26,
    baseY = 184
  const maxTok = niceMax(Math.max.apply(null, tok.concat([0.0001])))
  const maxCost = niceMax(Math.max.apply(null, dol.concat([0.0001])))
  const TX = (i: number) => (n === 1 ? (padL + padR) / 2 : padL + i * ((padR - padL) / (n - 1)))
  const YT = (v: number) => baseY - (v / maxTok) * (baseY - topY)
  const YC = (v: number) => baseY - (v / maxCost) * (baseY - topY)

  let area = 'M ' + TX(0).toFixed(1) + ' ' + baseY
  tok.forEach((v, i) => {
    area += ' L ' + TX(i).toFixed(1) + ' ' + YT(v).toFixed(1)
  })
  area += ' L ' + TX(n - 1).toFixed(1) + ' ' + baseY + ' Z'

  let line = 'M ' + TX(0).toFixed(1) + ' ' + YT(tok[0]).toFixed(1)
  tok.forEach((v, i) => {
    if (i) line += ' L ' + TX(i).toFixed(1) + ' ' + YT(v).toFixed(1)
  })

  const costPts = dol.map((v, i) => TX(i).toFixed(1) + ',' + YC(v).toFixed(1)).join(' ')

  const grid = []
  for (let k = 1; k <= 4; k++) {
    const val = (maxTok * k) / 5
    const y = YT(val).toFixed(1)
    const lbl =
      val >= 1 ? (val % 1 === 0 ? val + 'M' : val.toFixed(1) + 'M') : (val * 1000).toFixed(0) + 'K'
    grid.push(
      <>
        <line x1="38" y1={y} x2="564" y2={y} stroke="#1C2433" stroke-width="1" />
        <text x="30" y={y} text-anchor="end" dominant-baseline="middle" font-family={monoAttr} font-size="9" fill="#5E677D">
          {lbl}
        </text>
      </>,
    )
  }

  return (
    <svg viewBox="0 0 600 210">
      <defs>
        <linearGradient id="tokFill" x1="0" y1="0" x2="0" y2="1">
          <stop offset="0%" stop-color="#8FB4FF" stop-opacity="0.30" />
          <stop offset="100%" stop-color="#8FB4FF" stop-opacity="0" />
        </linearGradient>
      </defs>
      {grid}
      <path d={area} fill="url(#tokFill)" />
      <path d={line} fill="none" stroke="#8FB4FF" stroke-width="2" stroke-linejoin="round" stroke-linecap="round" />
      <polyline
        points={costPts}
        fill="none"
        stroke="#F4B860"
        stroke-width="2"
        stroke-linejoin="round"
        stroke-linecap="round"
        stroke-dasharray="1 5"
      />
      <circle cx={TX(n - 1).toFixed(1)} cy={YC(dol[n - 1]).toFixed(1)} r="3.5" fill="#0B0E16" stroke="#F4B860" stroke-width="2" />
      <text x="38" y="202" font-family={monoAttr} font-size="9" fill="#5E677D">
        {n - 1 + 'd ago'}
      </text>
      <text x="564" y="202" text-anchor="end" font-family={monoAttr} font-size="9" fill="#5E677D">
        today
      </text>
    </svg>
  )
}
