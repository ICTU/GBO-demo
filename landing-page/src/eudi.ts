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
  /* Moet overeenkomen met [disclosure_settings.<key>] in
     issuance-server.toml én met de sleutel in de adapter-catalogus. */
  key: string
  label: string
  desc: string
  /* Welke PID werkt. Verschilt per credential, en dat is precies waar een
     bezoeker anders op stukloopt: de fout valt pas aan het eind. */
  scanNote: string
}

/* De dev-portal heeft meer varianten (inkomensverklaring 2023 verwacht een
   DENY); hier staan alleen de usecases die een bezoeker een werkende
   credential opleveren. */
export const WALLET_USECASES: WalletUsecase[] = [
  {
    key: 'inkomensverklaring_2024',
    label: 'Inkomensverklaring',
    desc: 'De issuer haalt je inkomensgegevens bij de bron op en zet er een inkomensverklaring van in je wallet. Die deel je daarna zelf, zonder dat de bron opnieuw bevraagd wordt.',
    scanNote:
      'De demo-bron kent een paar test-BSN’s; zit er een andere BSN in je wallet-PID, dan loopt de uitgifte aan het eind vast.',
  },
  {
    key: 'akte_van_overlijden',
    label: 'Akte van overlijden',
    desc: 'Als nabestaande deel je je PID en krijg je de akte van overlijden van je partner terug, uit de BRP. Je krijgt alleen wat op een akte hoort: de overledene, het overlijden, de ouders en de partner.',
    scanNote:
      'Werkt alleen met de PID van Frouke Jansen (BSN 999991772). De andere personen in de demo-BRP zijn ongehuwd, gescheiden, of hebben een nog levende partner.',
  },
]

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
