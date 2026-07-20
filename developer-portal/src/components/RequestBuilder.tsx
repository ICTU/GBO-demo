import IssuanceForm from './IssuanceForm'
import UseForm from './UseForm'
import EudiForm from './EudiForm'
import HttpPreview from './HttpPreview'
import { curlFor } from '../util/curl'
import type {
  Citizen, EudiPayload, HistoryRun, IssuancePayload, Organization, Tab, UsePayload,
} from '../types'

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
}

export default function RequestBuilder({
  tab, setTab, issuancePayload, setIssuancePayload, usePayload, setUsePayload,
  eudiPayload, setEudiPayload,
  citizens, organizations, history, onSubmit, onSaveAsScenario, submitting,
}: Props) {
  const endpoint =
    tab === 'issuance'
      ? 'POST consent-portal-backend /portal/consents'
    : tab === 'use'
      ? 'POST dienstverlener-backend /api/dvtp/query'
      : 'QR-scan → wallet → eudi-issuance-server → eudi-adapter'

  const canSubmit =
    tab === 'issuance'
      ? !!issuancePayload.citizen_bsn && !!issuancePayload.dienstverlener_oin && issuancePayload.scopes.length > 0
    : tab === 'use'
      ? !!usePayload.consent_id
      : !!eudiPayload.usecase

  const curlText =
    tab === 'eudi-issuance'
      ? '# EUDI-flow start via wallet-QR — geen curl-equivalent'
      : curlFor(tab, tab === 'issuance' ? issuancePayload : usePayload)

  const submitLabel =
    tab === 'eudi-issuance'
      ? (submitting ? 'Wacht op wallet…' : 'Open QR-page + start')
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
          <EudiForm payload={eudiPayload} setPayload={setEudiPayload} citizens={citizens} />
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
