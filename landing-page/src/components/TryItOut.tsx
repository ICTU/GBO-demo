import { useState } from 'react'
import { QRCodeSVG } from 'qrcode.react'
import ExternalLink from './ExternalLink'
import { links } from '../config'
import { WALLET_USECASES, walletUniversalLink } from '../eudi'

const DVTP_STEPS = [
  <>HypotheekBV vraagt je inkomensgegevens op voor een hypotheekaanvraag.</>,
  <>
    Je beoordeelt dat verzoek als burger in het{' '}
    <ExternalLink href={links.toestemmingsportaal} describes="het toestemmingsportaal">
      toestemmingsportaal
    </ExternalLink>
    , dat de flow zelf in een nieuw tabblad opent.
  </>,
  <>HypotheekBV bevraagt de bron en toont wat er terugkomt.</>,
]

export default function TryItOut() {
  const [usecase, setUsecase] = useState(WALLET_USECASES[0])
  const crossDeviceLink = walletUniversalLink(usecase.key, 'cross_device')

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
                <p className="tryout-meta">DvTP · een paar minuten · alleen een browser nodig</p>
              </div>
            </div>
            <p className="tryout-desc">
              Je speelt om de beurt de afnemer die gegevens nodig heeft en de burger die daar
              toestemming voor geeft. Aan het eind zie je wat de bron vrijgeeft.
            </p>
            <ol className="tryout-steps">
              {DVTP_STEPS.map((step, i) => (
                <li key={i}>
                  <span className="tryout-step-num" aria-hidden="true">
                    {i + 1}
                  </span>
                  <span>{step}</span>
                </li>
              ))}
            </ol>
            <ExternalLink
              href={links.dienstverlener}
              describes="de aanvraagpagina van HypotheekBV"
              className="btn-primary"
            >
              Start bij HypotheekBV ↗
            </ExternalLink>
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
              {WALLET_USECASES.map((u) => (
                <button
                  key={u.key}
                  type="button"
                  aria-pressed={u.key === usecase.key}
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
              {WALLET_USECASES.map((u) => (
                <p
                  key={u.key}
                  className="tryout-desc"
                  aria-hidden={u.key === usecase.key ? undefined : true}
                >
                  {u.desc}
                </p>
              ))}
            </div>
            {crossDeviceLink ? (
              <>
                <div className="tryout-qr">
                  <QRCodeSVG
                    value={crossDeviceLink}
                    size={188}
                    bgColor="#ffffff"
                    fgColor="#05131f"
                    marginSize={2}
                    aria-label={`QR-code om de ${usecase.label.toLowerCase()} in je NL Wallet te laden`}
                  />
                </div>
                <div className="tryout-stack">
                  {WALLET_USECASES.map((u) => (
                    <p
                      key={u.key}
                      className="tryout-note"
                      aria-hidden={u.key === usecase.key ? undefined : true}
                    >
                      {u.scanNote} Scan met een andere telefoon dan waar je deze pagina op leest.
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
                  In deze omgeving is de issuance-server niet publiek bereikbaar, dus er valt niets
                  te scannen. Zet <code>EUDI_PUBLIC_URL</code> zodra de wallet de server kan
                  bereiken.
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
              De demo-bron serveert een eigen GraphQL-playground. Klik in GraphiQL een query bij
              elkaar, of bekijk in Voyager hoe het schema van de bron eruitziet. Deze ingang gaat
              rechtstreeks naar de bron, dus zonder toestemming, FSC-contract en beleidstoetsing.
            </p>
            <ExternalLink
              href={links.bronPlayground}
              describes="de GraphQL-playground van de demo-bron"
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
