import { useEffect, useMemo, useState } from 'react'
import { useNavigate, useParams } from 'react-router-dom'
import MijnOverheidLayout from '../components/MijnOverheidLayout'
import StatusChip from '../components/consent-detail/StatusChip'
import DetailGrid from '../components/consent-detail/DetailGrid'
import ScopeVeldenList from '../components/consent-detail/ScopeVeldenList'
import HistoryTimeline, { HistoryEvent } from '../components/consent-detail/HistoryTimeline'
import RevokeModal from '../components/consent-detail/RevokeModal'
import { useConsents } from '../hooks/useConsents'
import { usePortalToken } from '../hooks/usePortalToken'
import { revokeConsent } from '../api/portalClient'
import {
  deriveAfnemerName,
  deriveOnderwerp,
  deriveDoel,
  formatGeldigTot,
} from '../data/onderwerpMap'

export default function MijnToestemmingenDetail() {
  const { id = '' } = useParams()
  const { token } = usePortalToken()
  const navigate = useNavigate()
  const { consents, loading, error, refresh } = useConsents()

  const [modalOpen, setModalOpen] = useState(false)
  const [revoking, setRevoking] = useState(false)
  const [revokeError, setRevokeError] = useState<string | null>(null)

  useEffect(() => {
    if (!token) {
      navigate(
        `/auth?next=${encodeURIComponent(`/mijnoverheid/toestemmingen/${id}`)}`,
      )
    }
  }, [token, navigate, id])

  const consent = useMemo(() => consents?.find((c) => c.consent_id === id), [consents, id])

  const onRevoke = async () => {
    if (!token || !consent) return
    setRevokeError(null)
    setRevoking(true)
    try {
      await revokeConsent(token, consent.consent_id)
      await refresh()
      navigate(`/mijnoverheid/toestemmingen/${id}/ingetrokken`)
    } catch (err) {
      setRevokeError((err as Error).message)
    } finally {
      setRevoking(false)
    }
  }

  const afnemerName = consent ? deriveAfnemerName(consent.dienstverlener_oin) : '—'

  const history: HistoryEvent[] = useMemo(() => {
    if (!consent) return []
    const ev: HistoryEvent[] = [
      { label: 'Uitgegeven', at: consent.created_at, type: 'uitgegeven' },
    ]
    if (consent.effective_status === 'revoked') {
      ev.push({ label: 'Ingetrokken', at: consent.valid_until, type: 'ingetrokken' })
    } else if (consent.effective_status === 'expired') {
      ev.push({ label: 'Verlopen', at: consent.valid_until, type: 'verlopen' })
    }
    return ev
  }, [consent])

  return (
    <MijnOverheidLayout
      activeNav="toestemmingen"
      breadcrumb={[
        { label: 'Home', href: '#' },
        { label: 'Toestemmingen', href: '/mijnoverheid/toestemmingen' },
        { label: afnemerName },
      ]}
    >
      {loading && <div className="consent-empty">Laden…</div>}
      {error && (
        <div className="content-card">
          <h1 className="page-title">Niet gevonden</h1>
          <p>Kon toestemmingen niet ophalen: {error}</p>
        </div>
      )}
      {!loading && !error && !consent && (
        <div className="content-card">
          <h1 className="page-title">Toestemming niet gevonden</h1>
          <p>Deze toestemming bestaat niet of hoort niet bij uw account.</p>
          <a className="btn-link-inline" href="/mijnoverheid/toestemmingen">
            ← Terug naar overzicht
          </a>
        </div>
      )}
      {consent && (
        <>
          <div className="detail-head">
            <h1 className="page-title detail-title">{afnemerName}</h1>
            <StatusChip status={consent.effective_status} />
          </div>

          <div className="content-card">
            <DetailGrid
              items={[
                { label: 'Toestemmings-ID', value: <code className="mono">{consent.consent_id}</code> },
                { label: 'Onderwerp', value: deriveOnderwerp(consent.scopes) },
                { label: 'Doel', value: deriveDoel(consent.use_case) },
                { label: 'Integrator', value: '—' },
                { label: 'Geldig tot', value: formatGeldigTot(consent.valid_until) },
              ]}
            />
          </div>

          <div className="content-card">
            <h2 className="content-card-h2">Gedeelde gegevens</h2>
            <ScopeVeldenList scopes={consent.scopes} />
          </div>

          <div className="content-card">
            <h2 className="content-card-h2">Geschiedenis</h2>
            <HistoryTimeline events={history} />
          </div>

          {revokeError && (
            <div className="consent-error">Intrekken mislukt: {revokeError}</div>
          )}

          <div className="detail-actions">
            {consent.effective_status === 'active' && (
              <button className="btn btn-deny" onClick={() => setModalOpen(true)}>
                Toestemming intrekken
              </button>
            )}
            <a className="btn-link-inline" href="/mijnoverheid/toestemmingen">
              ← Terug naar overzicht
            </a>
          </div>

          {modalOpen && (
            <RevokeModal
              afnemer={afnemerName}
              onCancel={() => setModalOpen(false)}
              onConfirm={onRevoke}
              busy={revoking}
            />
          )}
        </>
      )}
    </MijnOverheidLayout>
  )
}
