import IssuanceForm from './IssuanceForm'
import UseForm from './UseForm'
import EudiForm from './EudiForm'
import AsiForm from './AsiForm'
import HttpPreview from './HttpPreview'
import { curlFor } from '../util/curl'
import type {
  AsiPayload, Citizen, EudiPayload, HistoryRun, IssuancePayload, Organization, Tab, UsePayload,
} from '../types'
import type { IssuanceOffer } from '../eudi'
import type { AsiAttribute } from '../lib/asiRequest'

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
  asiPayload: AsiPayload
  setAsiPayload: (p: AsiPayload) => void
  asiAttributes: AsiAttribute[]
  asiAttributesError: string
}

export default function RequestBuilder({
  tab, setTab, issuancePayload, setIssuancePayload, usePayload, setUsePayload,
  eudiPayload, setEudiPayload,
  citizens, organizations, history, onSubmit, onSaveAsScenario, submitting,
  eudiOffers, eudiOffersError, asiPayload, setAsiPayload, asiAttributes, asiAttributesError,
}: Props) {
  const endpoint =
    tab === 'issuance'
      ? 'POST consent-portal-backend /portal/consents'
    : tab === 'use'
      ? 'POST dienstverlener-backend /api/dvtp/query'
    : tab === 'asi-verify'
      ? `referentie-QTSP → POST asi-provider /<dataservice>/authsrc-api/${asiPayload.operation}`
      : 'QR-scan → wallet → eudi-issuance-server → eudi-adapter'

  const canSubmit =
    tab === 'issuance'
      ? !!issuancePayload.citizen_bsn && !!issuancePayload.dienstverlener_oin && issuancePayload.scopes.length > 0
    : tab === 'use'
      ? !!usePayload.consent_id && !!usePayload.consent_token
    : tab === 'asi-verify'
      ? asiPayload.attributes.length > 0
      : !!eudiPayload.usecase

  const curlText =
    tab === 'eudi-issuance'
      ? '# EUDI-flow start via wallet-QR — geen curl-equivalent'
    : tab === 'asi-verify'
      ? '# ASI-flow: de referentie-QTSP doet discovery, registratie, de doorverwijzing naar het inloggen met DigiD (mock), het DPoP-token en de aanroep'
      : curlFor(tab, tab === 'issuance' ? issuancePayload : usePayload)

  const submitLabel =
    tab === 'eudi-issuance'
      ? (submitting ? 'Wacht op wallet…' : 'Open QR-page + start')
    : tab === 'asi-verify'
      ? (submitting ? 'Wacht op de burger…' : 'Aanvragen')
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
          <AsiForm
            payload={asiPayload}
            setPayload={setAsiPayload}
            attributes={asiAttributes}
            attributesError={asiAttributesError}
          />
        )}

        <HttpPreview text={curlText} />

        <div className="btnrow">
          <button className="btn pri" onClick={onSubmit} disabled={!canSubmit || submitting}>
            {submitLabel}
          </button>
          <button className="btn" onClick={onSaveAsScenario} disabled={submitting}>
            Opslaan als scenario
          </button>
        </div>
      </div>
    </div>
  )
}
