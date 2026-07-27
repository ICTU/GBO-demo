import { QRCodeSVG } from 'qrcode.react'
import ExternalLink from './ExternalLink'
import { links } from '../config'
import { walletUniversalLink } from '../eudi'

const DVTP_STEPS = [
  <>HypotheekBV vraagt je inkomensgegevens op voor een hypotheekaanvraag.</>,
  <>
    Je beoordeelt dat verzoek als burger in het{' '}
    <ExternalLink href={links.toestemmingsportaal} describes="het toestemmingsportaal">
      toestemmingsportaal
    </ExternalLink>{' '}
    — daar opent de flow zelf een tabblad.
  </>,
  <>HypotheekBV bevraagt de bron en toont wat er terugkomt.</>,
]

export default function TryItOut() {
  const crossDeviceLink = walletUniversalLink('cross_device')
  const sameDeviceLink = walletUniversalLink('same_device')

  return (
    <section id="uitproberen" data-reveal className="section section--blue reveal">
      <div className="shell">
        <h2 className="h2-serif h2-serif--tight">Zelf uitproberen</h2>
        <p className="prose prose--measure cta-lede">
          Twee stromen draaien live in deze demo. Je start ze allebei hieronder; voor de
          wallet-stroom heb je daarnaast de EUDI-wallet-app op je telefoon nodig.
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
              Je loopt alle drie de rollen langs: eerst als afnemer die gegevens nodig heeft, dan
              als burger die daar toestemming voor geeft, en tot slot zie je wat de bron vrijgeeft.
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
                <p className="tryout-meta">EUDI · scan met de EUDI-wallet-app</p>
              </div>
            </div>
            <p className="tryout-desc">
              De issuer haalt je inkomensgegevens bij de bron op en zet er een inkomensverklaring
              van in je wallet. Die deel je daarna zelf, zonder dat de bron opnieuw bevraagd wordt.
            </p>
            {crossDeviceLink ? (
              <>
                <div className="tryout-qr">
                  <QRCodeSVG
                    value={crossDeviceLink}
                    size={188}
                    bgColor="#ffffff"
                    fgColor="#05131f"
                    marginSize={2}
                    aria-label="QR-code om de inkomensverklaring in je EUDI-wallet te laden"
                  />
                </div>
                <p className="tryout-note">
                  Scan met een andere telefoon dan waar je deze pagina op leest. Zit je al op je
                  telefoon?{' '}
                  <a href={sameDeviceLink} className="link-underline">
                    Open de wallet direct
                  </a>
                  .
                </p>
              </>
            ) : (
              /* Zonder publiek bereikbare issuance-server kan de wallet de
                 sessie niet openen; een QR tonen zou een dood spoor zijn. */
              <div className="tryout-qr tryout-qr--empty">
                <p>
                  Deze omgeving heeft geen publiek bereikbare issuance-server, dus er valt niets te
                  scannen. Zet <code>EUDI_PUBLIC_URL</code> zodra de wallet erbij kan.
                </p>
              </div>
            )}
          </article>
        </div>

        <aside className="tryout-aside">
          <h3 className="tryout-aside-title">Daarna: kijken wat er onder water gebeurde</h3>
          <p className="tryout-aside-desc">
            De developer-portal is geen vierde stroom maar een inspectieconsole. Draai eerst een van
            de twee flows hierboven; daarna zie je in de portal per hop terug wat er langskwam — het
            verzoek, de beleidsbeslissing met de regel die hem nam, het FSC-contract en de trace.
          </p>
          <ExternalLink
            href={links.developerPortal}
            describes="de developer-portal"
            className="link-underline tryout-aside-link"
          >
            Open de developer-portal ↗
          </ExternalLink>
        </aside>
      </div>
    </section>
  )
}
