import { httpPathForSpan, isSpanError, serviceNameForSpan, type JaegerTrace } from '../api/jaegerClient'
import type { Tab } from '../types'

export type NodeStatus = 'green' | 'red' | 'yellow' | 'grey' | 'no-otel'

export type SpanInfo = {
  serviceName: string
  operationName: string
  httpPath: string
  error: boolean
}

export function collectSpans(trace: JaegerTrace): SpanInfo[] {
  return trace.spans.map((s) => ({
    serviceName: serviceNameForSpan(trace, s) ?? '',
    operationName: s.operationName,
    httpPath: httpPathForSpan(s),
    error: isSpanError(s),
  }))
}

function nodesForSpan(span: SpanInfo, mode: Tab): string[] {
  const svc = span.serviceName

  if (mode === 'issuance') {
    switch (svc) {
      case 'consent-portal-backend': return ['s02']
      case 'bsnk-mock': return ['bsnk']
      case 'consent-register': return ['s01']
      default: return []
    }
  }

  if (mode === 'eudi-issuance') {
    // The EUDI-flow uses real OpenFSC (edi-outway → bd-inway), not
    // fsc-mock/pep-service. pdp-service exposes /evaluation (the AuthZen
    // endpoint) which FSC-Inway calls directly. wallet / demo-issuer /
    // issuance-server are Rust services without OTel (downstream fallback
    // marks them 'no-otel').
    switch (svc) {
      case 'eudi-adapter': return ['eudi-adapter']
      case 'edi-outway': return ['edi-outway']
      case 'edi-manager': return ['edi-manager']
      case 'bd-inway': return ['bd-inway']
      case 'pdp-service': return ['pdp']
      case 'opa': return ['opa']
      case 'bron-sidecar': return ['sidecar']
      case 'graphql-server': return ['bron']
      default: return []
    }
  }

  // USE-flow (DvTP) — now runs on real FSC (Hypotheekverlener-mock).
  // pep-service and fsc-mock are gone. The sidecar substitutes PI→BSN.
  switch (svc) {
    case 'dienstverlener-backend': return ['afnemer']
    case 'pdp-service': return ['pdp']
    case 'opa': return ['opa']
    case 'consent-register': return ['consent-pip']
    case 'bsnk-mock': return ['bsnk']
    case 'bron-sidecar': return ['sidecar']
    case 'graphql-server': return ['bron']
    default: return []
  }
}

export const ISSUANCE_NODE_IDS = ['actor', 's02', 'bsnk', 's01']
export const USE_NODE_IDS = [
  'afnemer', 'hv-outway', 'hv-manager', 'bd-inway',
  'pdp', 'opa', 'consent-pip', 'sidecar', 'bsnk', 'bron',
]
export const EUDI_ISSUANCE_NODE_IDS = [
  'wallet', 'demo-issuer', 'issuance-server',
  'eudi-adapter', 'edi-outway', 'bd-inway',
  'pdp', 'opa', 'sidecar', 'bron',
  'edi-manager',
]

function nodeIdsFor(mode: Tab): string[] {
  if (mode === 'issuance') return ISSUANCE_NODE_IDS
  if (mode === 'eudi-issuance') return EUDI_ISSUANCE_NODE_IDS
  return USE_NODE_IDS
}

