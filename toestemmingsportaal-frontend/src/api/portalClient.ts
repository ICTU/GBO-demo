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
}

export type CreateConsentResponse = {
  consent_id: string
  pseudonym: string
  pi: string
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
  pi: string
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
    headers: { Authorization: `Bearer ${token}` },
  })
  if (!res.ok) {
    const text = await res.text()
    throw new Error(`revokeConsent failed (${res.status}): ${text}`)
  }
}
