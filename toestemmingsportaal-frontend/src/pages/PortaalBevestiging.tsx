import { useEffect, useMemo, useState } from 'react'
import { useNavigate } from 'react-router-dom'
import MijnOverheidLayout from '../components/MijnOverheidLayout'
import { useRedirectContext, clearRedirectContext } from '../hooks/useRedirectContext'
import { usePortalToken } from '../hooks/usePortalToken'

const AUTO_REDIRECT_MS = 5000

type StoredConsent = {
  consent_id: string
  pseudonym: string
  pi: string
  scopes: string[]
  partial: boolean
}

function readLastConsent(): StoredConsent | null {
  const raw = sessionStorage.getItem('gbo.last_consent')
  if (!raw) return null
  try {
    return JSON.parse(raw) as StoredConsent
  } catch {
    return null
  }
}

export default function PortaalBevestiging() {
  const ctx = useRedirectContext()
  const { clear } = usePortalToken()
  const navigate = useNavigate()
  const consent = useMemo(readLastConsent, [])
  const [secondsLeft, setSecondsLeft] = useState(Math.ceil(AUTO_REDIRECT_MS / 1000))

  useEffect(() => {
    if (!ctx || !consent) return
    const tick = setInterval(() => setSecondsLeft((s) => (s > 0 ? s - 1 : 0)), 1000)
    const timer = setTimeout(() => doRedirect(), AUTO_REDIRECT_MS)
    return () => {
      clearInterval(tick)
      clearTimeout(timer)
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [ctx, consent])

  if (!ctx || !consent) {
    return (
      <MijnOverheidLayout activeNav="toestemmingen" breadcrumb={[{ label: 'Home' }]}>
        <h1 className="page-title">Geen recente toestemming</h1>
        <p>Er is geen consent-record gevonden in deze sessie.</p>
        <button className="btn btn-secondary" onClick={() => navigate('/auth')}>
          Opnieuw beginnen
        </button>
      </MijnOverheidLayout>
    )
  }

  const doRedirect = () => {
    const url = new URL(ctx.return_url)
    url.searchParams.set('status', 'ok')
    url.searchParams.set('consent_id', consent.consent_id)
    clearRedirectContext()
    clear()
    sessionStorage.removeItem('gbo.last_consent')
    window.location.assign(url.toString())
  }

  return (
    <MijnOverheidLayout
      activeNav="toestemmingen"
      breadcrumb={[
        { label: 'Home', href: '#' },
        { label: 'Toestemmingen', href: '#' },
        { label: 'Bevestiging' },
      ]}
    >
      <h1 className="page-title">Toestemming geregistreerd</h1>
      <p>
        U gaf toestemming aan <strong>{ctx.client_name}</strong>. Hieronder vindt u de details.
      </p>

      <div className="gov-card-inset bevestiging-detail">
        <dl style={{ margin: 0 }}>
          <dt>Referentie</dt>
          <dd className="mono">{consent.consent_id}</dd>
        </dl>
      </div>

      <p>
        U gaat over <strong>{secondsLeft}</strong> seconden automatisch terug naar {ctx.client_name}.
      </p>

      <div className="actions">
        <button className="btn btn-primary" onClick={doRedirect}>
          Direct terug naar {ctx.client_name} →
        </button>
      </div>
    </MijnOverheidLayout>
  )
}
