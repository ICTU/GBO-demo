/* The fixed links of the own credential issuer's offers (GET
   /disclosure-offers, #614): per offer the EU-wallet link that starts with a
   PID disclosure, and the login link of the authorization code flow, where
   the citizen logs in instead. */

export type DisclosureOfferLinks = {
  provider: string
  key: string
  profiles: Record<string, { link?: string; login_link?: string }>
}

export type EuWalletLink = { link: string; loginLink: string }

/** EU-wallet links by source and offer key. */
export type EuWalletLinks = Map<string, EuWalletLink>

export function euWalletLinksFrom(offers: unknown): EuWalletLinks {
  const links: EuWalletLinks = new Map()
  for (const offer of Array.isArray(offers) ? (offers as DisclosureOfferLinks[]) : []) {
    const eudi = offer.profiles?.eudi
    if (eudi?.link) links.set(`${offer.provider}/${offer.key}`, { link: eudi.link, loginLink: eudi.login_link ?? '' })
  }
  return links
}
