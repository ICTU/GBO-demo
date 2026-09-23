// The ownership check: IR asks LVG, through its own FSC peer and the GBO PDP,
// whether the citizen of the consent owns the verblijfsobject. The answer is
// the same VBO-id (owner), null (not the owner), or a refusal before LVG was
// asked (no valid consent) — the three outcomes of the pilot's diagram.

export type Outcome = 'owner' | 'not-owner' | 'unconfirmed'

export type OwnershipResult = { outcome: Outcome; traceId?: string }

type QueryResponse = {
  allowed: boolean
  data?: { data?: { vbo?: { vboId?: string } | null }; errors?: unknown[] }
  trace_id?: string
}

// outcomeOf reads the backend's answer. Anything but an allowed answer that
// names the requested VBO-id or explicitly none is "unconfirmed": IR releases
// no data on an answer it cannot read.
export function outcomeOf(response: QueryResponse, vboId: string): Outcome {
  if (!response.allowed) return 'unconfirmed'
  // A null next to errors is a failed check, not LVG saying "not theirs".
  if (response.data?.errors?.length) return 'unconfirmed'
  const graph = response.data?.data
  if (!graph || !('vbo' in graph)) return 'unconfirmed'
  if (graph.vbo === null) return 'not-owner'
  return graph.vbo?.vboId === vboId ? 'owner' : 'unconfirmed'
}

export async function checkOwnership(consentToken: string, vboId: string): Promise<OwnershipResult> {
  try {
    const res = await fetch('/api/dvtp/query', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ consent_token: consentToken, vbo_id: vboId }),
    })
    const body = (await res.json()) as QueryResponse
    return { outcome: res.ok ? outcomeOf(body, vboId) : 'unconfirmed', traceId: body.trace_id }
  } catch {
    return { outcome: 'unconfirmed' }
  }
}
