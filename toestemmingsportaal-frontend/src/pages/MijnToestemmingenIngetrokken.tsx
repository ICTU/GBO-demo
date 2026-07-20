import { useMemo } from 'react'
import { Link, useParams } from 'react-router-dom'
import MijnOverheidLayout from '../components/MijnOverheidLayout'
import { useConsents } from '../hooks/useConsents'
import { deriveAfnemerName } from '../data/onderwerpMap'

const CheckCircle = () => (
  <svg width="22" height="22" viewBox="0 0 24 24" fill="none" stroke="#fff" strokeWidth="2.5">
    <path d="M5 12l4 4L19 7" />
  </svg>
)

export default function MijnToestemmingenIngetrokken() {
  const { id = '' } = useParams()
  const { consents } = useConsents()
  const consent = useMemo(() => consents?.find((c) => c.consent_id === id), [consents, id])
  const afnemerName = consent ? deriveAfnemerName(consent.dienstverlener_oin) : 'de organisatie'

  return (
    <MijnOverheidLayout
      activeNav="toestemmingen"
      breadcrumb={[
        { label: 'Home', href: '#' },
        { label: 'Toestemmingen', href: '/mijnoverheid/toestemmingen' },
        { label: 'Ingetrokken' },
      ]}
    >
      <div className="content-card revoke-confirm">
        <div className="revoke-confirm-icon">
          <CheckCircle />
        </div>
        <h1 className="page-title">Toestemming ingetrokken</h1>
        <p>
          <strong>{afnemerName}</strong> kan deze gegevens niet meer ophalen. Het
          toestemmingsregister is direct bijgewerkt; bij een volgende aanvraag krijgt {afnemerName}{' '}
          een weigering.
        </p>
        <p>
          De toestemming staat nu in het overzicht onder <strong>Verlopen</strong>.
        </p>
        <div className="detail-actions">
          <Link to="/mijnoverheid/toestemmingen?tab=verlopen" className="btn btn-primary">
            Terug naar overzicht
          </Link>
        </div>
      </div>
    </MijnOverheidLayout>
  )
}
