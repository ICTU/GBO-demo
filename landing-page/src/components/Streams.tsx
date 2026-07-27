import ExternalLink from './ExternalLink'
import { links } from '../config'

export default function Streams() {
  return (
    <section id="stromen" data-reveal className="section section--grey reveal">
      <div className="shell">
        <div className="grid-12 streams-intro">
          <h2 className="h2-serif span-5">Drie stromen, gedeelde pipeline</h2>
          <p className="prose prose--muted span-7">
            Elke stroom heeft zijn eigen ingang, maar loopt over hetzelfde FSC-transport naar
            dezelfde generieke componenten van de bronhouder. Eén keer aansluiten volstaat: een
            nieuwe stroom vraagt geen nieuwe ontsluiting.
          </p>
        </div>

        <div className="streams">
          <article>
            <div className="stream-art stream-art--dvtp">
              <svg viewBox="0 0 200 120" aria-label="Burger geeft toestemming aan een dienstverlener">
                <line x1="46" y1="60" x2="154" y2="60" stroke="#01689b" strokeWidth="1.5" />
                <circle cx="46" cy="60" r="17" fill="none" stroke="#01689b" strokeWidth="1.5" />
                <rect x="137" y="43" width="34" height="34" fill="none" stroke="#01689b" strokeWidth="1.5" />
                <circle cx="100" cy="60" r="7" fill="#01689b" />
              </svg>
            </div>
            <p className="stream-status">Live in deze demo</p>
            <h3 className="stream-title">DvTP · Delen via Toestemming</h3>
            <p className="stream-body">
              Een burger geeft een afnemer expliciet toestemming om een afgebakende set gegevens op
              te vragen. De afnemer werkt alleen met een toestemming-ID. De bronhouder bepaalt zelf,
              binnen de eigen vertrouwensgrens, om welke persoon het gaat.
            </p>
            <p className="stream-body stream-body--muted stream-body--last">
              In de demo vraagt HypotheekBV inkomensgegevens op voor een hypotheekaanvraag. De burger
              geeft daarvoor toestemming in het toestemmingsportaal (in deze demo een
              MijnOverheid-achtige mock); pas daarna komt het verzoek langs de toetsing bij de bron.
              <br />
              <br />
              Afnemer vraagt → Burger stemt toe → Afnemer bevraagt Bron
            </p>
            <div className="stream-links">
              <ExternalLink
                href={links.dienstverlener}
                describes="de dienstverlener-mock HypotheekBV"
                className="link-underline"
              >
                Probeer de flow ↗
              </ExternalLink>
              <ExternalLink
                href={links.developerPortal}
                describes="de developer-portal"
                className="link-underline link-muted"
              >
                Bekijk in dev-portal ↗
              </ExternalLink>
            </div>
          </article>

          <article>
            <div className="stream-art stream-art--eudi">
              <svg viewBox="0 0 200 120" aria-label="Credential in de wallet van de burger">
                <rect x="62" y="30" width="76" height="60" rx="3" fill="none" stroke="#01689b" strokeWidth="1.5" />
                <rect x="78" y="46" width="44" height="28" rx="2" fill="#01689b" />
                <line x1="62" y1="102" x2="138" y2="102" stroke="#01689b" strokeWidth="1.5" opacity="0.4" />
              </svg>
            </div>
            <p className="stream-status">Live in deze demo</p>
            <h3 className="stream-title">EUDI-wallet</h3>
            <p className="stream-body">
              Een issuer haalt gegevens op bij de bronhouder en geeft daarmee een credential uit aan
              de burger, bijvoorbeeld een inkomensverklaring. De burger bewaart die in de wallet en
              deelt hem later zelf met een dienstverlener. De bron wordt dus bevraagd bij uitgifte,
              niet bij gebruik.
            </p>
            <p className="stream-body stream-body--muted stream-body--last">
              Bron → Issuer → Wallet → Dienstverlener
            </p>
            <div className="stream-links">
              <ExternalLink
                href={links.eudiIssuer}
                describes="de EUDI-issuer"
                className="link-underline"
              >
                Scan de QR (issuance) ↗
              </ExternalLink>
              <ExternalLink
                href={links.developerPortal}
                describes="de developer-portal"
                className="link-underline link-muted"
              >
                Bekijk in dev-portal ↗
              </ExternalLink>
            </div>
          </article>

          <article>
            <div className="stream-art stream-art--todo">
              <svg viewBox="0 0 200 120" aria-label="Grensoverschrijdende uitwisseling, nog te bouwen">
                <rect x="34" y="43" width="34" height="34" fill="none" stroke="#9a9a9a" strokeWidth="1.5" />
                <rect x="132" y="43" width="34" height="34" fill="none" stroke="#9a9a9a" strokeWidth="1.5" />
                <line x1="68" y1="60" x2="132" y2="60" stroke="#9a9a9a" strokeWidth="1.5" strokeDasharray="5 6" />
              </svg>
            </div>
            <p className="stream-status">Todo</p>
            <h3 className="stream-title stream-title--todo">OOTS · Once-Only Technical System</h3>
            <p className="stream-body stream-body--muted stream-body--flush">
              OOTS voegt grensoverschrijdende uitwisseling voor EU-diensten toe, volgens het
              once-only principe. Nog niet gebouwd in deze demo; de architectuur is er wel op
              voorbereid.
            </p>
          </article>
        </div>
      </div>
    </section>
  )
}
