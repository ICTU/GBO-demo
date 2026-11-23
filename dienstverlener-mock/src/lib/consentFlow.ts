// The provider's side of a consent request, through this app's backend
// (/api, same origin). The backend pushes the request to the consent portal,
// keeps the PKCE verifier and the state, exchanges the one-time code and
// keeps the consent token in its session (an HttpOnly cookie). The browser
// only carries the portal's address there and the code back: nothing of the
// request, the code or the token is kept in browser storage.

export type ConsentRequest = {
  service: string
  scopes: string[]
  valid_until?: string
}

export type PortalReturn =
  | { code: string; state: string }
  | { error: string; state: string }

export type Outcome = 'granted' | 'denied' | 'failed'

export type Completion = { outcome: Outcome; consent_id?: string; scopes?: string[] }

type Fetch = typeof fetch

export class ConsentFlowError extends Error {
  readonly status: number
  readonly code: string

  constructor(status: number, code: string) {
    super(`consent flow failed (${status}${code ? `: ${code}` : ''})`)
    this.status = status
    this.code = code
  }
}

async function postJSON(fetchImpl: Fetch, path: string, body: unknown): Promise<unknown> {
  const res = await fetchImpl(path, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(body),
    credentials: 'same-origin',
  })
  const answer = (await res.json().catch(() => ({}))) as Record<string, unknown>
  if (!res.ok) {
    throw new ConsentFlowError(res.status, typeof answer.error === 'string' ? answer.error : '')
  }
  return answer
}

// portalAddress accepts only an absolute http(s) URL: the browser goes there
// next, so a broken answer must not become a javascript: URL.
export function portalAddress(value: unknown): string | null {
  if (typeof value !== 'string') return null
  try {
    const url = new URL(value)
    return url.protocol === 'https:' || url.protocol === 'http:' ? url.toString() : null
  } catch {
    return null
  }
}

// startConsent pushes the request and answers with the portal address the
// browser goes to.
export async function startConsent(request: ConsentRequest, fetchImpl: Fetch = fetch): Promise<string> {
  const answer = (await postJSON(fetchImpl, '/api/consent/start', request)) as { redirect_to?: unknown }
  const to = portalAddress(answer.redirect_to)
  if (!to) throw new ConsentFlowError(502, 'invalid_redirect')
  return to
}

// readPortalReturn reads what the portal put on the return address: a code
// and the state, or an error and the state. Anything else is null.
export function readPortalReturn(search: string): PortalReturn | null {
  const query = new URLSearchParams(search)
  const state = query.get('state') ?? ''
  const code = query.get('code') ?? ''
  const error = query.get('error') ?? ''
  if (!state) return null
  if (code && !error) return { code, state }
  if (error && !code) return { error, state }
  return null
}

// completeConsent hands the return to the backend, which checks the state
// and exchanges a code for the consent token. The answer never carries the
// token.
export async function completeConsent(back: PortalReturn, fetchImpl: Fetch = fetch): Promise<Completion> {
  const answer = (await postJSON(fetchImpl, '/api/consent/complete', back)) as Partial<Completion>
  const outcome: Outcome =
    answer.outcome === 'granted' || answer.outcome === 'denied' ? answer.outcome : 'failed'
  return { outcome, consent_id: answer.consent_id, scopes: answer.scopes }
}
