// Architecture-strip definitions — two chains (issuance/use) + branches.
// What's here must correspond 1:1 to the real services in docker-compose.yml
// — not to an imagined ideal.

import { BD_BRON, type BronProfile } from './bronnen'
import { HYPOTHEEK_BV, type Consumer } from '../util/consumer'

export type NodeDef = {
  id: string
  role: string // top uppercase label
  name: string // larger middle label
  svc: string // monospace sub-label (container/role name)
  branchOf?: string // for branches only: parent node-id
}

export const ISSUANCE_CHAIN: NodeDef[] = [
  { id: 'actor', role: 'Actor', name: 'Burger', svc: 'mock-bsn' },
  { id: 's02', role: 'S02', name: 'Consent-portal', svc: 'consent-portal-backend' },
  { id: 'bsnk', role: 'BSNk', name: 'Pseudonimisering', svc: 'bsnk' },
  { id: 's01', role: 'S01', name: 'Consent-register', svc: 'consent-register' },
]

// USE chain (DvTP) — on real FSC. DvTP follows the same AuthZen path as
// EUDI. Difference with EUDI: the sidecar substitutes PI→BSN
// (subject_id_type=pseudonym from the grant-property).
//
// The consumer's half is consumer-dependent: Hypotheek-BV asks the BD bron
// through its own peer, the Installatie Register asks LVG through its own.
// Both sources sit behind the same provider Inway (bd-inway). The node ids
// stay the same for both; which services they name is a labelling matter
// (see consumerForServices in util/consumer).
export function useChain(consumer: Consumer = HYPOTHEEK_BV): NodeDef[] {
  return [
    { id: 'afnemer', role: 'Afnemer', name: consumer.name, svc: consumer.backendSvc },
    { id: 'outway', role: 'FSC', name: consumer.outwayName, svc: consumer.outwaySvc },
    { id: 'bd-inway', role: 'FSC', name: 'BD-Inway', svc: 'bd-inway' },
    // PDP is the logical decision-unit (XACML): context-handler (P3,
    // context-handler now runs inside the OpenFTV PDP as a request-mapper). The engine hangs as a
    // branch under the PDP the same way PIP-services do at the PEP. The
    // PDP-node status reflects the DECISION outcome (override in ArchStrip);
    // the OPA branch shows engine-status.
    { id: 'pdp', role: 'PDP', name: 'Policy Decision', svc: 'pdp-service' },
    { id: 'sidecar', role: 'Bron · Gateway', name: consumer.bron.gatewayName, svc: consumer.bron.gatewaySvc },
    { id: 'bron', role: 'Bron', name: consumer.bron.bronName, svc: consumer.bron.bronSvc },
  ]
}

// Branches: hang under a parent-node in the Use chain.
export function useBranches(consumer: Consumer = HYPOTHEEK_BV): NodeDef[] {
  return [
    { id: 'outway-manager', role: 'FSC · Manager', name: 'Contract + token', svc: consumer.managerSvc, branchOf: 'outway' },
    { id: 'consent-pip', role: 'S01 · PIP', name: 'Consent-PIP', svc: 'consent-register', branchOf: 'pdp' },
    { id: 'opa', role: 'PDP · engine', name: 'OpenFTV', svc: 'opa', branchOf: 'pdp' },
    { id: 'bsnk', role: 'BSNk', name: 'PI → BSN', svc: 'bsnk-mock', branchOf: 'sidecar' },
  ]
}

// EUDI Route 1 — wallet receives a PuB-EAA credential. Transport uses real
// OpenFSC. FSC-Inway is the PEP (via the built-in AuthZen plugin that
// calls pdp-service directly). Same AuthZen path as DvTP; the difference is
// the evidence the request carries — a disclosed PID here, a verified consent
// token on the DvTP route — plus the subject_id_type grant-property (EUDI:
// direct — sidecar pass-through).
//
// The last two nodes are bron-dependent: which register a run reads from
// follows from the usecase (BD for the inkomensverklaringen, BRP for the akte
// van overlijden), so the chain is a function of the bronprofiel the run
// turned out to use — see bronForSpans in util/spanMapping. Until a run names
// its bron the BD pair is shown (BD_BRON), which is what three of the four
// usecases and the whole DvTP-flow use.
export function eudiIssuanceChain(bron: BronProfile = BD_BRON): NodeDef[] {
  return [
    { id: 'wallet', role: 'Actor', name: 'NL-Wallet', svc: 'wallet (TestFlight)' },
    // De QR-stap zit architectuurgewijs bij de issuer, maar wordt in deze
    // demo door het portal zelf gerenderd (EudiQrPanel bouwt de
    // universal-link client-side). De nl-wallet demo_issuer draaide hier
    // ooit voor; die is verwijderd omdat hij niet in compose stond.
    { id: 'demo-issuer', role: 'QR', name: 'QR / universal-link', svc: 'developer-portal' },
    { id: 'issuance-server', role: 'IS', name: 'Issuance-server', svc: 'eudi-issuance-server' },
    { id: 'eudi-adapter', role: 'Adapter', name: 'EUDI-adapter', svc: 'eudi-adapter' },
    { id: 'edi-outway', role: 'FSC', name: 'EDI-Outway', svc: 'edi-outway' },
    { id: 'bd-inway', role: 'FSC', name: 'BD-Inway', svc: 'bd-inway' },
    { id: 'pdp', role: 'PDP', name: 'Policy Decision', svc: 'pdp-service' },
    { id: 'sidecar', role: 'Bron · Gateway', name: bron.gatewayName, svc: bron.gatewaySvc },
    { id: 'bron', role: 'Bron', name: bron.bronName, svc: bron.bronSvc },
  ]
}

// Branches for the EUDI chain:
//   - edi-manager: contract- + token-fetch that edi-outway relies on
//   - opa: policy-engine behind pdp-service
export const EUDI_ISSUANCE_BRANCHES: NodeDef[] = [
  { id: 'edi-manager', role: 'FSC · Manager', name: 'Contract + token', svc: 'edi-manager', branchOf: 'edi-outway' },
  { id: 'opa', role: 'PDP · engine', name: 'OpenFTV', svc: 'opa', branchOf: 'pdp' },
]

// Nodes for which we structurally get no OTel spans: browser-/Rust-side
// steps without OTel instrumentation and OpenFSC containers (bd-inway/
// edi-outway/edi-manager and the use-chain's outway/outway-manager don't
// export traces without specific OTel-config). The UI shows these as 'no-otel' instead of
// 'grey' — absence of data means "not measurable" here, not "not yet".
export const NO_OTEL_NODE_IDS = new Set<string>([
  'wallet', 'demo-issuer', 'issuance-server',
  'edi-outway', 'bd-inway', 'edi-manager',
  'outway', 'outway-manager',
])
