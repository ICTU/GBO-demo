import { useMemo, useState } from 'react'
import { useNavigate } from 'react-router-dom'
import MijnOverheidLayout from '../components/MijnOverheidLayout'
import ScopeToggle from '../components/consent/ScopeToggle'
import PartialConsentWarning from '../components/consent/PartialConsentWarning'
import { useRedirectContext, clearRedirectContext } from '../hooks/useRedirectContext'
import { usePortalToken } from '../hooks/usePortalToken'
import { createConsent } from '../api/portalClient'
import { SCOPE_GROUPS } from '../data/scopeGroups'

function formatValidUntil(iso: string): string {
  if (!iso) return '3 maanden na toestemming'
  const d = new Date(iso)
  if (isNaN(d.getTime())) return iso
  const now = new Date()
  const months = Math.round(
    (d.getTime() - now.getTime()) / (1000 * 60 * 60 * 24 * 30),
  )
  if (months >= 1 && months <= 12) return `${months} maanden na toestemming`
  return d.toLocaleDateString('nl-NL', { year: 'numeric', month: 'long', day: 'numeric' })
}

function computeValiditySeconds(iso: string): number {
  if (!iso) return 90 * 24 * 3600
  const ms = new Date(iso).getTime() - Date.now()
  return ms > 0 ? Math.floor(ms / 1000) : 90 * 24 * 3600
}

export default function PortaalConsent() {
  const ctx = useRedirectContext()
  const { token, clear } = usePortalToken()
  const navigate = useNavigate()

  const [picked, setPicked] = useState<Record<string, boolean>>(() =>
    Object.fromEntries(SCOPE_GROUPS.map((s) => [s.code, true])),
  )
  const [submitting, setSubmitting] = useState(false)
  const [error, setError] = useState<string | null>(null)

  const selectedCount = useMemo(
    () => SCOPE_GROUPS.filter((s) => picked[s.code]).length,
    [picked],
  )
  const partial = selectedCount > 0 && selectedCount < SCOPE_GROUPS.length

  if (!ctx) {
    return (
      <MijnOverheidLayout activeNav="toestemmingen" breadcrumb={[{ label: 'Home' }]}>
        <div className="content-card">
          <h1 className="page-title">Geen sessie</h1>
          <p>Open dit scherm via een dienstverlener. Er is geen redirect-context gevonden.</p>
        </div>
      </MijnOverheidLayout>
    )
  }
  if (!token) {
    navigate('/auth')
    return null
  }

  const onGrant = async () => {
    setError(null)
    setSubmitting(true)
    try {
      const chosen = SCOPE_GROUPS.filter((s) => picked[s.code]).map((s) => s.code)
      const result = await createConsent(token, {
        dienstverlener_oin: ctx.client_oin,
        scopes: chosen,
        validity_seconds: computeValiditySeconds(ctx.valid_until),
      })
      sessionStorage.setItem(
        'gbo.last_consent',
        JSON.stringify({ ...result, scopes: chosen, partial }),
      )
      navigate('/bevestiging')
    } catch (err) {
      setError((err as Error).message)
    } finally {
      setSubmitting(false)
    }
  }

  const onDeny = () => {
    const url = new URL(ctx.return_url)
    url.searchParams.set('status', 'denied')
    clearRedirectContext()
    clear()
    window.location.assign(url.toString())
  }

  const onCancel = () => navigate('/auth')

  return (
    <MijnOverheidLayout
      activeNav="toestemmingen"
      breadcrumb={[{ label: 'Home', href: '#' }, { label: 'Toestemming geven' }]}
    >
      <div className="content-card">
        <h1 className="page-title">
          Welke gegevens mag <span className="accent">{ctx.client_name}</span> opvragen?
        </h1>

        <div className="summary-grid">
          <div>
            <div className="summary-label">Wie vraagt</div>
            <div className="summary-value">{ctx.client_name}</div>
          </div>
          <div>
            <div className="summary-label">Doel</div>
            <div className="summary-value">{ctx.purpose}</div>
          </div>
          <div>
            <div className="summary-label">Geldig tot</div>
            <div className="summary-value">{formatValidUntil(ctx.valid_until)}</div>
          </div>
        </div>

        <p className="guidance">
          Kies welke gegevens u wilt delen. U kunt uw toestemming later intrekken onder{' '}
          <strong>Toestemmingen</strong>.
        </p>

        <div className="scope-list">
          {SCOPE_GROUPS.map((s) => (
            <ScopeToggle
              key={s.code}
              scope={s}
              checked={!!picked[s.code]}
              onChange={(v) => setPicked((p) => ({ ...p, [s.code]: v }))}
            />
          ))}
        </div>

        {partial && (
          <div style={{ marginTop: 16 }}>
            <PartialConsentWarning clientName={ctx.client_name} />
          </div>
        )}

        {error && (
          <div className="partial-warning" style={{ marginTop: 16 }} role="alert">
            <span className="icon">!</span>
            <div>Aanmaken van toestemming mislukt: {error}</div>
          </div>
        )}

        <div className="actions">
          <button
            className="btn btn-primary"
            onClick={onGrant}
            disabled={submitting || selectedCount === 0}
          >
            {submitting ? 'Bezig…' : 'Geef toestemming'}
          </button>
          <button className="btn btn-secondary" onClick={onDeny} disabled={submitting}>
            Weiger
          </button>
          <button className="btn btn-link" onClick={onCancel} disabled={submitting}>
            Annuleren
          </button>
          <span className="counter">
            {selectedCount} van {SCOPE_GROUPS.length} onderdelen geselecteerd
          </span>
        </div>
      </div>
    </MijnOverheidLayout>
  )
}
