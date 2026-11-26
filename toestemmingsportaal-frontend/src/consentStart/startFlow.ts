// The start of a consent flow from a pushed request. The provider sends the
// browser to /toestemming/start with only its client_id and the request_uri
// the consent portal backend gave it; everything the citizen sees comes
// from resolving that reference at the backend, and the backend decides
// where the browser returns to. Nothing about the request is kept in web
// storage: the start URL waits in a short-lived cookie while the citizen
// logs in, and the rest lives in React state.

import { requestedScopeGroups } from '../data/scopeGroups.ts'
import { PortalApiError } from '../api/portalClient.ts'

export const START_ROUTE = '/toestemming/start'

/** The login route of the start flow; DigiDAuth resumes from the cookie. */
export const START_LOGIN_ROUTE = '/auth?flow=start'

export const START_COOKIE = 'gbo_consent_start'

/** How long the start URL waits for the citizen to log in, in seconds. */
export const START_COOKIE_MAX_AGE = 5 * 60

/** How long the confirmation shows before the browser returns. */
export const AUTO_REDIRECT_MS = 5000

export type StartParams = { clientId: string; requestUri: string }

/**
 * The client_id and request_uri of a start URL, and nothing else: any other
 * parameter is ignored. Null when either is missing, empty or repeated.
 */
export function startParams(search: URLSearchParams | string): StartParams | null {
  const params = typeof search === 'string' ? new URLSearchParams(search) : search
  const clientId = params.getAll('client_id')
  const requestUri = params.getAll('request_uri')
  if (clientId.length !== 1 || requestUri.length !== 1) return null
  if (!clientId[0] || !requestUri[0]) return null
  return { clientId: clientId[0], requestUri: requestUri[0] }
}

/** The start URL with only client_id and request_uri, as a path. */
export function startPath(start: StartParams): string {
  const query = new URLSearchParams({ client_id: start.clientId, request_uri: start.requestUri })
  return `${START_ROUTE}?${query.toString()}`
}

function cookieAttributes(maxAge: number, secure: boolean): string {
  return `; Max-Age=${maxAge}; Path=/; SameSite=Lax${secure ? '; Secure' : ''}`
}

/** A Set-Cookie value (for document.cookie) holding the start URL. */
export function startCookie(start: StartParams, secure: boolean): string {
  return `${START_COOKIE}=${encodeURIComponent(startPath(start))}${cookieAttributes(START_COOKIE_MAX_AGE, secure)}`
}

/** A Set-Cookie value (for document.cookie) that removes the start URL. */
export function clearedStartCookie(secure: boolean): string {
  return `${START_COOKIE}=${cookieAttributes(0, secure)}`
}

/**
 * The start URL kept in the cookie header, rebuilt from its client_id and
 * request_uri so that it can only lead back to the start route. Null when
 * there is none or it is not a start URL.
 */
export function readStartCookie(cookieHeader: string): string | null {
  for (const part of cookieHeader.split(';')) {
    const [name, ...rest] = part.trim().split('=')
    if (name !== START_COOKIE) continue
    let value: string
    try {
      value = decodeURIComponent(rest.join('='))
    } catch {
      return null
    }
    const base = 'http://portal.invalid'
    if (!URL.canParse(value, base)) return null
    const url = new URL(value, base)
    if (url.origin !== base || url.pathname !== START_ROUTE) return null
    const start = startParams(url.searchParams)
    return start ? startPath(start) : null
  }
  return null
}

/**
 * Where the login sends the citizen in the start flow: back to the start URL
 * kept in the cookie or, when the cookie has expired (it lives five
 * minutes, the pushed request may live longer) or holds no start URL, to
 * the start route without parameters, which is the flow's error page.
 */
export function resumeAfterLogin(cookieHeader: string): string {
  return readStartCookie(cookieHeader) ?? START_ROUTE
}

/**
 * What the start page does first: the error page for a URL without a usable
 * client_id and request_uri, without a portal login the login (the start
 * URL waits in the cookie), and otherwise the resolve at the backend.
 */
export function startAction(start: StartParams | null, token: string | null | undefined): 'error' | 'login' | 'resolve' {
  if (!start) return 'error'
  if (!token) return 'login'
  return 'resolve'
}

