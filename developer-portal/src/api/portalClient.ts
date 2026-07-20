import type { IssuancePayload, IssuanceResponse } from '../types'

const BASE = '/portal-api'

export async function issuanceFlow(
  payload: IssuancePayload,
  traceparent?: string,
): Promise<IssuanceResponse> {
  const tpHeader: Record<string, string> = traceparent ? { traceparent } : {}
  // X-Demo-Source tells the backend that the dev-portal is the trigger,
  // so it skips its own history-post (the frontend already logs).
  const sourceHeader = { 'X-Demo-Source': 'dev-portal' }

  const loginRes = await fetch(`${BASE}/portal/login`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json', ...tpHeader, ...sourceHeader },
    body: JSON.stringify({ citizen_bsn: payload.citizen_bsn }),
  })
  if (!loginRes.ok) {
    const t = await loginRes.text()
    throw new Error(`portal/login ${loginRes.status}: ${t}`)
  }
  const { token } = (await loginRes.json()) as { token: string }

  const consentRes = await fetch(`${BASE}/portal/consents`, {
    method: 'POST',
    headers: {
      'Content-Type': 'application/json',
      Authorization: `Bearer ${token}`,
      ...tpHeader,
      ...sourceHeader,
    },
    body: JSON.stringify({
      dienstverlener_oin: payload.dienstverlener_oin,
      scopes: payload.scopes,
      validity_seconds: payload.validity_seconds ?? 7776000,
    }),
  })
  if (!consentRes.ok) {
    const t = await consentRes.text()
    throw new Error(`portal/consents ${consentRes.status}: ${t}`)
  }
  return consentRes.json()
}
