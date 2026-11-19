import { demoSessionHeader } from './demoSession'

const BASE = '/portal-api'

export type LoginResponse = { token: string }

export async function login(citizen_bsn: string): Promise<LoginResponse> {
  const res = await fetch(`${BASE}/portal/login`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ citizen_bsn }),
  })
  if (!res.ok) {
    const text = await res.text()
    throw new Error(`login failed (${res.status}): ${text}`)
  }
  return res.json()
}

export type CreateConsentRequest = {
  dienstverlener_oin: string
  scopes: string[]
  validity_seconds?: number
  use_case?: string
}

export type CreateConsentResponse = {
  consent_id: string
  consent_token: string
}

export async function createConsent(
  token: string,
  body: CreateConsentRequest,
): Promise<CreateConsentResponse> {
  const res = await fetch(`${BASE}/portal/consents`, {
    method: 'POST',
    headers: {
      'Content-Type': 'application/json',
      Authorization: `Bearer ${token}`,
      ...demoSessionHeader(),
    },
    body: JSON.stringify(body),
  })
  if (!res.ok) {
    const text = await res.text()
    throw new Error(`createConsent failed (${res.status}): ${text}`)
  }
  return res.json()
}

export type ConsentRecord = {
  consent_id: string
  status: string
  effective_status: 'active' | 'expired' | 'revoked'
  subject_ref: string
  dienstverlener_oin: string
  scopes: string[]
  scope_entries?: Array<{ bronhouder: string; scope_id: string; consented_fields: string[] }>
  use_case: string
  created_at: string
  valid_until: string
}

export async function listConsents(token: string): Promise<ConsentRecord[]> {
  const res = await fetch(`${BASE}/portal/consents`, {
    headers: { Authorization: `Bearer ${token}` },
  })
  if (!res.ok) {
    const text = await res.text()
    throw new Error(`listConsents failed (${res.status}): ${text}`)
  }
  return res.json()
}

export async function revokeConsent(token: string, consentId: string): Promise<void> {
  const res = await fetch(`${BASE}/portal/consents/${encodeURIComponent(consentId)}`, {
    method: 'DELETE',
    headers: { Authorization: `Bearer ${token}`, ...demoSessionHeader() },
  })
  if (!res.ok) {
    const text = await res.text()
    throw new Error(`revokeConsent failed (${res.status}): ${text}`)
  }
}

// One moment a consent was used: a source delivered data under it to the
// service provider it was given to.
export type ConsentUse = {
  used_at: string
  service_provider_oin: string
  // Ready to show: the register names the service provider and the source.
  service_provider: string
  source: string
}

// What the citizen is told when the uses cannot all be shown. index_unavailable:
// the data usage index could not be reached, so the list is not known.
export type UsageNotice = {
  kind: string
  source?: string
}

export type ConsentUsage = {
  uses: ConsentUse[]
  notices: UsageNotice[]
}

export async function getConsentUses(token: string, consentId: string): Promise<ConsentUsage> {
  const res = await fetch(`${BASE}/portal/consents/${encodeURIComponent(consentId)}/uses`, {
    headers: { Authorization: `Bearer ${token}`, ...demoSessionHeader() },
  })
  if (!res.ok) {
    const text = await res.text()
    throw new Error(`getConsentUses failed (${res.status}): ${text}`)
  }
  return res.json()
}
