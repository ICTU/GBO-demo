import { demoSessionHeader } from './demoSession.ts'
import type { ConsentRequest } from '../consentStart/startFlow.ts'

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
  if (!res.ok) throw await portalApiError('revokeConsent', res)
}

/** The reason the portal gives for a consent that is already revoked or expired. */
export const CONSENT_NOT_REVOCABLE = 'CONSENT_NOT_REVOCABLE'

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

/** A refused call to the portal backend, with its status and reason code. */
export class PortalApiError extends Error {
  readonly status: number
  readonly reason?: string

  constructor(status: number, message: string, reason?: string) {
    super(message)
    this.status = status
    this.reason = reason
  }
}

async function portalApiError(op: string, res: Response): Promise<PortalApiError> {
  const text = await res.text()
  let reason: string | undefined
  try {
    const body = JSON.parse(text)
    if (typeof body?.reason === 'string') reason = body.reason
  } catch {
    // Not JSON: the text is the message.
  }
  return new PortalApiError(res.status, `${op} failed (${res.status}): ${text}`, reason)
}

/**
 * Resolves a pushed request for the logged-in citizen:
 * GET /portal/requests/{request_uri}?client_id=…. The backend binds the
 * reference to the citizen and returns what the registration says.
 */
export async function resolveRequest(
  token: string,
  requestUri: string,
  clientId: string,
): Promise<ConsentRequest> {
  const query = new URLSearchParams({ client_id: clientId })
  const res = await fetch(`${BASE}/portal/requests/${encodeURIComponent(requestUri)}?${query}`, {
    headers: { Authorization: `Bearer ${token}` },
  })
  if (!res.ok) throw await portalApiError('resolveRequest', res)
  return res.json()
}

export type DecisionBody = { decision: 'grant'; scopes: string[] } | { decision: 'deny' }

/**
 * Records the citizen's decision on a pushed request:
 * POST /portal/requests/{request_uri}/decision. The answer says how it ended
 * and where the browser goes; it never carries the consent token.
 */
export async function decideRequest(token: string, requestUri: string, body: DecisionBody): Promise<unknown> {
  const res = await fetch(`${BASE}/portal/requests/${encodeURIComponent(requestUri)}/decision`, {
    method: 'POST',
    headers: {
      'Content-Type': 'application/json',
      Authorization: `Bearer ${token}`,
      ...demoSessionHeader(),
    },
    body: JSON.stringify(body),
  })
  if (!res.ok) throw await portalApiError('decideRequest', res)
  return res.json()
}
