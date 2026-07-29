import type { Citizen, EudiPayload } from '../types'

type Props = {
  payload: EudiPayload
  setPayload: (p: EudiPayload) => void
  citizens: Citizen[]
}

// Four usecases across two bronnen: three income-statements (one per tax
// year, BD bron) and the akte van overlijden (BRP bron). The usecase-key maps
// to the disclosure_settings key in issuance-server config AND to the path in
// the adapter (usecase-catalog driven). IB 2023 is deliberately in the catalog
// while the EUD0001 policy denies it — demonstrating scope-authorization
// independent of catalog-membership.
type AttestationConfig = {
  code: string
  label: string
  usecase: string  // must match [disclosure_settings.<usecase>] in issuance-server.toml AND the adapter-catalog key
  clientId: string // reader-cert client_id
}

const ISSUANCE_SERVER_PUBLIC_URL = (
  window.__GBO_RUNTIME_CONFIG__?.eudiPublicUrl ??
  import.meta.env.VITE_EUDI_PUBLIC_URL ??
  ''
).replace(/\/$/, '')

// Since nl-wallet v0.5.0 the issuance-server derives the client_id from its
// own public_url — `x509_san_dns:<host of public_url>` (verifier.rs,
// client_id_from_public_url) — and the wallet rejects the session unless the
// client_id in the universal link is byte-for-byte the same. So derive it the
// same way here instead of carrying a separately-configured value that can
// silently drift. EUDI_CLIENT_ID stays available as an explicit override.
function clientIdFrom(publicUrl: string): string {
  if (!publicUrl) return ''
  try {
    return `x509_san_dns:${new URL(publicUrl).hostname}`
  } catch {
    return ''
  }
}

// `||`, not `??`: the nginx entrypoint always writes eudiClientId, as "" when
// the env var is unset, so a nullish check would never reach the fallback.
const CLIENT_ID =
  window.__GBO_RUNTIME_CONFIG__?.eudiClientId ||
  import.meta.env.VITE_EUDI_CLIENT_ID ||
  clientIdFrom(ISSUANCE_SERVER_PUBLIC_URL)

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
    label: 'Inkomensverklaring 2023 (Belastingdienst) — verwacht DENY (SCOPE_NOT_ALLOWED)',
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
        <b>Bron + scope</b> komen uit de <code className="mono">
        eudi-adapter/config/usecase_catalog.json</code> — per usecase een
        eigen bronprofiel, scope en flow. Policy EUD0001 (BD) en EUD0002 (BRP)
        weigeren scopes buiten hun whitelist, onafhankelijk van wat de catalog
        kent. Dat is waarom &quot;IB 2023&quot; verwacht wordt te DENY'en:
        catalog kent 'em, policy niet.
      </div>

      <div className="hint" style={{ fontSize: 12, color: 'var(--mute)', marginTop: 8 }}>
        <b>Akte van overlijden</b> leest uit de tweede bron (BRP,{' '}
        <code className="mono">brp-graphql-server</code>) en loopt van de
        nabestaande via <code className="mono">heeftHuwelijk.partners</code>{' '}
        naar de overledene. In de mock voldoet alleen BSN{' '}
        <code className="mono">999991772</code> (Frouke Jansen) daaraan; andere
        BSN&#39;s geven een 404 zonder dat er policy aan te pas komt.
      </div>
    </>
  )
}
