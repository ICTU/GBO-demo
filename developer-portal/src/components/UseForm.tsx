import { useMemo } from 'react'
import type { HistoryRun, IssuancePayload, UsePayload } from '../types'

type Props = {
  payload: UsePayload
  setPayload: (p: UsePayload) => void
  history: HistoryRun[]
}

type IssuedConsent = { consent_id: string; label: string }

const DEFAULT_FIELDS = [
  'belastingjaar', 'verzamelinkomen', 'box1Inkomen',
  'status', 'indieningsdatum',
]

// The backend generates the GraphQL-query itself based on `fields`
// (buildQuery in dienstverlener-backend/main.go): Bedrag-fields are wrapped
// in an `... on AangifteIH` fragment. The query always contains
// `$bsn: BSN!` — the PI gets filled in by the backend after consent-lookup
// and travels to HV-Outway as `variables.bsn`. The sidecar at the source
// resolves PI→BSN (subject_id_type=pseudonym in the grant-property).
// The BD bron-schema has no belastingjaren argument; the backend filters
// the requested years from the response.
function previewQuery(fields: string[]): string {
  const bedrag = fields.filter((f) => ['verzamelinkomen', 'box1Inkomen', 'box2Inkomen', 'box3Inkomen'].includes(f))
  const plain = fields.filter((f) => !bedrag.includes(f))
  const selection =
    plain.join('\n      ') +
    (bedrag.length
      ? `\n      ... on AangifteIH {\n        ${bedrag.map((f) => `${f} { waarde valuta }`).join('\n        ')}\n      }`
      : '')
  return `query($bsn: BSN!) {\n  ingeschrevenPersoon(bsn: $bsn) {\n    heeftBelastingjaarAangifte {\n      ${selection}\n    }\n  }\n}`
}

export default function UseForm({ payload, setPayload, history }: Props) {
  const issuedConsents = useMemo<IssuedConsent[]>(() => {
    const seen = new Set<string>()
    const out: IssuedConsent[] = []
    for (const h of history) {
      if (h.tab !== 'issuance' || h.outcome !== 'allow' || !h.consent_id) continue
      if (seen.has(h.consent_id)) continue
      seen.add(h.consent_id)
      const p = h.payload as IssuancePayload
      const bsn = p.citizen_bsn || '???'
      const scopes = (p.scopes ?? []).join('+') || '(geen scopes)'
      const time = new Date(h.ts).toLocaleTimeString('nl-NL', { hour: '2-digit', minute: '2-digit' })
      out.push({ consent_id: h.consent_id, label: `${bsn} · ${scopes} · ${time}` })
    }
    return out
  }, [history])

  const jaren = payload.belastingjaren ?? [2025]
  const fields = payload.fields ?? DEFAULT_FIELDS
  const preview = previewQuery(fields)

  return (
    <>
      <div className="field">
        <label htmlFor="cid">Consent-ID <span className="opt">(uit eerder issuance-scenario)</span></label>
        <div className="row2">
          <select
            id="cid"
            className="sel"
            value={payload.consent_id}
            onChange={(e) => setPayload({ ...payload, consent_id: e.target.value })}
          >
            <option value="">— kies uit eerdere issuance —</option>
            {issuedConsents.map(({ consent_id, label }) => (
              <option key={consent_id} value={consent_id}>{label}</option>
            ))}
          </select>
          <input
            className="inp mono"
            placeholder="of plak hier"
            value={payload.consent_id}
            onChange={(e) => setPayload({ ...payload, consent_id: e.target.value })}
          />
        </div>
      </div>

      <div className="field">
        <label htmlFor="scope">Scope-ID <span className="opt">(geldig in token-claim; PDP checkt scope ⊆ consent.granted_scopes)</span></label>
        <input
          id="scope"
          className="inp mono"
          value={payload.scope_id ?? ''}
          onChange={(e) => setPayload({ ...payload, scope_id: e.target.value })}
          placeholder="bd:ib:2025"
        />
      </div>

      <div className="field">
        <label htmlFor="jaren">Belastingjaren <span className="opt">(comma-separated; bron levert alle jaren, backend filtert)</span></label>
        <input
          id="jaren"
          className="inp mono"
          value={jaren.join(', ')}
          onChange={(e) => {
            const parsed = e.target.value
              .split(',')
              .map((s) => parseInt(s.trim(), 10))
              .filter((n) => !Number.isNaN(n))
            setPayload({ ...payload, belastingjaren: parsed })
          }}
        />
      </div>

      <div className="field">
        <label>
          GraphQL-query <span className="opt">(auto-generated; PI wordt in `variables.bsn` gezet door dienstverlener-backend na consent-lookup)</span>
        </label>
        <div className="ed-wrap">
          <div className="ed-bar">
            <span style={{ display: 'inline-flex', gap: 5 }}>
              <span style={{ width: 9, height: 9, borderRadius: '50%', background: '#ff5f57', display: 'inline-block' }} />
              <span style={{ width: 9, height: 9, borderRadius: '50%', background: '#febc2e', display: 'inline-block' }} />
              <span style={{ width: 9, height: 9, borderRadius: '50%', background: '#28c840', display: 'inline-block' }} />
            </span>
            <span>query.graphql (preview)</span>
          </div>
          <pre className="ed-ta" style={{ margin: 0, whiteSpace: 'pre-wrap' }}>{preview}</pre>
        </div>
      </div>
    </>
  )
}
