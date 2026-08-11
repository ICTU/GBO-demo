import { eudi } from './config'

/* De wallet-link wordt volledig client-side samengesteld: bij het scannen
   POST de wallet naar request_uri en opent de issuance-server daar zelf
   een sessie. Deze pagina hoeft dus niets voor te bereiden en heeft geen
   backend nodig.

   Zelfde constructie als in developer-portal/src/components/EudiForm.tsx
   (walletUniversalLinkFor). Bewust gedupliceerd: de frontends in deze repo
   staan los van elkaar, met een eigen package.json en zonder gedeeld
   pakket. Wijzigt het linkformaat, dan moeten beide mee. */

export type WalletUsecase = {
  /* Wordt door onboarding gegenereerd uit offers[] van de bron en komt
     overeen met [disclosure_settings.<key>] in issuance_server.toml. */
  key: string
  label: string
  description?: string
  attestation_type: string
  source_oin: string
  type_id: string
  parameters: Record<string, string | number | boolean>
}

export async function loadWalletUsecases(): Promise<WalletUsecase[]> {
  const response = await fetch('/eudi-offers.json', { cache: 'no-store' })
  if (!response.ok) throw new Error(`issuance-aanbod is niet beschikbaar (HTTP ${response.status})`)
  const offers = (await response.json()) as WalletUsecase[]
  if (!Array.isArray(offers) || offers.length === 0) {
    throw new Error('issuance-aanbod bevat geen producten')
  }
  return offers
}

export type SessionType = 'same_device' | 'cross_device'

export function walletUniversalLink(usecase: string, sessionType: SessionType): string {
  if (!eudi.publicUrl) return ''
  const base = eudi.publicUrl.replace(/\/$/, '')
  const params = new URLSearchParams({
    request_uri: `${base}/disclosure/${usecase}/request_uri?session_type=${sessionType}`,
    request_uri_method: 'post',
    client_id: eudi.clientId,
  })
  return `${eudi.ulBase}?${params.toString()}`
}
