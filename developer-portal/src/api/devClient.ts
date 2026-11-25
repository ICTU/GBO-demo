import { demoSessionId } from '../util/demoSession'
import type { Scenario, HistoryRun, Citizen, Organization } from '../types'
import { testPersonsFrom, testPidOfferFrom, type TestPersons, type TestPidOffer } from '../lib/testPid'
import {
  attestationOfferFrom, revocationOffersFrom, revokeEntryErrorFrom, revokeOutcomeFrom,
  type AttestationOffer, type RevocationOffers, type RevokeOutcome, type StatusListView,
} from '../lib/revocation'
import {
  lifecycleEventsFrom, lifecycleMessage, lifecycleSettingsFrom, rpCatalogFrom, rpEventsFrom, rpRequestFrom,
  type LifecycleEvent, type LifecycleSettings, type ReusePolicy, type RPCatalog, type RPEvent, type RPRequest,
} from '../lib/lifecycle'

const BASE = '/api/dev'

async function jsonGet<T>(path: string): Promise<T> {
  const res = await fetch(BASE + path)
  if (!res.ok) throw new Error(`GET ${path} failed: ${res.status}`)
  return res.json()
}

export async function listScenarios(): Promise<Scenario[]> { return jsonGet('/scenarios') }
export async function listCitizens(): Promise<Citizen[]> { return jsonGet('/citizens') }
export async function listOrganizations(): Promise<Organization[]> { return jsonGet('/organizations') }
// listHistory returns this session's runs plus every unattributable one. Pass
// no session to get the whole timeline, everybody's runs included.
export async function listHistory(session?: string): Promise<HistoryRun[]> {
  return jsonGet(session ? `/history?session=${encodeURIComponent(session)}` : '/history')
}

async function readJson(res: Response): Promise<unknown> {
  try {
    return await res.json()
  } catch {
    return undefined
  }
}

export async function loadTestPersons(): Promise<TestPersons> {
  try {
    const res = await fetch(`${BASE}/test-pid/persons`, { cache: 'no-store' })
    return testPersonsFrom(res.status, await readJson(res))
  } catch {
    return testPersonsFrom(null, undefined)
  }
}

export async function createTestPidOffer(bsn: string): Promise<TestPidOffer> {
  try {
    const res = await fetch(`${BASE}/test-pid/offers`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ bsn }),
    })
    return testPidOfferFrom(res.status, await readJson(res))
  } catch {
    return { error: 'De portal-backend is niet bereikbaar.' }
  }
}

async function postJson(path: string, body: unknown): Promise<{ status: number | null; body: unknown }> {
  try {
    const res = await fetch(BASE + path, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(body),
    })
    return { status: res.status, body: await readJson(res) }
  } catch {
    return { status: null, body: undefined }
  }
}

export async function loadRevocationOffers(): Promise<RevocationOffers> {
  try {
    const res = await fetch(`${BASE}/revocation/offers`, { cache: 'no-store' })
    return revocationOffersFrom(res.status, await readJson(res))
  } catch {
    return revocationOffersFrom(null, undefined)
  }
}

/** An offer of a source's attestation with a reference, for revocation. */
export async function createAttestationOffer(sourceId: string, key: string, bsn: string): Promise<AttestationOffer> {
  const answer = await postJson('/revocation/offers', { source_id: sourceId, key, bsn })
  return attestationOfferFrom(answer.status, answer.body)
}

export async function revokeOffer(reference: string): Promise<RevokeOutcome> {
  const answer = await postJson('/revocation/revoke', { reference })
  return revokeOutcomeFrom(answer.status, answer.body)
}

/** Revokes one status list entry; '' when it worked, else the message. */
export async function revokeEntry(provider: string, list: string, index: number): Promise<string> {
  const answer = await postJson('/revocation/revoke-entry', { provider, list, index })
  return revokeEntryErrorFrom(answer.status, answer.body)
}

async function sendJson(method: string, path: string, body?: unknown): Promise<{ status: number | null; body: unknown }> {
  try {
    const res = await fetch(BASE + path, {
      method,
      cache: 'no-store',
      headers: body === undefined ? undefined : { 'Content-Type': 'application/json' },
      body: body === undefined ? undefined : JSON.stringify(body),
    })
    return { status: res.status, body: await readJson(res) }
  } catch {
    return { status: null, body: undefined }
  }
}

export async function loadLifecycleSettings(): Promise<LifecycleSettings> {
  const answer = await sendJson('GET', '/credential-lifecycle/settings')
  return lifecycleSettingsFrom(answer.status, answer.body)
}