export function nodeStatesFromSpans(
  spans: SpanInfo[],
  mode: Tab,
): Record<string, NodeStatus> {
  const expected = nodeIdsFor(mode)
  const result: Record<string, NodeStatus> = {}
  for (const n of expected) result[n] = 'grey'

  for (const sp of spans) {
    const nodes = nodesForSpan(sp, mode)
    for (const node of nodes) {
      if (!(node in result)) continue
      if (result[node] === 'red') continue
      result[node] = sp.error ? 'red' : 'green'
    }
  }

  if (mode === 'issuance') {
    // Actor is the browser — no span of its own; piggyback on s02.
    if (result.s02 === 'green' || result.s02 === 'red') result.actor = 'green'
  }

  if (mode === 'eudi-issuance') {
    // Nodes without OTel instrumentation:
    //   - Rust services (wallet/demo-issuer/issuance-server)
    //   - OpenFSC containers (edi-outway/bd-inway/edi-manager)
    // If the request made it through the chain (eudi-adapter or bron has a
    // span), mark them all 'no-otel' (not 'grey' — which would imply "not
    // seen yet", while these are structurally unmeasurable).
    const anyDownstream = ['eudi-adapter', 'pdp', 'sidecar', 'bron'].some(
      (n) => result[n] === 'green' || result[n] === 'red',
    )
    if (anyDownstream) {
      for (const n of ['wallet', 'demo-issuer', 'issuance-server', 'edi-outway', 'bd-inway', 'edi-manager']) {
        if (result[n] === 'grey') result[n] = 'no-otel'
      }
    }
  }

  if (mode === 'use') {
    // Same principle for the DvTP branch: OpenFSC containers
    // hv-outway/hv-manager/bd-inway don't export spans; mark 'no-otel' if
    // downstream arrived.
    const anyDownstream = ['pdp', 'sidecar', 'bron'].some(
      (n) => result[n] === 'green' || result[n] === 'red',
    )
    if (anyDownstream) {
      for (const n of ['hv-outway', 'hv-manager', 'bd-inway']) {
        if (result[n] === 'grey') result[n] = 'no-otel'
      }
    }
  }
  // OPA-color now comes from the `pep.opa_authorize` child span (see
  // nodesForSpan above): present → OPA was reached, status follows. If the
  // span is absent (PEP early-failed before the OPA call) the OPA node
  // stays grey, reflecting reality.
  return result
}

export function pendingAllNodes(nodeIds: string[]): Record<string, NodeStatus> {
  const r: Record<string, NodeStatus> = {}
  for (const id of nodeIds) r[id] = 'yellow'
  return r
}

export type RevealStep = { node: string; status: NodeStatus }

// Build a node-reveal order from a Jaeger trace: sort spans by startTime, map
// each to its node(s), append derived nodes (actor, opa) at logical positions.
// Used by the architecture-strip to animate node-by-node in causal order
// instead of painting as data trickles in.
export function computeRevealOrder(
  trace: JaegerTrace,
  mode: Tab,
  finalStates: Record<string, NodeStatus>,
): RevealStep[] {
  const expected = nodeIdsFor(mode)
  const sortedSpans = trace.spans.slice().sort((a, b) => a.startTime - b.startTime)
  const order: RevealStep[] = []
  const seen = new Set<string>()

  for (const s of sortedSpans) {
    const info: SpanInfo = {
      serviceName: serviceNameForSpan(trace, s) ?? '',
      operationName: s.operationName,
      httpPath: httpPathForSpan(s),
      error: isSpanError(s),
    }
    for (const n of nodesForSpan(info, mode)) {
      if (!expected.includes(n) || seen.has(n)) continue
      seen.add(n)
      order.push({ node: n, status: finalStates[n] ?? 'grey' })
    }
  }

  if (mode === 'issuance' && expected.includes('actor') && !seen.has('actor')) {
    order.unshift({ node: 'actor', status: finalStates.actor ?? 'grey' })
    seen.add('actor')
  }

  // Insert PDP + OPA right after the PEP-equivalent so the reveal-order is
  // causal even if the spans race in the collector.
  //   - For both 'use' and 'eudi-issuance': PEP-equivalent is FSC-Inway
  //     (id 'bd-inway') which calls pdp-service via the AuthZen plugin.
  if (mode === 'use' || mode === 'eudi-issuance') {
    const pepEquivId = 'bd-inway'
    let insertAfter = order.findIndex((o) => o.node === pepEquivId)
    for (const n of ['pdp', 'opa']) {
      if (!expected.includes(n) || seen.has(n)) continue
      const step: RevealStep = { node: n, status: finalStates[n] ?? 'grey' }
      if (insertAfter >= 0) {
        order.splice(insertAfter + 1, 0, step)
        insertAfter++
      } else {
        order.push(step)
      }
      seen.add(n)
    }
  }

  if (mode === 'eudi-issuance') {
    // Prepend the wallet-branch (no OTel spans, so never reached via span-loop).
    // Show them upfront in reveal-order so the strip animates left-to-right.
    for (const n of ['issuance-server', 'demo-issuer', 'wallet']) {
      if (!expected.includes(n) || seen.has(n)) continue
      order.unshift({ node: n, status: finalStates[n] ?? 'grey' })
      seen.add(n)
    }
  }

  for (const n of expected) {
    if (!seen.has(n)) order.push({ node: n, status: finalStates[n] ?? 'grey' })
  }

  return order
}