/**
 * What follows a failed resolve or decision: when the backend no longer
 * accepts the portal login (401), the citizen logs in again and resumes;
 * any other failure is the error page.
 */
export function afterFailure(err: unknown): 'relogin' | 'failed' {
  return err instanceof PortalApiError && err.status === 401 ? 'relogin' : 'failed'
}

/**
 * The data sets a grant consents to: those of the request the citizen left
 * checked (all are checked at first), in the order the screen shows them.
 */
export function chosenScopes(requested: string[], picked: Record<string, boolean>): string[] {
  return requestedScopeGroups(requested).groups.filter((s) => picked[s.code] ?? true).map((s) => s.code)
}

/**
 * Whether "Geef toestemming" can be pressed: at least one data set is
 * chosen, the citizen confirmed having seen who asks, the purpose, the data
 * and the validity, and no decision is under way. Consent must be informed,
 * and the backend refuses a grant without the confirmation.
 */
export function canGrant(chosen: string[], informationConfirmed: boolean, deciding: boolean): boolean {
  return chosen.length > 0 && informationConfirmed && !deciding
}

/** A resolved reference, as GET /portal/requests/{request_uri} returns it. */
export type ConsentRequest = {
  client_id: string
  client_name: string
  service: string
  purpose: string
  scopes: string[]
  valid_until?: string
}

export type Outcome = 'granted' | 'denied' | 'failed'

/** The decision's answer, checked: where the browser returns and why. */
export type Confirmation = { outcome: Outcome; clientName: string; redirectTo: string }

const OUTCOMES: readonly string[] = ['granted', 'denied', 'failed']

/**
 * The confirmation for the backend's answer to a decision. redirectTo is the
 * answer's redirect_to as it is: the frontend adds nothing and changes
 * nothing. Null when the answer has no outcome or no absolute http(s) return
 * URL; the citizen then sees the error page.
 */
export function confirmation(answer: unknown): Confirmation | null {
  if (typeof answer !== 'object' || answer === null) return null
  const { outcome, client_name, redirect_to } = answer as Record<string, unknown>
  if (typeof outcome !== 'string' || !OUTCOMES.includes(outcome)) return null
  if (typeof redirect_to !== 'string' || !URL.canParse(redirect_to)) return null
  const { protocol } = new URL(redirect_to)
  if (protocol !== 'https:' && protocol !== 'http:') return null
  return {
    outcome: outcome as Outcome,
    clientName: typeof client_name === 'string' ? client_name : '',
    redirectTo: redirect_to,
  }
}

/** Where the start page is. The consent screen needs a resolved request. */
export type Phase =
  | { kind: 'resolving' }
  | { kind: 'consent'; request: ConsentRequest }
  | { kind: 'deciding'; request: ConsentRequest }
  | { kind: 'confirmed'; confirmation: Confirmation }
  | { kind: 'error' }

export type FlowEvent =
  | { type: 'resolved'; request: ConsentRequest }
  | { type: 'deciding' }
  | { type: 'decided'; answer: unknown }
  | { type: 'failed' }

export const INITIAL_PHASE: Phase = { kind: 'resolving' }

/**
 * The start page's state machine. The consent screen follows only a
 * successful resolve of data sets the portal can describe; a decision
 * leads to the confirmation, never straight to the return URL; every
 * failure leads to the one error page.
 */
export function nextPhase(phase: Phase, event: FlowEvent): Phase {
  switch (event.type) {
    case 'resolved': {
      if (phase.kind !== 'resolving') return phase
      const { groups, unknown } = requestedScopeGroups(event.request.scopes ?? [])
      if (groups.length === 0 || unknown.length > 0) return { kind: 'error' }
      return { kind: 'consent', request: event.request }
    }
    case 'deciding':
      return phase.kind === 'consent' ? { kind: 'deciding', request: phase.request } : phase
    case 'decided': {
      if (phase.kind !== 'deciding') return phase
      const confirmed = confirmation(event.answer)
      return confirmed ? { kind: 'confirmed', confirmation: confirmed } : { kind: 'error' }
    }
    case 'failed':
      return phase.kind === 'confirmed' ? phase : { kind: 'error' }
  }
}
