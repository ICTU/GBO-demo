// Bronprofielen — one entry per register the demo can query.
//
// The EUDI chain does not end in a fixed bron: an inkomensverklaring reads
// from the BD bron, the akte van overlijden from the BRP bron. Which bron a
// run used is derived per run from its trace (see util/spanMapping). The
// adapter records the OIN from the activated onboarding record.
//
// This is visualization metadata only: OIN and OTEL service names connect a
// trace to the human-readable register label.

export type BronProfile = {
  id: string
  sourceOIN: string
  label: string // register name, for the playground's bron-picker
  gatewaySvc: string // OTEL_SERVICE_NAME of the sidecar in front of the bron
  gatewayName: string // node label for the gateway
  bronSvc: string // OTEL_SERVICE_NAME of the GraphQL-server
  bronName: string // node label for the bron itself
  // Same-origin path the playground queries. nginx.conf (image) and
  // vite.config.ts (dev-server) proxy it to this bron's GraphQL-server, so
  // the browser needs no published bron-port and no CORS.
  graphqlPath: string
  // Query the editor opens with. Keep it ASCII-only: graphql-go's lexer
  // (v0.8.1, and still on master) counts bytes and runes inconsistently while
  // skipping a `#` comment, so one multi-byte character desynchronises every
  // token after it — mid-query it does not even fail, it silently changes the
  // selection set. GraphiQL parses it fine; the bron is what chokes on it.
  exampleQuery: string
}

const BD_EXAMPLE = `# Klik links in de Explorer velden aan om deze query aan te passen,
# of open het Schema-tabblad voor de typegraaf.
{
  ingeschrevenPersoon(bsn: "123456789") {
    bsn
    heeftBelastingjaarAangifte(belastingjaren: [2024]) {
      aangifteIdentificatie
      belastingjaar
      belastingsoort
      status
      indieningsdatum
      ... on AangifteIH {
        verzamelinkomen { waarde valuta }
        box1Inkomen { waarde valuta }
      }
    }
  }
}
`

// Explore the BRP facts underlying the source-owned `akteVanOverlijden` view.
// BSN 999991772 (Frouke Jansen) is the one persona in the mock whose marriage
// ended in death; the adapter itself only executes the query from bronmetadata.
const BRP_EXAMPLE = `# Klik links in de Explorer velden aan om deze query aan te passen,
# of open het Schema-tabblad voor de typegraaf.
{
  ingeschrevenPersoon(bsn: "999991772") {
    bsn
    geslachtsnaam
    voornamen
    heeftHuwelijk {
      soortVerbintenis
      datumVoltrekking
      datumOntbinding
      redenOntbinding
      partners {
        geslachtsnaam
        voornamen
        geboortedatum
        datumOverlijden
        plaatsOverlijden
      }
    }
  }
}
`

// Ask LVG whether a citizen owns a verblijfsobject. The answer is that VBO-id,
// or null when the building is not theirs.
const LVG_EXAMPLE = `# Is 123456789 eigenaar van Meidoornhof 12? LVG antwoordt met het VBO-id,
# of met null als het pand niet van deze burger is.
{
  vbo(bsn: "123456789", vboId: "0632010000099412") {
    vboId
  }
}
`

export const BRON_PROFILES: BronProfile[] = [
  {
    id: 'bd',
    sourceOIN: '99999999900000000200',
    label: 'Belastingdienst',
    gatewaySvc: 'bron-sidecar',
    gatewayName: 'BD-sidecar',
    bronSvc: 'graphql-server',
    bronName: 'BD GraphQL-server',
    graphqlPath: '/bron-api/bd/graphql',
    exampleQuery: BD_EXAMPLE,
  },
  {
    id: 'brp',
    sourceOIN: '99999999900000000400',
    label: 'BRP',
    gatewaySvc: 'brp-sidecar',
    gatewayName: 'BRP-sidecar',
    bronSvc: 'brp-graphql-server',
    bronName: 'BRP GraphQL-server',
    graphqlPath: '/bron-api/brp/graphql',
    exampleQuery: BRP_EXAMPLE,
  },
  {
    // LVG is a logical source on the Belastingdienst's provider peer, so it
    // shares that OIN: a source_oin lookup finds BD first. Only the DvTP
    // use-chain reaches LVG, and that chain names its bron through the
    // consumer (util/consumer), not through the OIN.
    id: 'lvg',
    sourceOIN: '99999999900000000200',
    label: 'LVG',
    gatewaySvc: 'lvg-sidecar',
    gatewayName: 'LVG-sidecar',
    bronSvc: 'lvg-graphql-server',
    bronName: 'LVG GraphQL-server',
    graphqlPath: '/bron-api/lvg/graphql',
    exampleQuery: LVG_EXAMPLE,
  },
]

// Deep-link to the portal's playground for a bron. Same origin as the portal
// itself, so no public bron-URL has to be configured anywhere.
export function playgroundUrlFor(bron: BronProfile): string {
  return `/playground?bron=${encodeURIComponent(bron.id)}`
}

// The BD bron: the resting state of the EUDI strip (shown until a run names
// its own bron) and Hypotheek-BV's bron in the DvTP use-flow. The BRP service
// has no pseudonym-contract consumer.
export const BD_BRON: BronProfile = BRON_PROFILES[0]

// The LVG bron: the Installatie Register's, in the DvTP use-flow.
export const LVG_BRON: BronProfile = BRON_PROFILES[2]

export function bronProfileById(id: string | undefined): BronProfile | undefined {
  if (!id) return undefined
  return BRON_PROFILES.find((b) => b.id === id)
}

export function bronProfileBySourceOIN(sourceOIN: string | undefined): BronProfile | undefined {
  if (!sourceOIN) return undefined
  return BRON_PROFILES.find((b) => b.sourceOIN === sourceOIN)
}

// Which bron-node (if any) a service name belongs to. 'sidecar' and 'bron' are
// the node-ids both the Use- and the EUDI-chain use for the last two hops.
export function bronNodeForService(svc: string): 'sidecar' | 'bron' | undefined {
  for (const b of BRON_PROFILES) {
    if (svc === b.gatewaySvc) return 'sidecar'
    if (svc === b.bronSvc) return 'bron'
  }
  return undefined
}
