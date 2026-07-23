import { buildPortalUrl } from '../lib/buildPortalUrl'

const VALID_UNTIL_90D = new Date(Date.now() + 90 * 24 * 3600 * 1000).toISOString()

const REDIRECT_CONTEXT = {
  service: 'aangiftegegevens',
  purpose: 'Hypotheek-aanvraag',
  scope: ['bd:ib:2025', 'bd:ib:2024'],
  client_oin: '00000001234567890000',
  client_name: 'Hypotheek-BV',
  valid_until: VALID_UNTIL_90D,
  return_url: `${window.location.origin}/return`,
}

export default function Start() {
  const onLogin = () => {
    window.location.assign(buildPortalUrl(REDIRECT_CONTEXT))
  }

  return (
    <div className="hb-shell">
      <header className="hb-header">
        <div className="hb-header-left">
          <span className="hb-logo">H</span>
          <span className="hb-brand">Hypotheek-BV</span>
        </div>
        <div className="hb-header-right">
          <span className="user-icon" aria-hidden /> J. de Vries · Mijn dossier
        </div>
      </header>

      <main className="hb-main">
        <div className="hb-eyebrow">Mijn dossier · Hypotheekaanvraag</div>
        <h1 className="hb-title">Uw dossier is bijna compleet</h1>
        <p className="hb-subtitle">
          Er ontbreekt nog één onderdeel. Vul uw inkomensgegevens aan om uw aanvraag af te ronden.
        </p>

        <ul className="hb-checklist" role="list">
          <li className="hb-check-item">
            <span className="hb-check-icon done" aria-hidden>✓</span>
            <div className="hb-check-body">
              <div className="hb-check-title">Persoonlijke gegevens</div>
              <div className="hb-check-meta">Naam, adres, contactgegevens</div>
            </div>
            <span className="hb-check-status done">Compleet</span>
          </li>

          <li className="hb-check-item">
            <span className="hb-check-icon done" aria-hidden>✓</span>
            <div className="hb-check-body">
              <div className="hb-check-title">Woning &amp; aankoop</div>
              <div className="hb-check-meta">
                Aankoopprijs € 425.000 · gewenste hypotheek € 380.000
              </div>
            </div>
            <span className="hb-check-status done">Compleet</span>
          </li>

          <li className="hb-check-item pending">
            <span className="hb-check-icon todo" aria-hidden>+</span>
            <div className="hb-check-body">
              <div className="hb-check-title">Inkomensgegevens</div>
              <div className="hb-check-meta">Inkomstenbelasting 2024 en 2025</div>
            </div>
            <span className="hb-check-status todo">Nog aanvullen</span>
          </li>
        </ul>

        <section className="hb-cta">
          <h2>Inkomensgegevens aanvullen</h2>
          <p>
            Wij hebben uw inkomen over 2024 en 2025 nodig. U kunt dit veilig laten ophalen bij de
            Belastingdienst via MijnOverheid — u hoeft geen papieren op te sturen. Log in met DigiD om
            te beginnen.
          </p>
          <button className="hb-digid-btn" onClick={onLogin}>
            <img src="/Logo_of_DigiD.png" alt="" width={36} height={36} />
            <span className="label">Inloggen met DigiD</span>
          </button>
        </section>

      </main>
    </div>
  )
}
