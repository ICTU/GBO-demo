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
            Overheidsorganisaties (bronhouders) staan voor de opgave om gegevens zoals inkomen,
            rijbewijzen of diploma&rsquo;s veilig en eenvoudig beschikbaar te maken voor onder
            andere de Europese Digitale Identiteit (EUDI-wallet), het Once-Only Technical System
            (OOTS) en Delen via Toestemming met Private dienstverleners (DvTP). Voor deze drie
            oplossingen ontwikkelt GBO een gestandaardiseerde ontsluiting waarmee bronhouders hun
            gegevens interoperabel en herbruikbaar beschikbaar kunnen stellen.
          </p>
          <p className="prose prose--measure prose--muted prose--flow-last">
            GBO richt zich vanuit het perspectief van bronhouders op de gemeenschappelijke aspecten
            van bronontsluiting. De bronhouder houdt regie.
          </p>
          <p className="prose prose--measure prose--flow">
            Op de{' '}
            <ExternalLink
              href={docs.gbo}
              describes="de GBO-documentatie-omgeving"
              className="link-underline"
            >
              documentatie-omgeving ↗
            </ExternalLink>{' '}
            vind je de inhoudelijke uitwerking van GBO.
          </p>
          <p className="prose prose--measure">
            Op de{' '}
            <ExternalLink
              href={docs.informatiesite}
              describes="de GBO-informatiesite"
              className="link-underline"
            >
              informatiesite ↗
            </ExternalLink>{' '}
            lees je over aanleiding en doel van het programma, en word je op de hoogte gehouden van
            de voortgang.
          </p>
        </div>
        <div className="span-5 benefits">
          <h3>Voordelen GBO</h3>
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
