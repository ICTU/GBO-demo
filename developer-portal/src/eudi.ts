export type IssuanceOffer = {
  key: string
  label: string
  description?: string
  attestation_type: string
  source_id: string
  source_oin: string
  type_id: string
  parameters: Record<string, string | number | boolean>
}

const ISSUANCE_SERVER_PUBLIC_URL = (
  window.__GBO_RUNTIME_CONFIG__?.eudiPublicUrl ||
  import.meta.env.VITE_EUDI_PUBLIC_URL ||
  ''
).replace(/\/$/, '')

function readerClientId(publicUrl: string): string {
  try {
    const hostname = new URL(publicUrl).hostname
    if (hostname) return `x509_san_dns:${hostname}`
  } catch {
    // No QR can be generated without a valid public issuance URL.
  }
  return ''
}

const UL_BASE =
  import.meta.env.VITE_EUDI_UL_BASE ??
  'https://app.preproductie.wallet.edi.bzk.nl/deeplink/disclosure_based_issuance'

export async function loadIssuanceOffers(): Promise<IssuanceOffer[]> {
  const response = await fetch('/eudi-offers.json', { cache: 'no-store' })
  if (!response.ok) throw new Error(`issuance-aanbod is niet beschikbaar (HTTP ${response.status})`)
  const offers = (await response.json()) as IssuanceOffer[]
  if (!Array.isArray(offers) || offers.length === 0) {
    throw new Error('issuance-aanbod bevat geen producten')
  }
  return offers
}

export function walletUniversalLinkFor(offer: IssuanceOffer, sessionType: 'same_device' | 'cross_device'): string {
  if (!ISSUANCE_SERVER_PUBLIC_URL) return ''
  const requestUri = `${ISSUANCE_SERVER_PUBLIC_URL}/disclosure/${offer.key}/request_uri?session_type=${sessionType}`
  const params = new URLSearchParams({
    request_uri: requestUri,
    request_uri_method: 'post',
    client_id: readerClientId(ISSUANCE_SERVER_PUBLIC_URL),
  })
  return `${UL_BASE}?${params.toString()}`
}

export function adapterPathFor(offer: IssuanceOffer): string {
  const query = new URLSearchParams()
  for (const [name, value] of Object.entries(offer.parameters)) query.set(name, String(value))
  const suffix = query.size > 0 ? `?${query.toString()}` : ''
  return `/eudi-api/attestations/${encodeURIComponent(offer.source_id)}/${encodeURIComponent(offer.type_id)}${suffix}`
}
