import { demoSessionHeader } from '../util/demoSession'
import type { UsePayload, UseResponse } from '../types'

const BASE = '/dvtp-api'

export async function useQuery(payload: UsePayload, traceparent?: string): Promise<UseResponse> {
  const tpHeader: Record<string, string> = traceparent ? { traceparent } : {}
  const res = await fetch(`${BASE}/api/dvtp/query`, {
    method: 'POST',
    headers: {
      'Content-Type': 'application/json',
      'X-Demo-Source': 'dev-portal',
      // Tag our own runs too, so a colleague's watch-mode leaves them alone.
      ...demoSessionHeader(),
      ...tpHeader,
    },
    body: JSON.stringify({
      consent_token: payload.consent_token,
      scope_id: payload.scope_id,
      belastingjaren: payload.belastingjaren,
      fields: payload.fields,
    }),
  })
  if (!res.ok) {
    const t = await res.text()
    throw new Error(`dvtp/query ${res.status}: ${t}`)
  }
  return res.json()
}