/** Changes or resets a batch setting; '' when the issuer took it, else why not. */
export async function changeLifecycleSetting(
  target: { provider: string } | { configuration: string },
  value: { batch_size: number } | { reuse_policy: ReusePolicy } | null,
): Promise<string> {
  const path = 'provider' in target
    ? `/credential-lifecycle/settings/providers/${encodeURIComponent(target.provider)}`
    : `/credential-lifecycle/settings/configurations/${encodeURIComponent(target.configuration)}`
  const answer = value === null ? await sendJson('DELETE', path) : await sendJson('PUT', path, value)
  return answer.status === 200 ? '' : lifecycleMessage(answer.status, answer.body)
}

export async function loadLifecycleEvents(): Promise<LifecycleEvent[] | { error: string }> {
  const answer = await sendJson('GET', '/credential-lifecycle/events')
  return lifecycleEventsFrom(answer.status, answer.body)
}

export async function loadRPCatalog(): Promise<RPCatalog> {
  const answer = await sendJson('GET', '/test-rp/catalog')
  return rpCatalogFrom(answer.status, answer.body)
}

/** Asks the test relying party for a presentation request. */
export async function requestPresentation(relyingParty: string, credential: string, claims: string[][]): Promise<RPRequest> {
  const answer = await sendJson('POST', '/test-rp/presentations', { relying_party: relyingParty, credential, claims })
  return rpRequestFrom(answer.status, answer.body)
}

export async function loadRPEvents(): Promise<RPEvent[] | { error: string }> {
  const answer = await sendJson('GET', '/test-rp/events')
  return rpEventsFrom(answer.status, answer.body)
}

export async function loadStatusLists(): Promise<StatusListView[] | { error: string }> {
  try {
    const res = await fetch(`${BASE}/revocation/status-lists`, { cache: 'no-store' })
    const body = await readJson(res)
    if (res.ok && Array.isArray(body)) return body as StatusListView[]
    return { error: `Status lists niet beschikbaar (HTTP ${res.status}).` }
  } catch {
    return { error: 'De portal-backend is niet bereikbaar.' }
  }
}

export async function saveScenario(s: Omit<Scenario, 'user_saved'>): Promise<Scenario> {
  const res = await fetch(`${BASE}/scenarios`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(s),
  })
  if (!res.ok) throw new Error(`saveScenario failed: ${res.status}`)
  return res.json()
}

export async function deleteScenario(id: string): Promise<void> {
  const res = await fetch(`${BASE}/scenarios/${encodeURIComponent(id)}`, { method: 'DELETE' })
  if (!res.ok) throw new Error(`deleteScenario failed: ${res.status}`)
}

export async function logHistory(run: Omit<HistoryRun, 'run_id' | 'ts'>): Promise<HistoryRun> {
  const res = await fetch(`${BASE}/history`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    // Stamp our own runs so they come back to this session's timeline and
    // stay out of everyone else's.
    body: JSON.stringify({ demo_session: demoSessionId(), ...run }),
  })
  if (!res.ok) throw new Error(`logHistory failed: ${res.status}`)
  return res.json()
}

// A record of the Authorization Decision Log: the audit record of a PDP
// decision. It holds the AuthZEN request and response, so the decision and
// the reason code of a denial; the engine transports nothing more.
export type AdlRecord = {
  timestamp: number
  trace_id: string
  span_id: string
  parent_span_id?: string
  event_name: string
  status: string
  policies: number
  fsc_transaction_id?: string
  // Which id found the record: adl.fsc.transaction_id, or the
  // Fsc-Transaction-Id header in the recorded request while the Inway does
  // not pass it to the PDP.
  matched_on: string
  decision?: boolean
  reason?: string
  request?: unknown
  response?: unknown
}

// An entry of the embedded OPA's console decision log (Loki), normalised:
// `result` is the policy's decision document, per-field detail included.
// Observability, not a record of the decision.
export type EngineDecision = {
  decision_id?: string
  path?: string
  input?: Record<string, unknown>
  result?: Record<string, unknown>
}

export type PdpDecisions = {
  audit: { records: AdlRecord[]; error?: string }
  engine: { decisions: EngineDecision[]; error?: string }
}

// fetchDecisions loads both parts, or one: `part: 'audit'` skips Loki, so a
// slow or unreachable Loki cannot delay the decision of record, and
// `part: 'engine'` skips the ADL.
export async function fetchDecisions(
  transactionId: string,
  opts: { part?: 'audit' | 'engine' } = {},
): Promise<PdpDecisions> {
  const part = opts.part ? `&part=${opts.part}` : ''
  const res = await fetch(`${BASE}/decisions?transaction_id=${encodeURIComponent(transactionId)}${part}`)
  if (!res.ok) throw new Error(`fetchDecisions failed: ${res.status}`)
  const body = (await res.json()) as Partial<PdpDecisions>
  return {
    audit: { records: body.audit?.records ?? [], error: body.audit?.error },
    engine: { decisions: body.engine?.decisions ?? [], error: body.engine?.error },
  }
}

