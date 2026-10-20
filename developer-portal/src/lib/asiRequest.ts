// The ASI tab's request at the reference QTSP: building the request the QTSP
// accepts, and reading its outcome. Pure, so it is tested without a browser.
import type { AsiExpected, AsiLoginHint, AsiPayload } from '../types.ts'

// An attribute the QTSP can request (GET /api/attributes).
export type AsiAttribute = { identifier: string; label: string; example: unknown }

// The QTSP's view of a request (GET /api/requests/{id}).
export type AsiRequestView = {
  id: string
  start_url: string
  status: 'pending' | 'done' | 'denied' | 'failed'
  operation: 'verify' | 'retrieve'
  data_service?: string
  provider?: string
  source?: string
  asip_status?: number
  problem?: string
  seal?: { valid: boolean; signer?: string; error?: string }
  results?: { identifier: string; label: string; result?: string; value?: unknown }[]
  error?: string
  error_description?: string
  // trace_id is the trace of the QTSP's call at the ASIP: the chain of this
  // request. Absent until the call is made, and for a denial.
  trace_id?: string
}

export type AsiRequestBody = {
  operation: 'verify' | 'retrieve'
  attributes: { identifier: string; value?: unknown }[]
  // login_hint prefills the QTSP's mock login; it stays on the QTSP.
  login_hint?: AsiLoginHint
}

// requestBody turns the form into the body of POST /api/requests: the values
// are parsed as JSON for a verify and left out for a retrieve, and the
// scenario's test data goes along for the QTSP's mock login. An error names
// the attribute whose value is not JSON.
export function requestBody(p: AsiPayload, labels: Record<string, string> = {}): { body: AsiRequestBody } | { error: string } {
  if (p.attributes.length === 0) return { error: 'Kies ten minste één attribuut.' }
  const attributes: AsiRequestBody['attributes'] = []
  for (const a of p.attributes) {
    if (p.operation === 'retrieve') {
      attributes.push({ identifier: a.identifier })
      continue
    }
    try {
      attributes.push({ identifier: a.identifier, value: JSON.parse(a.value ?? '') })
    } catch {
      return { error: `De waarde van ${labels[a.identifier] ?? a.identifier} is geen JSON.` }
    }
  }
  return { body: { operation: p.operation, attributes, ...(p.login ? { login_hint: p.login } : {}) } }
}

// The verification results of ETSI TS 119 478 as the QTSP names them, and
// the outcome a scenario expects for each.
const EXPECTED: Record<string, AsiExpected> = {
  Match: 'match',
  MatchWithVariation: 'match-with-variation',
  NoMatch: 'no-match',
  Unknown: 'unknown',
}

// expectedOf is the outcome a scenario saved from this request expects: the
// result of its first attribute, or retrieve. Undefined while the request
// has no accepted result.
export function expectedOf(v: AsiRequestView): AsiExpected | undefined {
  if (v.status !== 'done' || !v.seal?.valid || !v.results?.length) return undefined
  if (v.operation === 'retrieve') return 'retrieve'
  return EXPECTED[v.results[0].result ?? '']
}

export const EXPECTED_LABEL: Record<AsiExpected, string> = {
  match: 'MATCH',
  'match-with-variation': 'MATCH MET VARIATIE',
  'no-match': 'NO MATCH',
  unknown: 'UNKNOWN',
  retrieve: 'OPHALEN',
}

// traceOf is the trace whose chain belongs to this request: only the one the
// QTSP reports, never another ASI trace that happens to arrive meanwhile.
export function traceOf(v: AsiRequestView): string | null {
  return v.status === 'pending' ? null : v.trace_id ?? null
}

// summary is the line the result panel shows above the per-attribute table.
export function summary(v: AsiRequestView): string {
  switch (v.status) {
    case 'pending':
      return 'Wacht op de burger bij de autorisatieserver…'
    case 'denied':
      return `Geweigerd (${v.error}${v.error_description ? `: ${v.error_description}` : ''}). Er is niets bij de bron opgevraagd.`
    case 'failed':
      return v.problem ? `De ASIP gaf status ${v.asip_status}: ${v.problem}` : `Niet gelukt: ${v.error ?? 'onbekende fout'}`
    case 'done':
      return v.seal?.valid
        ? `Zegel geldig, verzegeld door ${v.seal.signer}.`
        : `Zegel ongeldig — resultaat niet geaccepteerd. ${v.seal?.error ?? ''}`.trim()
  }
}

// display is what the table shows for an attribute: the verification result,
// or the retrieved value as JSON.
export function display(r: NonNullable<AsiRequestView['results']>[number]): string {
  return r.result ?? JSON.stringify(r.value)
}
