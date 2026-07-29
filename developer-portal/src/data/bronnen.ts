// Bronprofielen — one entry per register the demo can query.
//
// The EUDI chain does not end in a fixed bron: an inkomensverklaring reads
// from the BD bron, the akte van overlijden from the BRP bron. Which bron a
// run used is derived per run from its trace (see util/spanMapping) — the
// usecase → bron mapping itself lives in
// `services/eudi-adapter/config/usecase_catalog.json` and is deliberately NOT
// duplicated here.
//
// `id` must match the `bron` key in that catalog and `gatewaySvc`/`bronSvc`
// must match OTEL_SERVICE_NAME in docker-compose.yml. Together they are the
// only thing tying a run's spans to its register, so a third bronprofiel is
// added here and nowhere else in the portal.

export type BronProfile = {
  id: string // catalog `bron` value ('bd', 'brp', …)
  gatewaySvc: string // OTEL_SERVICE_NAME of the sidecar in front of the bron
  gatewayName: string // node label for the gateway
  bronSvc: string // OTEL_SERVICE_NAME of the GraphQL-server
  bronName: string // node label for the bron itself
}

export const BRON_PROFILES: BronProfile[] = [
  {
    id: 'bd',
    gatewaySvc: 'bron-sidecar',
    gatewayName: 'BD-sidecar',
    bronSvc: 'graphql-server',
    bronName: 'BD GraphQL-server',
  },
  {
    id: 'brp',
    gatewaySvc: 'brp-sidecar',
    gatewayName: 'BRP-sidecar',
    bronSvc: 'brp-graphql-server',
    bronName: 'BRP GraphQL-server',
  },
]

// The BD bron: the resting state of the EUDI strip (shown until a run names
// its own bron) and the only bron the DvTP use-flow reaches — the BRP service
// has no pseudonym-contract consumer.
export const BD_BRON: BronProfile = BRON_PROFILES[0]

export function bronProfileById(id: string | undefined): BronProfile | undefined {
  if (!id) return undefined
  return BRON_PROFILES.find((b) => b.id === id)
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
