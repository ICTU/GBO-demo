// The redirect to MijnOverheid (the GBO toestemmingsportaal) and back. The
// citizen gives the Installatie Register consent to ask LVG whether the
// building is theirs; the portal returns the signed consent token in the URL
// fragment, which IR keeps.

declare global {
  interface Window {
    __GBO_CONFIG__?: {
      consentPortalUrl?: string
      consumerPeerId?: string
    }
  }
}

// The Installatie Register's own FSC peer: the PDP binds the consent to it.
export const DEFAULT_IR_PEER_ID = '99999999900000001000'

export const LVG_SCOPE = 'lvg:vbo:eigendom'

export function consumerPeerId(): string {
  return window.__GBO_CONFIG__?.consumerPeerId?.trim() || import.meta.env.VITE_CONSUMER_PEER_ID || DEFAULT_IR_PEER_ID
}

// Configured explicitly: unlike the dienstverlener-mock, the portal is not
// one port up from this app.
export function portalBase(): string {
  return (
    window.__GBO_CONFIG__?.consentPortalUrl?.trim() ||
    import.meta.env.VITE_CONSENT_PORTAL_URL ||
    'http://localhost:9002'
  ).replace(/\/$/, '')
}

export function consentRequestUrl(returnUrl: string, now = new Date()): string {
  const validUntil = new Date(now)
  validUntil.setFullYear(now.getFullYear() + 1)
  const params = new URLSearchParams({
    service: 'installatieregister',
    purpose: 'Goedkeuring installatiegegevens',
    scope: LVG_SCOPE,
    client_oin: consumerPeerId(),
    client_name: 'Installatie Register',
    valid_until: validUntil.toISOString(),
    return_url: returnUrl,
  })
  return `${portalBase()}/auth?${params.toString()}`
}

export function myConsentsUrl(): string {
  return `${portalBase()}/mijnoverheid/toestemmingen`
}

export type PortalReturn =
  | { status: 'ok'; consentId: string; consentToken: string }
  | { status: 'denied' }
  | { status: 'invalid' }

// readPortalReturn parses what the portal appended to the return URL: the
// status and consent id in the query, the token in the fragment.
export function readPortalReturn(search: string, hash: string): PortalReturn {
  const query = new URLSearchParams(search)
  if (query.get('status') === 'denied') return { status: 'denied' }
  const consentId = query.get('consent_id') ?? ''
  const consentToken = new URLSearchParams(hash.replace(/^#/, '')).get('consent_token') ?? ''
  if (query.get('status') !== 'ok' || !consentId || !consentToken) return { status: 'invalid' }
  return { status: 'ok', consentId, consentToken }
}
