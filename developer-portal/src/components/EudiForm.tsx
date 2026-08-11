import type { Citizen, EudiPayload } from '../types'

type Props = {
  payload: EudiPayload
  setPayload: (p: EudiPayload) => void
  citizens: Citizen[]
}

// Four issuance products across two onboarded sources. `usecase` is only the
// nl-wallet disclosure_settings key; its adapter endpoint is generated from
// the corresponding source activation. IB 2023 deliberately demonstrates the
// policy's allowed-year boundary.
type AttestationConfig = {
  code: string
  label: string
  usecase: string  // must match [disclosure_settings.<usecase>] in issuance_server.toml
  clientId: string // reader-cert client_id
}

const ISSUANCE_SERVER_PUBLIC_URL = (
  window.__GBO_RUNTIME_CONFIG__?.eudiPublicUrl ||
  import.meta.env.VITE_EUDI_PUBLIC_URL ||
  ''
).replace(/\/$/, '')

function readerClientId(publicUrl: string): string {
  const configured = (
    window.__GBO_RUNTIME_CONFIG__?.eudiClientId ||
    import.meta.env.VITE_EUDI_CLIENT_ID ||
    ''
  ).trim()
  if (configured) return configured
  try {
    const hostname = new URL(publicUrl).hostname
    if (hostname) return `x509_san_dns:${hostname}`
  } catch {
    // Keep the development fallback below for an absent or invalid URL.
  }
  return ''
}

const CLIENT_ID = readerClientId(ISSUANCE_SERVER_PUBLIC_URL)

const ATTESTATION_TYPES: AttestationConfig[] = [
  {
    code: 'nl.gbo.belastingdienst.inkomensverklaring',
    label: 'Inkomensverklaring 2024 (Belastingdienst) — ALLOW',
    usecase: 'inkomensverklaring_2024',
    clientId: CLIENT_ID,
  },
  {
    code: 'nl.gbo.belastingdienst.inkomensverklaring',
    label: 'Inkomensverklaring 2025 (Belastingdienst) — ALLOW',
    usecase: 'inkomensverklaring_2025',
    clientId: CLIENT_ID,
  },
  {
    code: 'nl.gbo.belastingdienst.inkomensverklaring',
    label: 'Inkomensverklaring 2023 (Belastingdienst) — verwacht DENY (YEAR_NOT_ALLOWED)',
    usecase: 'inkomensverklaring_2023',
    clientId: CLIENT_ID,
  },
  {
    code: 'nl.gbo.brp.akte-van-overlijden',
    label: 'Akte van overlijden (BRP) — ALLOW',
    usecase: 'akte_van_overlijden',
    clientId: CLIENT_ID,
  },
]

const UL_BASE =
  import.meta.env.VITE_EUDI_UL_BASE ??
  'https://app.preproductie.wallet.edi.bzk.nl/deeplink/disclosure_based_issuance'
// Build the same universal-link that demo-issuer's <nl-wallet-button>
// generates. On scan the wallet POSTs to `request_uri`, where the
// issuance-server opens its own session — no dev-portal-side state needed.
export function walletUniversalLinkFor(cfg: AttestationConfig, sessionType: 'same_device' | 'cross_device'): string {
  if (!ISSUANCE_SERVER_PUBLIC_URL) return ''
  const requestUri = `${ISSUANCE_SERVER_PUBLIC_URL}/disclosure/${cfg.usecase}/request_uri?session_type=${sessionType}`
  const params = new URLSearchParams({
    request_uri: requestUri,
    request_uri_method: 'post',
    client_id: cfg.clientId,
  })
  return `${UL_BASE}?${params.toString()}`
}

export function attestationConfigFor(usecase: string): AttestationConfig | undefined {
  return ATTESTATION_TYPES.find((a) => a.usecase === usecase)
}

export default function EudiForm({ payload, setPayload, citizens }: Props) {
  const knownBsns = citizens.map((c) => c.bsn)
  return (
    <>
      <div className="field">
        <label htmlFor="usecase">Usecase</label>
        <select
          id="usecase"
          className="sel mono"
          value={payload.usecase}
          onChange={(e) => setPayload({ ...payload, usecase: e.target.value })}
        >
          {ATTESTATION_TYPES.map((a) => (
            <option key={a.usecase} value={a.usecase}>{a.label}</option>
          ))}
        </select>
      </div>

      <div className="hint" style={{ fontSize: 12, color: 'var(--mute)', marginTop: 8 }}>
        <div>
          BSN komt uit de <b>wallet-PID-disclosure</b> — niet uit dit portaal.
          Gebruik in je wallet een BSN die de bron kent, anders krijg je aan
          het eind een <code className="mono">404</code> van de adapter.
        </div>
        {knownBsns.length > 0 && (
          <div className="mono" style={{ fontSize: 11, marginTop: 4 }}>
            Bekende BSN&#39;s (graphql-server mockdata):{' '}
            {knownBsns.join(', ')}
          </div>
        )}
      </div>

      <div className="hint" style={{ fontSize: 12, color: 'var(--mute)', marginTop: 8 }}>
        De <b>bron</b> publiceert de query, parameters, mapping en Type Metadata;
        onboarding bindt die aan bron-OIN en FSC-service. De PDP autoriseert de
        daadwerkelijk gevraagde velden. EUD0001 staat alleen 2024 en 2025 toe,
        waardoor &quot;IB 2023&quot; fail-closed wordt geweigerd.
      </div>

      <div className="hint" style={{ fontSize: 12, color: 'var(--mute)', marginTop: 8 }}>
        <b>Akte van overlijden</b> leest uit de tweede bron (BRP,{' '}
        <code className="mono">brp-graphql-server</code>) en loopt van de
        bron-eigen attestation-view <code className="mono">akteVanOverlijden</code>.
        De selectie van de overleden partner gebeurt dus in de bron. In de mock
        voldoet alleen BSN{' '}
        <code className="mono">999991772</code> (Frouke Jansen) daaraan; andere
        BSN&#39;s geven een 404 zonder dat er policy aan te pas komt.
      </div>
    </>
  )
}
