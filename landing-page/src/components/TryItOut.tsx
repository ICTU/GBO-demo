import { useEffect, useState, type ReactNode } from 'react'
import { QRCodeSVG } from 'qrcode.react'
import ExternalLink from './ExternalLink'
import { links } from '../config'
import { loadWalletUsecases, walletUniversalLink, type WalletUsecase } from '../eudi'

/* Twee scenario's over dezelfde DvTP-stroom: een andere afnemer en bron,
   hetzelfde toestemmingspatroon. Daarom één kaart met een keuze, geen
   tweede stroom. */
type DvtpScenario = {
  key: string
  label: string
  desc: string
  steps: ReactNode[]
  hint?: string
  start: { href: string; describes: string; label: string }
}

const toestemmingsportaal = (
  <ExternalLink href={links.toestemmingsportaal} describes="het toestemmingsportaal">
    toestemmingsportaal
  </ExternalLink>
)

const DVTP_SCENARIOS: DvtpScenario[] = [
  {
    key: 'hypotheek',
    label: 'Hypotheek (HypotheekBV)',
    desc: 'Je speelt om de beurt de afnemer die gegevens nodig heeft en de burger die daar toestemming voor geeft. Aan het eind zie je wat de bron vrijgeeft.',
    steps: [
      <>HypotheekBV vraagt je inkomensgegevens op voor een hypotheekaanvraag.</>,
      <>
        Je beoordeelt dat verzoek als burger in het {toestemmingsportaal}, dat de flow zelf in een
        nieuw tabblad opent.
      </>,
      <>HypotheekBV bevraagt de bron en toont wat er terugkomt.</>,
    ],
    start: {
      href: links.dienstverlener,
      describes: 'de aanvraagpagina van HypotheekBV',
      label: 'Start bij HypotheekBV ↗',
    },
  },
  {
    key: 'installaties',
    label: 'Installaties (Installatie Register)',
    desc: 'Een installateur wil de installaties in je pand zien, zoals een warmtepomp. Het Installatie Register geeft pas toegang als de Landelijke Voorziening Gebouwen heeft bevestigd dat jij de eigenaar bent. Je geeft daarom twee keer toestemming.',
    steps: [
      <>Als installateur dien je een toestemmingsaanvraag in voor een pand.</>,
      <>
        Als eigenaar open je het verzoek, log je in bij het {toestemmingsportaal} en geef je
        akkoord. Daarna geef je in het Installatie Register de installateur toegang.
      </>,
      <>Als installateur bekijk je de installatiegegevens.</>,
    ],
    hint: 'Log in met BSN 123456789, eigenaar van Meidoornhof 12. Met BSN 987654321 zie je de uitkomst “geen eigenaar”. Trek je de toestemming later in, dan krijgt de installateur bij de volgende opvraging niets meer.',
    start: {
      href: links.installatieRegister,
      describes: 'het Installatie Register',
      label: 'Start bij het Installatie Register ↗',
    },
  },
]

