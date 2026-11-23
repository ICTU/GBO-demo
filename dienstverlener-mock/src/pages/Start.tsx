import { useEffect, useState } from 'react'
import { startConsent } from '../lib/consentFlow'
import { ChecklistItem, CompletedDossierItems, DossierUser, HeaderBar } from '../components/dossier'

// What Hypotheek-BV asks consent for. The name and purpose the citizen sees
// come from Hypotheek-BV's registration, not from this request.
const CONSENT_REQUEST = {
  service: 'aangiftegegevens',
  scopes: ['bd:ib:2025', 'bd:ib:2024'],
}

export default function Start() {
  const [busy, setBusy] = useState(false)
  const [failed, setFailed] = useState(false)

  // Back from the portal with the browser's back button, the page may come
  // from the back-forward cache, still busy.
  useEffect(() => {
    const reset = (e: PageTransitionEvent) => { if (e.persisted) setBusy(false) }
    window.addEventListener('pageshow', reset)
    return () => window.removeEventListener('pageshow', reset)
  }, [])

  const onLogin = async () => {
    setBusy(true)
    setFailed(false)
    try {
      const validUntil = new Date(Date.now() + 90 * 24 * 3600 * 1000).toISOString()
      window.location.assign(await startConsent({ ...CONSENT_REQUEST, valid_until: validUntil }))
    } catch {
      setFailed(true)
      setBusy(false)
    }
  }

  return (
    <div className="hb-shell">
      <HeaderBar right={<DossierUser />} />

      <main className="hb-main">
        <div className="hb-eyebrow">Mijn dossier · Hypotheekaanvraag</div>
        <h1 className="hb-title">Uw dossier is bijna compleet</h1>
        <p className="hb-subtitle">
          Er ontbreekt nog één onderdeel. Vul uw inkomensgegevens aan om uw aanvraag af te ronden.
        </p>

        <ul className="hb-checklist" role="list">
          <CompletedDossierItems />
          <ChecklistItem
            state="todo"
            title="Inkomensgegevens"
            meta="Inkomstenbelasting 2024 en 2025"
            status="Nog aanvullen"
          />
        </ul>

        <section className="hb-cta">
          <h2>Inkomensgegevens aanvullen</h2>
          <p>
            Wij hebben uw inkomen over 2024 en 2025 nodig. U kunt dit veilig laten ophalen bij de
            Belastingdienst via MijnOverheid — u hoeft geen papieren op te sturen. Log in met DigiD om
            te beginnen.
          </p>
          <button className="hb-digid-btn" onClick={onLogin} disabled={busy}>
            <img src="/Logo_of_DigiD.png" alt="" width={36} height={36} />
            <span className="label">{busy ? 'Bezig…' : 'Inloggen met DigiD'}</span>
          </button>
          {failed && (
            <p className="hb-status-denied" role="alert">
              Het ophalen via MijnOverheid kan nu niet worden gestart. Probeer het later opnieuw.
            </p>
          )}
        </section>

      </main>
    </div>
  )
}
