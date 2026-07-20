import type { Scenario, HistoryRun, Citizen, Organization } from '../types'

const BASE = '/api/dev'

async function jsonGet<T>(path: string): Promise<T> {
  const res = await fetch(BASE + path)
  if (!res.ok) throw new Error(`GET ${path} failed: ${res.status}`)
  return res.json()
}

export async function listScenarios(): Promise<Scenario[]> { return jsonGet('/scenarios') }
export async function listCitizens(): Promise<Citizen[]> { return jsonGet('/citizens') }
export async function listOrganizations(): Promise<Organization[]> { return jsonGet('/organizations') }
export async function listHistory(): Promise<HistoryRun[]> { return jsonGet('/history') }

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
    body: JSON.stringify(run),
  })
  if (!res.ok) throw new Error(`logHistory failed: ${res.status}`)
  return res.json()
}

export type OpaExplainDecision = {
  decision_id?: string
  path?: string
  input?: Record<string, unknown>
  result?: Record<string, unknown>
  result_replay?: unknown
  explanation?: string[]
}

export async function fetchExplain(
  traceId: string,
  mode: 'none' | 'fails' | 'full' = 'none',
): Promise<OpaExplainDecision[]> {
  const q = mode === 'none' ? '' : `&mode=${mode}`
  const res = await fetch(`${BASE}/explain?trace_id=${encodeURIComponent(traceId)}${q}`)
  if (!res.ok) throw new Error(`fetchExplain failed: ${res.status}`)
  const body = (await res.json()) as { decisions: OpaExplainDecision[] }
  return body.decisions ?? []
}

export async function fetchPolicySource(id: string): Promise<{ id: string; raw: string }> {
  const res = await fetch(`${BASE}/policy-source?id=${encodeURIComponent(id)}`)
  if (!res.ok) throw new Error(`fetchPolicySource failed: ${res.status}`)
  return res.json()
}

export type PolicySnippet = { id: string; line: number; raw: string }

export async function fetchPolicySnippet(path: string, code: string): Promise<PolicySnippet | null> {
  const res = await fetch(`${BASE}/policy-snippet?path=${encodeURIComponent(path)}&code=${encodeURIComponent(code)}`)
  if (res.status === 404) return null
  if (!res.ok) throw new Error(`fetchPolicySnippet failed: ${res.status}`)
  return res.json()
}

// Rule-metadata as emitted by RFC0052-versie-GBO self-contained rules. The
// dev-portal-backend /rules endpoint flattens OPA's package map to a list,
// dropping the package-leaf key (each rule carries its own rule_id).
export type RuleMeta = {
  rule_id: string
  covers_types?: string[]
  covers_fields?: string[]
  spec: {
    rule_id: string
    consent_required?: boolean
    consent_must_cover_scope?: boolean
    consent_must_cover_fields?: boolean
    constraint_binding?: { arg: string; resource_field: string }[]
    pip?: unknown
  }
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