export async function fetchPolicySource(id: string): Promise<{ id: string; raw: string }> {
  const res = await fetch(`${BASE}/policy-source?id=${encodeURIComponent(id)}`)
  if (!res.ok) throw new Error(`fetchPolicySource failed: ${res.status}`)
  return res.json()
}

export type PolicySnippet = { id: string; line: number; raw: string }

// With rule, the snippet comes from the file of the rule that refused: the
// same code appears in several rules.
export async function fetchPolicySnippet(path: string, code: string, rule?: string): Promise<PolicySnippet | null> {
  const ruleParam = rule ? `&rule=${encodeURIComponent(rule)}` : ''
  const res = await fetch(`${BASE}/policy-snippet?path=${encodeURIComponent(path)}&code=${encodeURIComponent(code)}${ruleParam}`)
  if (res.status === 404) return null
  if (!res.ok) throw new Error(`fetchPolicySnippet failed: ${res.status}`)
  return res.json()
}

// A rule as the dev-portal-backend /rules endpoint lists it: its metadata
// from rules.json and its Rego source, which says what it checks.
export type RuleMeta = {
  rule_id: string
  regime?: string
  package?: string
  covers_types?: string[]
  covers_fields?: string[]
  // The FSC services the rule binds on (a field key can be in two schemas).
  services?: string[]
  source_id?: string
  source?: string
}

export async function listRules(): Promise<RuleMeta[]> { return jsonGet('/rules') }

// FSC-txlog per transaction_id (fsc-logging conformant). The backend
// (dev-portal-backend) fetches the records from both FSC-orgs (edi-issuer
// + belastingdienst-mock) in parallel. Per peer it yields a record with
// peer-IDs, service, contract-hash and direction — enough to show per hop
// who sent/received.
export type FscTxlogRecord = {
  transaction_id: string
  group_id: string
  direction: 'DIRECTION_INCOMING' | 'DIRECTION_OUTGOING' | string
  grant_hash: string
  service_name: string
  source: { outway_peer_id?: string; type: string }
  destination: { service_peer_id?: string; type: string }
  created_at: number | string
}
export type FscTxlogPeer = {
  peer: string
  records: FscTxlogRecord[] | null
  error?: string
}
export type FscTxlogResponse = {
  transaction_id: string
  peers: FscTxlogPeer[]
  note?: string
}

export async function fetchFscTxlog(txID: string): Promise<FscTxlogResponse | null> {
  const res = await fetch(`${BASE}/fsc/txlog/${encodeURIComponent(txID)}`)
  if (!res.ok) return null
  return res.json()
}

// Logboek Dataverwerkingen per trace (Logius LDV v1.0.0). LDV keeps each
// Verantwoordelijke's records in that Verantwoordelijke's own logbook and
// lets only trace metadata cross a boundary, so the backend reads a chain the
// way the read extension says a reader does: from the logbooks where a
// processing starts, along dpl.read.nextLogbookId, each queried on the
// request's trace id. The FSC transaction log and the PDP decision key on the
// Fsc-Transaction-Id; for a request that crosses FSC once the two are the same
// value. This is the other two thirds of the "one trace id, three standards"
// picture.
// One dataProcessingOperation as the read extension returns it: camelCase
// names and RFC 3339 times, which differ from the write side's snake_case and
// epoch milliseconds. That is the standard's own split, not ours.
export type LdvRecord = {
  traceId: string
  spanId: string
  parentSpanId?: string
  name: string
  status: string
  startTime: string
  endTime: string
  resource?: { attributes: Record<string, unknown> }
  attributes: Record<string, unknown>
}

export type LdvLogbookResult = {
  logbook: { id: string; name: string }
  records: LdvRecord[]
  error?: string
  // How the chain view reached this logbook: the logbook of an application
  // that starts a processing, a nextLogbookId in another logbook's records,
  // or neither.
  reached_via: 'start' | 'pointer' | 'none'
  // The id of the logbook whose record pointed here.
  from?: string
}

export type LdvChainResponse = {
  trace_id: string
  logbooks: LdvLogbookResult[]
}

export async function fetchLdvChain(traceID: string): Promise<LdvChainResponse | null> {
  const res = await fetch(`${BASE}/ldv/${encodeURIComponent(traceID)}`)
  if (!res.ok) return null
  return res.json()
}
