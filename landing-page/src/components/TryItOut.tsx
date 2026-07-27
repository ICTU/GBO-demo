import ExternalLink from './ExternalLink'
import { links } from '../config'

const ENTRIES = [
  {
    href: links.developerPortal,
    describes: 'de developer-portal',
    title: 'Developer-portal',
    desc: 'Interne test-console. Volg requests, beslissingen, contracts en traces door de hele keten.',
  },
  {
    href: links.toestemmingsportaal,
    describes: 'het toestemmingsportaal',
    title: 'Toestemmingsportaal',
    desc: 'Voor de burger: openstaande toestemming-verzoeken beoordelen en eerdere toestemmingen beheren of intrekken.',
  },
  {
    href: links.dienstverlener,
    describes: 'de dienstverlener-mock HypotheekBV',
    title: 'Hypotheekverstrekker',
    desc: 'De flow van de afnemer: start een aanvraag als hypotheekverstrekker (HypotheekBV) die inkomensgegevens nodig heeft.',
  },
]

export default function TryItOut() {
  return (
    <section id="uitproberen" data-reveal className="section section--blue reveal">
      <div className="shell">
        <h2 className="h2-serif h2-serif--tight">Zelf uitproberen</h2>
        <p className="prose prose--measure cta-lede">
          Drie ingangen in de simulatieomgeving: de developer-console, de flow van de burger en de
          flow van de afnemer.
        </p>
        <div className="cta-grid">
          {ENTRIES.map((entry) => (
            <ExternalLink
              key={entry.title}
              href={entry.href}
              describes={entry.describes}
              className="cta"
            >
              <span className="cta-title">{entry.title} ↗</span>
              <span className="cta-desc">{entry.desc}</span>
            </ExternalLink>
          ))}
        </div>
      </div>
    </section>
  )
}
