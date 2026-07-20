// Map a run-response to per-node states for the architecture-strip.
//
// Issuance: we have the real api_calls array from consent-portal-backend.
// From that we derive exactly which subcalls succeeded or failed.
//
// Use: dienstverlener-backend now returns only {allowed, reason, trace_id}.
// We infer state from the reason-string (reason-codes). When in doubt:
// grey (honest: "don't know").

import type { IssuanceResponse, UseResponse } from '../types'

export type NodeState = 'green' | 'red' | 'yellow' | 'grey' | 'no-otel'

export type ArchStates = Record<string, NodeState>

// --------- Issuance ---------

export function statesForIssuance(res: IssuanceResponse | null, error: boolean): ArchStates {
  if (error) {
    return { actor: 'green', s02: 'red', bsnk: 'grey', s01: 'grey' }
  }
  if (!res) return {}
  const states: ArchStates = { actor: 'green', s02: 'green', bsnk: 'grey', s01: 'grey' }
  const calls = res.api_calls ?? []
  for (const c of calls) {
    const ok = c.status >= 200 && c.status < 300
    if (c.url.includes('/pseudonymize')) states.bsnk = ok ? 'green' : 'red'
    else if (c.url.includes('/consents')) states.s01 = ok ? 'green' : 'red'
  }
  // If BSNk failed, S01 was never touched → stays grey (no update above).
  return states
}

// --------- Use ---------

export function statesForUse(res: UseResponse | null, error: boolean): ArchStates {
  // DvTP now follows the same AuthZen path as EUDI. Nodes:
  //   afnemer → hv-outway → hv-manager (branch) → bd-inway →
  //   pdp → opa (branch) + consent-pip (branch) → sidecar → bsnk (branch) → bron
  if (error) {
    return {
      afnemer: 'red',
      'hv-outway': 'grey', 'hv-manager': 'grey', 'bd-inway': 'grey',
      pdp: 'grey', opa: 'grey', 'consent-pip': 'grey',
      sidecar: 'grey', bsnk: 'grey', bron: 'grey',
    }
  }
  if (!res) return {}
  if (res.allowed) {
    return {
      afnemer: 'green', 'hv-outway': 'green', 'hv-manager': 'green', 'bd-inway': 'green',
      pdp: 'green', opa: 'green', 'consent-pip': 'green',
      sidecar: 'green', bsnk: 'green', bron: 'green',
    }
  }
  // DENY — default: the chain ran up to OPA and denied there.
  const r = (res.reason ?? '').toLowerCase()
  let s: ArchStates = {
    afnemer: 'green', 'hv-outway': 'green', 'hv-manager': 'green', 'bd-inway': 'green',
    pdp: 'green', 'consent-pip': 'green',
    opa: 'red', sidecar: 'grey', bsnk: 'grey', bron: 'grey',
  }

  // Consent-lookup failed at the afnemer-backend (before the FSC-hop).
  if (r.includes('consent_lookup_failed')) {
    s = {
      afnemer: 'red',
      'hv-outway': 'grey', 'hv-manager': 'grey', 'bd-inway': 'grey',
      pdp: 'grey', opa: 'grey', 'consent-pip': 'grey',
      sidecar: 'grey', bsnk: 'grey', bron: 'grey',
    }
  }
  // FSC transport errors (grant/inway) — outway or inway fails.
  else if (r.startsWith('fsc_') || r.includes('unauthorized')) {
    s = {
      afnemer: 'green', 'hv-outway': 'red',
      'hv-manager': 'grey', 'bd-inway': 'grey',
      pdp: 'grey', opa: 'grey', 'consent-pip': 'grey',
      sidecar: 'grey', bsnk: 'grey', bron: 'grey',
    }
  }
  return s
}
