import type { AsiPayload } from '../types'
import type { AsiAttribute } from '../lib/asiRequest'

type Props = {
  payload: AsiPayload
  setPayload: (p: AsiPayload) => void
  attributes: AsiAttribute[]
  attributesError: string
}

// AsiForm is what the QTSP asks of the ASIP: verify or retrieve, and the
// attributes with the values the QTSP holds. The attributes come from the
// QTSP, so the portal offers what the QTSP's own form offers.
export default function AsiForm({ payload, setPayload, attributes, attributesError }: Props) {
  const chosen = (id: string) => payload.attributes.find((a) => a.identifier === id)
  const toggle = (a: AsiAttribute) => {
    setPayload({
      ...payload,
      attributes: chosen(a.identifier)
        ? payload.attributes.filter((x) => x.identifier !== a.identifier)
        : [...payload.attributes, { identifier: a.identifier, value: JSON.stringify(a.example) }],
    })
  }
  const setValue = (id: string, value: string) => {
    setPayload({ ...payload, attributes: payload.attributes.map((x) => (x.identifier === id ? { ...x, value } : x)) })
  }

  return (
    <>
      <div className="field">
        <label>Wat</label>
        <div className="seg">
          <button
            type="button"
            className={`seg-btn${payload.operation === 'verify' ? ' on' : ''}`}
            onClick={() => setPayload({ ...payload, operation: 'verify' })}
          >
            Verifiëren
          </button>
          <button
            type="button"
            className={`seg-btn${payload.operation === 'retrieve' ? ' on' : ''}`}
            onClick={() => setPayload({ ...payload, operation: 'retrieve' })}
          >
            Ophalen
          </button>
        </div>
      </div>

      <div className="field">
        <label>
          Attributen{' '}
          <span className="opt">
            {payload.operation === 'verify' ? '(met de waarde die de QTSP heeft, als JSON)' : '(de bron levert de waarde)'}
          </span>
        </label>
        {attributesError && <div className="hint">De QTSP is niet bereikbaar: {attributesError}</div>}
        <div className="scope-grid">
          {attributes.map((a) => {
            const c = chosen(a.identifier)
            return (
              <div key={a.identifier}>
                {/* .field label would make the row a block; keep it a row. */}
                <label className={`scope-item${c ? ' on' : ''}`} style={{ display: 'flex', marginBottom: 0 }}>
                  <input type="checkbox" checked={!!c} onChange={() => toggle(a)} />
                  <span style={{ flex: 1 }}>{a.label}</span>
                  <span className="mono" style={{ fontSize: 11, color: 'var(--mute)' }}>{a.identifier.replace(/^https?:\/\//, '')}</span>
                </label>
                {c && payload.operation === 'verify' && (
                  <textarea
                    className="inp mono"
                    rows={2}
                    aria-label={`Waarde van ${a.label}`}
                    value={c.value ?? ''}
                    onChange={(e) => setValue(a.identifier, e.target.value)}
                    style={{ width: '100%', marginTop: 4 }}
                  />
                )}
              </div>
            )
          })}
        </div>
      </div>

      <div className="hint" style={{ fontSize: 12, color: 'var(--mute)', marginTop: 8 }}>
        <em>Aanvragen</em> legt de aanvraag neer bij de referentie-QTSP en opent die in een nieuw
        tabblad. U logt eerst in bij de QTSP, met de NL Wallet of met testgegevens (mock)
        {payload.login ? <>, al ingevuld met <b>{payload.login.given_name} {payload.login.family_name}</b></> : null}.
        Daarna gaat u naar de autorisatieserver van de bron. Daar logt u in, wat het verzoek goedkeurt,
        of weigert u. Met DigiD (mock) vult u zelf het BSN in
        {payload.person ? <>, bijvoorbeeld van <b>{payload.person}</b></> : null}. Met de NL Wallet
        komt het BSN uit het persoonsidentificatiebewijs in uw wallet; het resultaat hangt er dan
        van af of de BRP (mock) die persoon kent. Het resultaat verschijnt hier, met de keten, het
        FSC-transactielog en de PDP-beslissing.
      </div>
    </>
  )
}
