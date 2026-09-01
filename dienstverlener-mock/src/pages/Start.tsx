import { buildPortalUrl, configuredConsumerPeerID } from '../lib/buildPortalUrl'
import { ChecklistItem, CompletedDossierItems, DossierUser, HeaderBar } from '../components/dossier'

const VALID_UNTIL_90D = new Date(Date.now() + 90 * 24 * 3600 * 1000).toISOString()

const REDIRECT_CONTEXT = {
  service: 'aangiftegegevens',
  purpose: 'Hypotheek-aanvraag',
  scope: ['bd:ib:2025', 'bd:ib:2024'],
  client_oin: configuredConsumerPeerID(),
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
          <button className="hb-digid-btn" onClick={onLogin}>
            <img src="/Logo_of_DigiD.png" alt="" width={36} height={36} />
            <span className="label">Inloggen met DigiD</span>
          </button>
        </section>

      </main>
    </div>
  )
}
