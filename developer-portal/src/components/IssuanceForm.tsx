import type { Citizen, IssuancePayload, Organization } from '../types'

type Props = {
  payload: IssuancePayload
  setPayload: (p: IssuancePayload) => void
  citizens: Citizen[]
  organizations: Organization[]
}

const SCOPE_OPTIONS = [
  { code: 'bd:ib:2025', label: 'Inkomstenbelasting 2025' },
  { code: 'bd:ib:2024', label: 'Inkomstenbelasting 2024' },
  { code: 'bd:ib:2023', label: 'Inkomstenbelasting 2023' },
]

export default function IssuanceForm({ payload, setPayload, citizens, organizations }: Props) {
  const toggleScope = (code: string) => {
    const next = payload.scopes.includes(code)
      ? payload.scopes.filter((s) => s !== code)
      : [...payload.scopes, code]
    setPayload({ ...payload, scopes: next })
  }
  return (
    <>
      <div className="field">
        <label htmlFor="bsn">Burger-BSN <span className="opt">(mock)</span></label>
        <select
          id="bsn"
          className="sel mono"
          value={payload.citizen_bsn}
          onChange={(e) => setPayload({ ...payload, citizen_bsn: e.target.value })}
        >
          <option value="">— kies —</option>
          {citizens.map((c) => (
            <option key={c.bsn} value={c.bsn}>{c.bsn}</option>
          ))}
        </select>
      </div>

      <div className="field">
        <label htmlFor="oin">Dienstverlener-OIN</label>
        <select
          id="oin"
          className="sel mono"
          value={payload.dienstverlener_oin}
          onChange={(e) => setPayload({ ...payload, dienstverlener_oin: e.target.value })}
        >
          <option value="">— kies —</option>
          {organizations.map((o) => (
            <option key={o.oin} value={o.oin}>
              {o.oin} · {o.name ?? o.oin}
            </option>
          ))}
        </select>
      </div>

      <div className="field">
        <label>Scope <span className="opt">(per scope-ID uit dienstencatalogus)</span></label>
        <div className="scope-grid">
          {SCOPE_OPTIONS.map((sc) => {
            const on = payload.scopes.includes(sc.code)
            return (
              <label key={sc.code} className={`scope-item${on ? ' on' : ''}`}>
                <input type="checkbox" checked={on} onChange={() => toggleScope(sc.code)} />
                <span style={{ flex: 1 }}>{sc.label}</span>
                <span className="mono" style={{ fontSize: 11, color: 'var(--mute)' }}>{sc.code}</span>
              </label>
            )
          })}
        </div>
      </div>

      <div className="row2">
        <div className="field">
          <label htmlFor="validity">Geldigheid <span className="opt">(seconden)</span></label>
          <input
            id="validity"
            className="inp mono"
            type="number"
            min={60}
            value={payload.validity_seconds ?? 7776000}
            onChange={(e) =>
              setPayload({ ...payload, validity_seconds: parseInt(e.target.value, 10) || 0 })
            }
          />
        </div>
        <div className="field">
          <label htmlFor="usecase">Doel / use-case</label>
          <input
            id="usecase"
            className="inp"
            value={payload.use_case ?? ''}
            onChange={(e) => setPayload({ ...payload, use_case: e.target.value })}
            placeholder="hypotheek"
          />
        </div>
      </div>
    </>
  )
}
