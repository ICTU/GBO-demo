import ExternalLink from './ExternalLink'
import { docs } from '../config'
import { BENEFITS } from '../data/content'

export default function WhatIsGbo() {
  return (
    <section id="wat-is-gbo" data-reveal className="section reveal">
      <div className="shell grid-12">
        <div className="span-7">
          <h2 className="h2-serif h2-serif--spaced">Wat doet GBO?</h2>
          <p className="prose prose--measure prose--flow">
            Het programma Gemeenschappelijke Bronontsluiting (GBO) ontwikkelt een
            gestandaardiseerde ontsluiting waarmee overheidsorganisaties (bronhouders) hun gegevens
            direct interoperabel en herbruikbaar beschikbaar stellen voor de Europese Digitale
            Identiteit wallet (EUDI-Wallet), het Once-Only Technical System (OOTS) en Delen via
            Toestemming met Private dienstverleners (DvTP).
          </p>
          <p className="prose prose--measure prose--muted prose--flow-last">
            GBO richt zich vanuit het perspectief van bronhouders op de gemeenschappelijke aspecten
            van bronontsluiting.
          </p>
          <ExternalLink href={docs.gbo} describes="de GBO-documentatie" className="link-underline">
            Lees meer in de GBO-documentatie ↗
          </ExternalLink>
        </div>
        <div className="span-5 benefits">
          <h3>Wat het oplevert</h3>
          {BENEFITS.map((benefit) => (
            <div key={benefit} className="benefit">
              {benefit}
            </div>
          ))}
        </div>
      </div>
    </section>
  )
}
