import { useMemo, useState } from 'react'
import type { AsiExpected, Scenario, Tab } from '../types'
import { EXPECTED_LABEL } from '../lib/asiRequest'

type Props = {
  scenarios: Scenario[]
  loading: boolean
  error: string | null
  onLoad: (s: Scenario) => void
  onDelete: (id: string) => void
}

type Filter = 'all' | 'dvtp' | 'eudi' | 'asi'

const DVTP_TABS = new Set<Tab>(['issuance', 'use'])
const EUDI_TABS = new Set<Tab>(['eudi-issuance'])
const ASI_TABS = new Set<Tab>(['asi-verify'])

function inFilter(s: Scenario, f: Filter): boolean {
  if (f === 'all') return true
  if (f === 'dvtp') return DVTP_TABS.has(s.tab)
  if (f === 'asi') return ASI_TABS.has(s.tab)
  return EUDI_TABS.has(s.tab)
}

// Four filters share the column: narrower padding than a .seg-btn, and an
// equal share of the width each.
const FILTER_BUTTON = { flex: 1, minWidth: 0, padding: '6px 6px' } as const

// The chip of an ASI scenario: the outcome it expects. A match is green, a
// no-match red; the others are neither.
function asiChip(expected: AsiExpected) {
  const tone = expected === 'match' ? ' allow' : expected === 'no-match' ? ' deny' : ' tab'
  return <span className={`chip${tone}`}>{EXPECTED_LABEL[expected]}</span>
}

export default function ScenarioLibrary({ scenarios, loading, error, onLoad, onDelete }: Props) {
  const [filter, setFilter] = useState<Filter>('all')

  const counts = useMemo(() => ({
    all: scenarios.length,
    dvtp: scenarios.filter((s) => DVTP_TABS.has(s.tab)).length,
    eudi: scenarios.filter((s) => EUDI_TABS.has(s.tab)).length,
    asi: scenarios.filter((s) => ASI_TABS.has(s.tab)).length,
  }), [scenarios])

  const visible = useMemo(() => scenarios.filter((s) => inFilter(s, filter)), [scenarios, filter])

  return (
    <div className="panel">
      <div className="panel-h">
        <span className="t">
          <span className="n">3.1</span>Scenarios
        </span>
      </div>
      <div style={{ padding: '8px 12px 0', display: 'flex' }}>
        <div className="seg" style={{ width: '100%' }}>
          <button
            type="button"
            className={`seg-btn${filter === 'all' ? ' on' : ''}`}
            onClick={() => setFilter('all')}
            style={FILTER_BUTTON}
          >
            Alles ({counts.all})
          </button>
          <button
            type="button"
            className={`seg-btn${filter === 'dvtp' ? ' on' : ''}`}
            onClick={() => setFilter('dvtp')}
            style={FILTER_BUTTON}
          >
            DvTP ({counts.dvtp})
          </button>
          <button
            type="button"
            className={`seg-btn${filter === 'eudi' ? ' on' : ''}`}
            onClick={() => setFilter('eudi')}
            style={FILTER_BUTTON}
          >
            EUDI ({counts.eudi})
          </button>
          <button
            type="button"
            className={`seg-btn${filter === 'asi' ? ' on' : ''}`}
            onClick={() => setFilter('asi')}
            style={FILTER_BUTTON}
          >
            ASI ({counts.asi})
          </button>
        </div>
      </div>
      <div className="panel-b" style={{ maxHeight: 640, overflowY: 'auto' }}>
        {loading && <div className="empty">Laden…</div>}
        {error && <div className="empty">Fout: {error}</div>}
        {!loading && !error && visible.length === 0 && (
          <div className="empty">
            {counts.all === 0 ? 'Geen scenarios beschikbaar.' : 'Geen scenarios in dit filter.'}
          </div>
        )}
        {!loading && !error && visible.length > 0 && visible.map((s) => (
          <div key={s.id} className="scn">
            <div className="scn-top">
              <div className="scn-name">{s.name}</div>
              {s.expected_outcome === 'allow' && <span className="chip allow">ALLOW</span>}
              {s.expected_outcome === 'deny' && <span className="chip deny">DENY</span>}
              {s.tab === 'asi-verify' && s.expected_outcome && s.expected_outcome in EXPECTED_LABEL &&
                asiChip(s.expected_outcome as AsiExpected)}
            </div>
            <div className="scn-desc">{s.desc}</div>
            <div className="scn-acts">
              <span className="chip tab">{s.tab}</span>
              <button className="mini pri" type="button" onClick={() => onLoad(s)}>Laad</button>
              {s.user_saved && (
                <button className="mini danger" type="button" onClick={() => onDelete(s.id)}>
                  Verwijder
                </button>
              )}
            </div>
          </div>
        ))}
      </div>
    </div>
  )
}
