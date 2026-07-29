/* Elke link op deze pagina wijst naar een andere service. Waar die
   service draait verschilt per omgeving, dus komt de URL van buiten:
   in de nginx-image uit window.__GBO_RUNTIME_CONFIG__ (envsubst op
   runtime-config.js.template), in de dev-server uit VITE_*, en anders
   uit de compose-defaults hieronder. */

/* Een variabele die envsubst niet kon invullen blijft als letterlijke
   ${VAR} in runtime-config.js staan. De Dockerfile declareert ze
   allemaal leeg, maar een template die daar ooit uit de pas mee loopt
   mag geen kapotte href opleveren. */
function clean(value: string | undefined): string {
  const trimmed = value?.trim() ?? ''
  return /^\$\{.*\}$/.test(trimmed) ? '' : trimmed
}

function resolve(runtime: string | undefined, env: string | undefined, fallback: string): string {
  return clean(runtime) || clean(env) || fallback
}

const rc = typeof window === 'undefined' ? undefined : window.__GBO_RUNTIME_CONFIG__

export const links = {
  developerPortal: resolve(
    rc?.developerPortalUrl,
    import.meta.env.VITE_DEVELOPER_PORTAL_URL,
    'http://localhost:9003',
  ),
  toestemmingsportaal: resolve(
    rc?.toestemmingsportaalUrl,
    import.meta.env.VITE_TOESTEMMINGSPORTAAL_URL,
    'http://localhost:9002',
  ),
  dienstverlener: resolve(
    rc?.dienstverlenerUrl,
    import.meta.env.VITE_DIENSTVERLENER_URL,
    'http://localhost:9001',
  ),
  /* De GraphQL-playground (GraphiQL 5 + explorer + Voyager) hangt onder de
     developer-portal en bedient elke bron via een bron-keuze — vandaar geen
     eigen env-var meer, maar het pad achter de portal-URL. */
  bronPlayground: `${resolve(
    rc?.developerPortalUrl,
    import.meta.env.VITE_DEVELOPER_PORTAL_URL,
    'http://localhost:9003',
  ).replace(/\/+$/, '')}/playground`,
  jaeger: resolve(rc?.jaegerUrl, import.meta.env.VITE_JAEGER_PUBLIC_URL, 'http://localhost:9686'),
  grafana: resolve(rc?.grafanaUrl, import.meta.env.VITE_GRAFANA_PUBLIC_URL, 'http://localhost:9300'),
  fscControllerBron: resolve(
    rc?.fscControllerBronUrl,
    import.meta.env.VITE_FSC_CONTROLLER_BRON_URL,
    'http://localhost:8092',
  ),
  fscControllerDvtp: resolve(
    rc?.fscControllerDvtpUrl,
    import.meta.env.VITE_FSC_CONTROLLER_DVTP_URL,
    'http://localhost:8096',
  ),
  fscControllerEudi: resolve(
    rc?.fscControllerEudiUrl,
    import.meta.env.VITE_FSC_CONTROLLER_EUDI_URL,
    'http://localhost:8094',
  ),
} as const

/* De QR wordt hier zelf samengesteld (zie src/eudi.ts). Daarvoor moet de
   wallet op de telefoon de issuance-server publiek kunnen bereiken —
   zonder publicUrl valt de QR weg en toont de pagina waarom. */
export const eudi = {
  publicUrl: resolve(rc?.eudiPublicUrl, import.meta.env.VITE_EUDI_PUBLIC_URL, ''),
  clientId: resolve(rc?.eudiClientId, import.meta.env.VITE_EUDI_CLIENT_ID, 'reader.example.com'),
  ulBase: resolve(
    rc?.eudiUlBase,
    import.meta.env.VITE_EUDI_UL_BASE,
    'https://app.preproductie.wallet.edi.bzk.nl/deeplink/disclosure_based_issuance',
  ),
} as const

/* Vaste externe bronnen — die verhuizen niet mee met de omgeving. */
export const docs = {
  gbo: 'https://ictu.github.io/GBO/latest/',
  github: 'https://github.com/ICTU/GBO-demo',
  fsc: 'https://fsc-standaard.nl/',
  simulatie: 'https://simulatie.datastelsel.nl/',
  contactMail: 'mailto:jeroen.dekok@ictu.nl',
} as const

export const versionString = resolve(rc?.versionString, import.meta.env.VITE_VERSION_STRING, '')
