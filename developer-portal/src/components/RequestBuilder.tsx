import IssuanceForm from './IssuanceForm'
import UseForm from './UseForm'
import EudiForm from './EudiForm'
import HttpPreview from './HttpPreview'
import { curlFor } from '../util/curl'
import type {
  Citizen, EudiPayload, HistoryRun, IssuancePayload, Organization, Tab, UsePayload,
} from '../types'
import type { IssuanceOffer } from '../eudi'

type Props = {
  tab: Tab
  setTab: (t: Tab) => void
  issuancePayload: IssuancePayload
  setIssuancePayload: (p: IssuancePayload) => void
  usePayload: UsePayload
  setUsePayload: (p: UsePayload) => void
  eudiPayload: EudiPayload
  setEudiPayload: (p: EudiPayload) => void
  citizens: Citizen[]
  organizations: Organization[]
  history: HistoryRun[]
  onSubmit: () => void
  onSaveAsScenario: () => void
  submitting: boolean
  eudiOffers: IssuanceOffer[]
  eudiOffersError: string
}

export default function RequestBuilder({
  tab, setTab, issuancePayload, setIssuancePayload, usePayload, setUsePayload,
  eudiPayload, setEudiPayload,
  citizens, organizations, history, onSubmit, onSaveAsScenario, submitting,
  eudiOffers, eudiOffersError,
}: Props) {
  const endpoint =
    tab === 'issuance'
      ? 'POST consent-portal-backend /portal/consents'
    : tab === 'use'
      ? 'POST dienstverlener-backend /api/dvtp/query'
    : tab === 'asi-verify'
      ? 'QTSP → POST asi-provider /<dataservice>/authsrc-api/verify'
      : 'QR-scan → wallet → eudi-issuance-server → eudi-adapter'

  const canSubmit =
    tab === 'issuance'
      ? !!issuancePayload.citizen_bsn && !!issuancePayload.dienstverlener_oin && issuancePayload.scopes.length > 0
    : tab === 'use'
      ? !!usePayload.consent_id && !!usePayload.consent_token
    : tab === 'asi-verify'
      ? true
      : !!eudiPayload.usecase

  const curlText =
    tab === 'eudi-issuance'
      ? '# EUDI-flow start via wallet-QR — geen curl-equivalent'
    : tab === 'asi-verify'
      ? '# ASI-flow start bij een QTSP (registratie, keuzepagina, DPoP-token) — de QTSP-mock volgt'
      : curlFor(tab, tab === 'issuance' ? issuancePayload : usePayload)

  const submitLabel =
    tab === 'eudi-issuance'
      ? (submitting ? 'Wacht op wallet…' : 'Open QR-page + start')
    : tab === 'asi-verify'
      ? 'Meekijken'
      : (submitting ? 'Bezig…' : 'Verzenden')

  return (
    <div className="panel">
      <div className="panel-h" style={{ padding: 0, display: 'block' }}>
        <div className="tabs">
          <div className="seg">
            <button className={`seg-btn${tab === 'issuance' ? ' on' : ''}`} onClick={() => setTab('issuance')}>
              Issuance
            </button>
            <button className={`seg-btn${tab === 'use' ? ' on' : ''}`} onClick={() => setTab('use')}>
              Use · query
            </button>
            <button className={`seg-btn${tab === 'eudi-issuance' ? ' on' : ''}`} onClick={() => setTab('eudi-issuance')}>
              EUDI
            </button>
            <button className={`seg-btn${tab === 'asi-verify' ? ' on' : ''}`} onClick={() => setTab('asi-verify')}>
              ASI
            </button>
          </div>
          <span style={{ marginLeft: 'auto', fontSize: 11, fontFamily: 'var(--mono)', color: 'var(--mute)' }}>
            {endpoint}
          </span>
        </div>
      </div>
      <div className="panel-b">
        {tab === 'issuance' && (
          <IssuanceForm
            payload={issuancePayload}
            setPayload={setIssuancePayload}
            citizens={citizens}
            organizations={organizations}
          />
        )}
        {tab === 'use' && (
          <UseForm payload={usePayload} setPayload={setUsePayload} history={history} />
        )}
        {tab === 'eudi-issuance' && (
          <EudiForm
            payload={eudiPayload}
            setPayload={setEudiPayload}
            citizens={citizens}
            offers={eudiOffers}
            offersError={eudiOffersError}
          />
        )}

        {tab === 'asi-verify' && (
          <p style={{ fontSize: 13, lineHeight: 1.5, margin: '0 0 12px' }}>
            Een gekwalificeerde trust service provider (QTSP) laat attributen van een burger
            verifiëren bij een dataservice van de ASIP (ETSI TS 119 478). De QTSP zit buiten
            de demo; het portal start het verzoek dus niet zelf. Met <em>Meekijken</em> pikt
            het de eerstvolgende <code>verify</code> of <code>retrieve</code> op, en toont de
            keten, het FSC-transactielog en de PDP-beslissing.
          </p>
        )}

        <HttpPreview text={curlText} />

        <div className="btnrow">
          <button className="btn pri" onClick={onSubmit} disabled={!canSubmit || submitting}>
            {submitLabel}
          </button>
          <button className="btn" onClick={onSaveAsScenario} disabled={submitting || tab === 'asi-verify'}>
            Opslaan als scenario
          </button>
        </div>
      </div>
    </div>
  )
}
