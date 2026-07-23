// Architecture-strip definitions — two chains (issuance/use) + branches.
// What's here must correspond 1:1 to the real services in docker-compose.yml
// — not to an imagined ideal.

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

// USE chain (DvTP) — now on real FSC via Hypotheekverlener-mock.
// fsc-mock + pep-service are gone; DvTP follows the same AuthZen path as
// EUDI. Difference with EUDI: bron-sidecar substitutes PI→BSN
// (subject_id_type=pseudonym from the grant-property).
export const USE_CHAIN: NodeDef[] = [
  { id: 'afnemer', role: 'Afnemer', name: 'Afnemer-stack', svc: 'dienstverlener-backend' },
  { id: 'hv-outway', role: 'FSC', name: 'HV-Outway', svc: 'hv-outway' },
  { id: 'bd-inway', role: 'FSC', name: 'BD-Inway', svc: 'bd-inway' },
  // PDP is the logical decision-unit (XACML): context-handler (P3,
  // pdp-service) + decision-engine (OPA) together. The engine hangs as a
  // branch under the PDP the same way PIP-services do at the PEP. The
  // PDP-node status reflects the DECISION outcome (override in ArchStrip);
  // the OPA branch shows engine-status.
  { id: 'pdp', role: 'PDP', name: 'Policy Decision', svc: 'pdp-service' },
  { id: 'sidecar', role: 'Bron · Gateway', name: 'Bron-sidecar', svc: 'bron-sidecar' },
  { id: 'bron', role: 'Bron', name: 'GraphQL-server', svc: 'graphql-server' },
]

// Branches: hang under a parent-node in the Use chain.
export const USE_BRANCHES: NodeDef[] = [
  { id: 'hv-manager', role: 'FSC · Manager', name: 'Contract + token', svc: 'hv-manager', branchOf: 'hv-outway' },
  { id: 'consent-pip', role: 'S01 · PIP', name: 'Consent-PIP', svc: 'consent-register', branchOf: 'pdp' },
  { id: 'opa', role: 'PDP · engine', name: 'OpenFTV', svc: 'opa', branchOf: 'pdp' },
  { id: 'bsnk', role: 'BSNk', name: 'PI → BSN', svc: 'bsnk-mock', branchOf: 'sidecar' },
]

// EUDI Route 1 — wallet receives a PuB-EAA credential. Transport uses real
// OpenFSC. FSC-Inway is the PEP (via the built-in AuthZen plugin that
// calls pdp-service directly). Same AuthZen path as DvTP; the difference
// is only in the flow + subject_id_type grant-properties (EUDI:
// eudi:attestation / direct — sidecar pass-through).
export const EUDI_ISSUANCE_CHAIN: NodeDef[] = [
  { id: 'wallet', role: 'Actor', name: 'NL-Wallet', svc: 'wallet (TestFlight)' },
  { id: 'demo-issuer', role: 'QR', name: 'Demo-issuer', svc: 'eudi-demo-issuer' },
  { id: 'issuance-server', role: 'IS', name: 'Issuance-server', svc: 'eudi-issuance-server' },
  { id: 'eudi-adapter', role: 'Adapter', name: 'EUDI-adapter', svc: 'eudi-adapter' },
  { id: 'edi-outway', role: 'FSC', name: 'EDI-Outway', svc: 'edi-outway' },
  { id: 'bd-inway', role: 'FSC', name: 'BD-Inway', svc: 'bd-inway' },
  { id: 'pdp', role: 'PDP', name: 'Policy Decision', svc: 'pdp-service' },
  { id: 'sidecar', role: 'Bron · Gateway', name: 'Bron-sidecar', svc: 'bron-sidecar' },
  { id: 'bron', role: 'Bron', name: 'GraphQL-server', svc: 'graphql-server' },
]

// Branches for the EUDI chain:
//   - edi-manager: contract- + token-fetch that edi-outway relies on
//   - opa: policy-engine behind pdp-service
export const EUDI_ISSUANCE_BRANCHES: NodeDef[] = [
  { id: 'edi-manager', role: 'FSC · Manager', name: 'Contract + token', svc: 'edi-manager', branchOf: 'edi-outway' },
  { id: 'opa', role: 'PDP · engine', name: 'OpenFTV', svc: 'opa', branchOf: 'pdp' },
]

// Nodes for which we structurally get no OTel spans: Rust services
// without OTel instrumentation and OpenFSC containers (bd-inway/
// edi-outway/edi-manager/hv-outway/hv-manager don't export traces without
// specific OTel-config). The UI shows these as 'no-otel' instead of
// 'grey' — absence of data means "not measurable" here, not "not yet".
export const NO_OTEL_NODE_IDS = new Set<string>([
  'wallet', 'demo-issuer', 'issuance-server',
  'edi-outway', 'bd-inway', 'edi-manager',
  'hv-outway', 'hv-manager',
])
