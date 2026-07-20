import { useMemo, useState } from 'react'
import type { Scenario, Tab } from '../types'

type Props = {
  scenarios: Scenario[]
  loading: boolean
  error: string | null
  onLoad: (s: Scenario) => void
  onDelete: (id: string) => void
}

type Filter = 'all' | 'dvtp' | 'eudi'

const DVTP_TABS = new Set<Tab>(['issuance', 'use'])
const EUDI_TABS = new Set<Tab>(['eudi-issuance'])

function inFilter(s: Scenario, f: Filter): boolean {
  if (f === 'all') return true
  if (f === 'dvtp') return DVTP_TABS.has(s.tab)
  return EUDI_TABS.has(s.tab)
}

export default function ScenarioLibrary({ scenarios, loading, error, onLoad, onDelete }: Props) {
  const [filter, setFilter] = useState<Filter>('all')

  const counts = useMemo(() => ({
    all: scenarios.length,
    dvtp: scenarios.filter((s) => DVTP_TABS.has(s.tab)).length,
    eudi: scenarios.filter((s) => EUDI_TABS.has(s.tab)).length,
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
            style={{ flex: 1 }}
          >
            Alles ({counts.all})
          </button>
          <button
            type="button"
            className={`seg-btn${filter === 'dvtp' ? ' on' : ''}`}
            onClick={() => setFilter('dvtp')}
            style={{ flex: 1 }}
          >
            DvTP ({counts.dvtp})
          </button>
          <button
            type="button"
            className={`seg-btn${filter === 'eudi' ? ' on' : ''}`}
            onClick={() => setFilter('eudi')}
            style={{ flex: 1 }}
          >
            EUDI ({counts.eudi})
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
