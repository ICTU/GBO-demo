import { useState } from 'react'
import { useNavigate, useSearchParams } from 'react-router-dom'
import DigiDLayout from '../components/DigiDLayout'
import { useRedirectContext, clearRedirectContext } from '../hooks/useRedirectContext'
import { usePortalToken } from '../hooks/usePortalToken'
import { login } from '../api/portalClient'

type Method = 'app' | 'sms' | 'rijbewijs' | 'id-kaart'

const ChevronRight = () => (
  <svg width="18" height="18" viewBox="0 0 24 24" fill="none" stroke="#E17000" strokeWidth="2.5">
    <path d="M9 6l6 6-6 6" />
  </svg>
)

const ChevronLeft = () => (
  <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2.5">
    <path d="M15 6l-6 6 6 6" />
  </svg>
)

const PhoneIcon = () => (
  <svg width="32" height="32" viewBox="0 0 48 48" fill="none" aria-hidden>
    <rect x="14" y="6" width="20" height="34" rx="3" stroke="currentColor" strokeWidth="2" />
    <rect x="17" y="10" width="14" height="22" fill="currentColor" opacity=".25" />
    <circle cx="24" cy="36" r="1.4" fill="currentColor" />
  </svg>
)

const SmsIcon = () => (
  <svg width="26" height="26" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.8" aria-hidden>
    <rect x="2" y="5" width="20" height="13" rx="1" />
    <path d="M2 8l10 6 10-6" />
  </svg>
)

const CardIcon = () => (
  <svg width="26" height="26" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.8" aria-hidden>
    <rect x="2" y="5" width="20" height="14" rx="1" />
    <path d="M2 10h20M6 15h4" />
  </svg>
)

const METHODS: { id: Method; title: string; subtitle?: string; icon: React.ReactNode }[] = [
  {
    id: 'app',
    title: 'Met de DigiD app',
    subtitle: 'De makkelijkste manier om veilig in te loggen',
    icon: <PhoneIcon />,
  },
  { id: 'sms', title: 'Met een sms-controle', icon: <SmsIcon /> },
  { id: 'rijbewijs', title: 'Met mijn rijbewijs', icon: <CardIcon /> },
  { id: 'id-kaart', title: 'Met mijn identiteitskaart', icon: <CardIcon /> },
]

export default function DigiDAuth() {
  const ctx = useRedirectContext()
  const { set } = usePortalToken()
  const navigate = useNavigate()
  const [params] = useSearchParams()
  const nextParam = params.get('next')

  const [method, setMethod] = useState<Method>('app')
  const [showForm, setShowForm] = useState(false)
  const [username, setUsername] = useState('j.dejong')
  const [password, setPassword] = useState('123456789')
  const [submitting, setSubmitting] = useState(false)
  const [error, setError] = useState<string | null>(null)

  // Standalone login (no ctx, no next) is valid — citizen visits MijnOverheid
  // directly. Default destination: consents overview.

  const onCancel = () => {
    if (ctx) {
      const url = new URL(ctx.return_url)
      url.searchParams.set('status', 'denied')
      clearRedirectContext()
      window.location.assign(url.toString())
    } else {
      navigate('/mijnoverheid/toestemmingen')
    }
  }

  const onMethodClick = (m: Method) => {
    setMethod(m)
    setShowForm(true)
  }

  const onSubmit = async (e: React.FormEvent) => {
    e.preventDefault()
    setError(null)
    setSubmitting(true)
    try {
      // Demo simplification: the backend has no real DigiD authn.
      // Username is cosmetic; password field carries the BSN we send to the mock backend.
      const { token } = await login(password.trim())
      set(token)
      // Destination choice: ctx (issuance flow) > next (standalone) > default
      const dest = ctx ? '/consent' : (nextParam ?? '/mijnoverheid/toestemmingen')
      navigate(dest)
    } catch (err) {
      setError((err as Error).message)
    } finally {
      setSubmitting(false)
    }
  }

  return (
    <DigiDLayout>
      <div className="digid-brandrow">
        <img src="/Logo_of_DigiD.png" alt="DigiD" />
        <div>
          <div className="label">Inloggen bij</div>
          <div className="label">MijnOverheid</div>
        </div>
      </div>

      <h2 className="digid-title">Hoe wilt u inloggen?</h2>

      <div className="digid-methods">
        {METHODS.map((m) => (
          <button
            key={m.id}
            type="button"
            className={`digid-method${method === m.id && showForm ? ' selected' : ''}`}
            onClick={() => onMethodClick(m.id)}
          >
            <span className="digid-method-icon">{m.icon}</span>
            <span className="digid-method-body">
              <span className="digid-method-title">{m.title}</span>
              {m.subtitle && <span className="digid-method-subtitle">{m.subtitle}</span>}
            </span>
            <ChevronRight />
          </button>
        ))}
      </div>

      {showForm && (
        <form className="digid-form" onSubmit={onSubmit}>
          <label htmlFor="username">Gebruikersnaam</label>
          <input
            id="username"
            type="text"
            autoComplete="username"
            value={username}
            onChange={(e) => setUsername(e.target.value)}
            required
            autoFocus
          />

          <label htmlFor="password" style={{ marginTop: 12 }}>Wachtwoord</label>
          <input
            id="password"
            type="text"
            inputMode="numeric"
            pattern="\d{9}"
            maxLength={9}
            value={password}
            onChange={(e) => setPassword(e.target.value.replace(/\D/g, '').slice(0, 9))}
            required
          />
          <div className="help">
            Demo: typ een BSN (9 cijfers) als wachtwoord — dat BSN wordt naar de mock-backend gestuurd.
            In echt DigiD typ je een gewoon wachtwoord; DigiD kent je BSN al.
          </div>

          {error && <div className="error">{error}</div>}
          <button type="submit" className="digid-btn" disabled={submitting || password.length !== 9 || !username}>
            {submitting ? 'Bezig…' : 'Inloggen'}
          </button>
        </form>
      )}

      <button type="button" className="digid-cancel" onClick={onCancel}>
        <ChevronLeft />
        Annuleren
      </button>

      <div className="digid-help">
        Kunt u niet verder? Download dan de{' '}
        <a href="#" onClick={(e) => e.preventDefault()}>DigiD app</a> [opent in een nieuw venster] of activeer de{' '}
        <a href="#" onClick={(e) => e.preventDefault()}>sms-controle</a> [opent in een nieuw venster].
        <br />
        <a href="#" onClick={(e) => e.preventDefault()}>Nog geen DigiD? Vraag uw DigiD aan</a>
      </div>
    </DigiDLayout>
  )
}
