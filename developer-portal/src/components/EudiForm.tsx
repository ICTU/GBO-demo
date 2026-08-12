import type { Citizen, EudiPayload } from '../types'
import type { IssuanceOffer } from '../eudi'

type Props = {
  payload: EudiPayload
  setPayload: (p: EudiPayload) => void
  citizens: Citizen[]
  offers: IssuanceOffer[]
  offersError: string
}

export default function EudiForm({ payload, setPayload, citizens, offers, offersError }: Props) {
  const knownBsns = citizens.map((c) => c.bsn)
  return (
    <>
      <div className="field">
        <label htmlFor="usecase">Usecase</label>
        <select
          id="usecase"
          className="sel mono"
          value={payload.usecase}
          onChange={(e) => setPayload({ ...payload, usecase: e.target.value })}
        >
          {offers.map((offer) => (
            <option key={offer.key} value={offer.key}>{offer.label}</option>
          ))}
        </select>
        {offersError && <div className="hint">{offersError}</div>}
      </div>

      <div className="hint" style={{ fontSize: 12, color: 'var(--mute)', marginTop: 8 }}>
        <div>
          BSN komt uit de <b>wallet-PID-disclosure</b> — niet uit dit portaal.
          Gebruik in je wallet een BSN die de bron kent, anders krijg je aan
          het eind een <code className="mono">404</code> van de adapter.
        </div>
        {knownBsns.length > 0 && (
          <div className="mono" style={{ fontSize: 11, marginTop: 4 }}>
            Bekende BSN&#39;s (graphql-server mockdata):{' '}
            {knownBsns.join(', ')}
          </div>
        )}
      </div>

      <div className="hint" style={{ fontSize: 12, color: 'var(--mute)', marginTop: 8 }}>
        De <b>bron</b> publiceert query, parameters, concrete aanbiedingen,
        mapping en Type Metadata. Onboarding genereert hieruit zowel de
        issuance-serverconfiguratie als deze keuzelijst; de PDP blijft de
        daadwerkelijk gevraagde velden en parameterwaarden autoriseren.
      </div>

      <div className="hint" style={{ fontSize: 12, color: 'var(--mute)', marginTop: 8 }}>
        <b>Akte van overlijden</b> leest uit de tweede bron (BRP,{' '}
        <code className="mono">brp-graphql-server</code>) en loopt van de
        bron-eigen attestation-view <code className="mono">akteVanOverlijden</code>.
        De selectie van de overleden partner gebeurt dus in de bron. In de mock
        voldoet alleen BSN{' '}
        <code className="mono">999991772</code> (Frouke Jansen) daaraan; andere
        BSN&#39;s geven een 404 zonder dat er policy aan te pas komt.
      </div>
    </>
  )
}
