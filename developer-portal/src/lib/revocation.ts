/* The revocation block: the offers the portal had the own credential issuer
   make carry a reference, by which the issuer revokes the credentials it
   issued for them. The block lists those offers with a revoke button, has a
   form to revoke a single status list entry, and shows the public status
   lists of the offers. The backend has no revocation routes unless the
   deployment turns them on; then the block is not shown. */

export type RevocationOffer = {
  reference: string
  kind: 'test_pid' | 'attestation'
  label: string
  bsn: string
  provider: string
  list: string
  offered_at: string
  revoked_at?: string
  revoked_entries: number
}

export type RevocationOffers =
  | { enabled: false }
  | { enabled: true; offers: RevocationOffer[]; error: string }

export type StatusListView = {
  provider: string
  list: string
  uri: string
  offers: number
  iat?: string
  exp?: string
  ttl?: number
  bits?: number
  size?: number
  revoked: number[]
  error?: string
}

export type AttestationOffer = { link: string; reference: string } | { error: string }

export type RevokeOutcome = { offer: RevocationOffer } | { error: string }

const MESSAGES: Record<string, string> = {
  invalid_request: 'Het verzoek is onvolledig: vul aanbieder, lijst en een plek van 0 of hoger in.',
  unknown_offer: 'Dit aanbod publiceert de bron niet (meer).',
  unknown_person: 'Dit BSN is geen testpersoon. Kies een BSN uit de testpersonen, zoals bij de test-PID.',
  unknown_reference: 'Dit kenmerk komt niet van dit portal, of het portal is sindsdien herstart.',
  unknown_entry: 'De issuer kent deze plek niet: de lijst bestaat niet, of de plek is nooit uitgegeven.',
  offer_not_configured: 'De credential issuer biedt dit aanbod niet aan op het eudi-profiel.',
  catalog_unavailable: 'De lijst met aanbiedingen van de bronnen (eudi-adapter) is niet bereikbaar.',
  no_data: 'De bron kent dit BSN niet; de issuer maakte geen offer.',
  source_unavailable: 'De bron is niet bereikbaar voor de credential issuer.',
  bad_source_data: 'De bron gaf gegevens die de credential issuer niet kon gebruiken.',
  issuer_unavailable: 'De credential issuer is niet bereikbaar. Draait het compose-profiel credential-issuer?',
  issuer_error: 'De credential issuer gaf een onverwacht antwoord.',
}

export function revocationMessage(code: string): string {
  return MESSAGES[code] ?? `Mislukt (${code}).`
}

function errorFrom(status: number | null, body: unknown): string {
  const code = (body as { error?: unknown } | undefined)?.error
  if (typeof code === 'string') return revocationMessage(code)
  return status === null ? 'De portal-backend is niet bereikbaar.' : revocationMessage(`HTTP ${status}`)
}

/** The offers answer; status null is a request that got no answer. */
export function revocationOffersFrom(status: number | null, body: unknown): RevocationOffers {
  if (status === 404) return { enabled: false }
  if (status === 200 && Array.isArray(body)) return { enabled: true, offers: body as RevocationOffer[], error: '' }
  return { enabled: true, offers: [], error: 'De aanbiedingen van het portal zijn niet beschikbaar.' }
}

export function attestationOfferFrom(status: number | null, body: unknown): AttestationOffer {
  const answer = (body ?? {}) as { link?: unknown; offer?: { reference?: unknown } }
  if (status === 201) {
    const reference = answer.offer?.reference
    return typeof answer.link === 'string' && answer.link && typeof reference === 'string'
      ? { link: answer.link, reference }
      : { error: revocationMessage('issuer_error') }
  }
  return { error: errorFrom(status, body) }
}

export function revokeOutcomeFrom(status: number | null, body: unknown): RevokeOutcome {
  const offer = (body as { offer?: RevocationOffer } | undefined)?.offer
  if (status === 200 && offer) return { offer }
  return { error: errorFrom(status, body) }
}

/** The answer to a single entry's revocation: '' when it worked. */
export function revokeEntryErrorFrom(status: number | null, body: unknown): string {
  return status === 204 ? '' : errorFrom(status, body)
}

/** What a revoked offer reports: how many entries, or why none. */
export function revokedSummary(offer: RevocationOffer): string {
  if (!offer.revoked_at) return 'geldig'
  if (offer.revoked_entries === 0) return 'ingetrokken; de wallet had het aanbod nog niet opgehaald, er wordt niets meer voor uitgegeven'
  return `ingetrokken: ${offer.revoked_entries} ${offer.revoked_entries === 1 ? 'plek' : 'plekken'}`
}

/** The question before a revocation, which cannot be undone. */
export function confirmRevocation(offer: RevocationOffer): string {
  return `${offer.label} voor BSN ${offer.bsn} intrekken (kenmerk ${offer.reference})?\n\n` +
    'Intrekken is definitief en niet terug te draaien. Alle kopieën die de wallet voor dit aanbod kreeg, worden ingetrokken; ' +
    'een relying party die de status list bewaart, ziet het pas na de ttl van de lijst.'
}

/** Parses the entry form; a message when it is not a valid entry. */
export function entryFrom(provider: string, list: string, index: string):
  | { provider: string; list: string; index: number }
  | { error: string } {
  const n = Number(index)
  if (!provider.trim() || !list.trim() || index.trim() === '' || !Number.isInteger(n) || n < 0) {
    return { error: revocationMessage('invalid_request') }
  }
  return { provider: provider.trim(), list: list.trim(), index: n }
}

/** The event the offer blocks send, so the revocation block reloads. */
export const OFFERS_CHANGED = 'gbo-revocation-offers-changed'
