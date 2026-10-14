// The reference QTSP's request API, through the dev-portal backend (asi.go).
import type { AsiAttribute, AsiRequestBody, AsiRequestView } from '../lib/asiRequest'

const BASE = '/api/dev/asi'

// reason reads the reason the QTSP gives for a refusal (an RFC 9457 problem)
// or the backend's error.
async function reason(res: Response): Promise<string> {
  try {
    const body = await res.json()
    return body.detail ?? body.error ?? `HTTP ${res.status}`
  } catch {
    return `HTTP ${res.status}`
  }
}

export async function listAsiAttributes(): Promise<AsiAttribute[]> {
  const res = await fetch(`${BASE}/attributes`)
  if (!res.ok) throw new Error(await reason(res))
  return res.json()
}

export async function createAsiRequest(body: AsiRequestBody): Promise<AsiRequestView> {
  const res = await fetch(`${BASE}/requests`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(body),
  })
  if (!res.ok) throw new Error(await reason(res))
  return res.json()
}

export async function getAsiRequest(id: string): Promise<AsiRequestView> {
  const res = await fetch(`${BASE}/requests/${encodeURIComponent(id)}`)
  if (!res.ok) throw new Error(await reason(res))
  return res.json()
}
