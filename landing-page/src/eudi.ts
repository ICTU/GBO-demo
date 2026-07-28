import { eudi } from './config'

/* De wallet-link wordt volledig client-side samengesteld: bij het scannen
   POST de wallet naar request_uri en opent de issuance-server daar zelf
   een sessie. Deze pagina hoeft dus niets voor te bereiden en heeft geen
   backend nodig.

   Zelfde constructie als in developer-portal/src/components/EudiForm.tsx
   (walletUniversalLinkFor). Bewust gedupliceerd: de frontends in deze repo
   staan los van elkaar, met een eigen package.json en zonder gedeeld
   pakket. Wijzigt het linkformaat, dan moeten beide mee. */

/* Eén usecase op de landingspagina: het inkomensjaar dat het beleid
   toestaat. De dev-portal heeft de varianten (2023 verwacht een DENY);
   hier telt alleen dat de bezoeker een werkende verklaring krijgt. */
export const USECASE = 'inkomensverklaring_2024'

export type SessionType = 'same_device' | 'cross_device'

export function walletUniversalLink(sessionType: SessionType): string {
  if (!eudi.publicUrl) return ''
  const base = eudi.publicUrl.replace(/\/$/, '')
  const params = new URLSearchParams({
    request_uri: `${base}/disclosure/${USECASE}/request_uri?session_type=${sessionType}`,
    request_uri_method: 'post',
    client_id: eudi.clientId,
  })
  return `${eudi.ulBase}?${params.toString()}`
}