export default function TryItOut() {
  const [scenario, setScenario] = useState(DVTP_SCENARIOS[0].key)
  const [usecases, setUsecases] = useState<WalletUsecase[]>([])
  const [usecase, setUsecase] = useState<WalletUsecase | null>(null)
  const [offersError, setOffersError] = useState('')

  useEffect(() => {
    void loadWalletUsecases()
      .then((loaded) => {
        setUsecases(loaded)
        setUsecase(loaded[0])
      })
      .catch((error: Error) => setOffersError(error.message))
  }, [])

  const crossDeviceLink = usecase ? walletUniversalLink(usecase.key, 'cross_device') : ''

  return (
    <section id="uitproberen" data-reveal className="section section--blue reveal">
      <div className="shell">
        <h2 className="h2-serif h2-serif--tight">Zelf uitproberen</h2>
        <p className="prose prose--measure cta-lede">
          Twee stromen kun je hier zelf doorlopen. Je start ze allebei hieronder; voor de
          wallet-stroom heb je de NL Wallet op je telefoon nodig.
        </p>

        <div className="tryout-grid">
          <article className="tryout">
            <div className="tryout-head">
              <span className="tryout-num">01</span>
              <div>
                <h3 className="tryout-title">Deel gegevens met toestemming</h3>
                <p className="tryout-meta">DvTP · twee scenario’s · alleen een browser nodig</p>
              </div>
            </div>
            <div className="tryout-choice" role="group" aria-label="Kies een scenario">
              {DVTP_SCENARIOS.map((s) => (
                <button
                  key={s.key}
                  type="button"
                  aria-pressed={s.key === scenario}
                  onClick={() => setScenario(s.key)}
                >
                  {s.label}
                </button>
              ))}
            </div>
            {/* Zelfde stapeling als de verklaringen in kaart 02: de kaart
                wordt zo hoog als het langste scenario. */}
            <div className="tryout-stack tryout-stack--fill">
              {DVTP_SCENARIOS.map((s) => (
                <div
                  key={s.key}
                  className="tryout-scenario"
                  aria-hidden={s.key === scenario ? undefined : true}
                >
                  <p className="tryout-desc">{s.desc}</p>
                  <ol className="tryout-steps">
                    {s.steps.map((step, i) => (
                      <li key={i}>
                        <span className="tryout-step-num" aria-hidden="true">
                          {i + 1}
                        </span>
                        <span>{step}</span>
                      </li>
                    ))}
                  </ol>
                  {s.hint && <p className="tryout-hint">{s.hint}</p>}
                  <ExternalLink href={s.start.href} describes={s.start.describes} className="btn-primary">
                    {s.start.label}
                  </ExternalLink>
                </div>
              ))}
            </div>
          </article>

          <article className="tryout">
            <div className="tryout-head">
              <span className="tryout-num">02</span>
              <div>
                <h3 className="tryout-title">Haal een verklaring op in je wallet</h3>
                <p className="tryout-meta">EUDI · scan met de NL Wallet</p>
              </div>
            </div>
            <div className="tryout-choice" role="group" aria-label="Kies welke verklaring je ophaalt">
              {usecases.map((u) => (
                <button
                  key={u.key}
                  type="button"
                  aria-pressed={u.key === usecase?.key}
                  onClick={() => setUsecase(u)}
                >
                  {u.label}
                </button>
              ))}
            </div>
            {/* Alle varianten in dezelfde grid-cel: de cel wordt zo hoog als
                de langste tekst, dus het wisselen verschuift niets — ook de
                kaart ernaast niet, die in dezelfde grid-rij meerekt. */}
            <div className="tryout-stack">
              {usecases.map((u) => (
                <p
                  key={u.key}
                  className="tryout-desc"
                  aria-hidden={u.key === usecase?.key ? undefined : true}
                >
                  {u.description}
                </p>
              ))}
            </div>
            {usecase && crossDeviceLink ? (
              <>
                <div className="tryout-qr">
                  <QRCodeSVG
                    value={crossDeviceLink}
                    size={188}
                    bgColor="#ffffff"
                    fgColor="#0e2b84"
                    marginSize={2}
                    aria-label={`QR-code om de ${usecase.label.toLowerCase()} in je NL Wallet te laden`}
                  />
                </div>
                <div className="tryout-stack">
                  {usecases.map((u) => (
                    <p
                      key={u.key}
                      className="tryout-note"
                      aria-hidden={u.key === usecase.key ? undefined : true}
                    >
                      Scan met een andere telefoon dan waar je deze pagina op leest.
                      Zit je al op je telefoon?{' '}
                      <a href={walletUniversalLink(u.key, 'same_device')} className="link-underline">
                        Open de NL Wallet
                      </a>
                      .
                    </p>
                  ))}
                </div>
              </>
            ) : (
              /* Zonder publiek bereikbare issuance-server kan de wallet de
                 sessie niet openen; een QR tonen zou een dood spoor zijn. */
              <div className="tryout-qr tryout-qr--empty">
                <p>
                  {offersError
                    ? `Het gegenereerde wallet-aanbod kon niet worden geladen: ${offersError}.`
                    : 'Het wallet-aanbod wordt geladen, of de issuance-server is niet publiek bereikbaar.'}
                </p>
              </div>
            )}
          </article>
        </div>

        <div className="tryout-asides">
          <aside className="tryout-aside">
            <h3 className="tryout-aside-title">Ook: meekijken in de developer-portal</h3>
            <p className="tryout-aside-desc">
              De developer-portal laat per hop zien wat er langskwam: het verzoek, welke
              beleidsregel het toestond of tegenhield, het FSC-contract en de trace. Je kunt er ook
              zelf verzoeken afvuren, met een kant-en-klaar scenario of een eigen request.
            </p>
            <ExternalLink
              href={links.developerPortal}
              describes="de developer-portal"
              className="link-underline tryout-aside-link"
            >
              Open de developer-portal ↗
            </ExternalLink>
          </aside>

          <aside className="tryout-aside">
            <h3 className="tryout-aside-title">Of: zelf de bron bevragen</h3>
            <p className="tryout-aside-desc">
              De developer-portal serveert een GraphQL-playground voor elke bron: kies de
              Belastingdienst- of de BRP-bron, klik in GraphiQL een query bij elkaar, of bekijk in
              Voyager hoe dat bronschema eruitziet. Deze ingang gaat rechtstreeks naar de bron, dus
              zonder toestemming, FSC-contract en beleidstoetsing.
            </p>
            <ExternalLink
              href={links.bronPlayground}
              describes="de GraphQL-playground van de bronnen"
              className="link-underline tryout-aside-link"
            >
              Open de GraphQL-playground ↗
            </ExternalLink>
          </aside>
        </div>
      </div>
    </section>
  )
}
