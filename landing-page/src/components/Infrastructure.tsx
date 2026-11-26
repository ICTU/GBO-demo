import ExternalLink from './ExternalLink'
import { docs, eudiFscAvailable, links } from '../config'

export default function Infrastructure() {
  return (
    <section data-reveal className="section reveal">
      <div className="shell grid-12">
        <div className="span-4 infra-lead">
          <h2 className="h2-serif">Inzicht in de infrastructuur</h2>
          <p>
            De operationele kant van de demo, voor wie wil zien hoe contracts, requests en traces er
            in het echt uitzien.
          </p>
        </div>
        <div className="span-8 infra-cols">
          <div>
            <h3>FSC-controllers</h3>
            <ExternalLink
              href={links.fscControllerBron}
              describes="de FSC-controller van de bronhouder"
              className="infra-link"
            >
              Bronhouder (minbzk) ↗
            </ExternalLink>
            <ExternalLink
              href={links.fscControllerDvtp}
              describes="de FSC-controller van de afnemer DvTP"
              className="infra-link"
            >
              Afnemer DvTP (minezk) ↗
            </ExternalLink>
            {eudiFscAvailable && (
              <ExternalLink
                href={links.fscControllerEudi}
                describes="de FSC-controller van de afnemer EUDI"
                className="infra-link"
              >
                Afnemer EUDI (minezk) ↗
              </ExternalLink>
            )}
          </div>
          <div>
            <h3>Observability</h3>
            <ExternalLink href={links.jaeger} describes="de traces" className="infra-link">
              Traces ↗
            </ExternalLink>
            <ExternalLink href={links.grafana} describes="de logs en metrics" className="infra-link">
              Logs en metrics ↗
            </ExternalLink>
          </div>
          <div>
            <h3>Broncode en context</h3>
            <ExternalLink
              href={docs.github}
              describes="de GitHub-repository ICTU/GBO-demo"
              className="infra-link"
            >
              GitHub: ICTU/GBO-demo ↗
            </ExternalLink>
            <ExternalLink href={docs.gbo} describes="de GBO-documentatie" className="infra-link">
              GBO-documentatie ↗
            </ExternalLink>
            <ExternalLink
              href={docs.fsc}
              describes="de FSC-standaard op fsc-standaard.nl"
              className="infra-link"
            >
              FSC (Federatieve Service Connectiviteit) ↗
            </ExternalLink>
            <ExternalLink
              href={docs.simulatie}
              describes="de Simulatieomgeving Federatief Datastelsel"
              className="infra-link"
            >
              Simulatieomgeving Federatief Datastelsel ↗
            </ExternalLink>
            <a href={docs.contactMail} className="infra-link">
              Contact: gbo@ictu.nl
            </a>
          </div>
        </div>
      </div>
    </section>
  )
}
