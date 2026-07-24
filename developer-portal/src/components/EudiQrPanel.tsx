import { useState } from 'react'
import { QRCodeSVG } from 'qrcode.react'
import {
  attestationConfigFor, demoIssuerPageFor, walletUniversalLinkFor,
} from './EudiForm'

type Props = {
  usecaseKey: string
  onCancel: () => void
  // Playground toggle: tells App that a curl is running so the watch-mode
  // callback doesn't jump to the result-panel (which would hide the user's
  // own response).
  onPlaygroundToggle?: (active: boolean) => void
}

// Show a client-side assembled universal-link as QR + fallback-links.
// Supports both same-device (open on this phone) and cross-device (scan
// with another device) — mirrors what demo-issuer's <nl-wallet-button>
// would render, but without a tab-switch.
export default function EudiQrPanel({ usecaseKey, onCancel, onPlaygroundToggle }: Props) {
  const cfg = attestationConfigFor(usecaseKey)
  const [bsn, setBsn] = useState('123456789')
  const [busy, setBusy] = useState(false)
  const [response, setResponse] = useState<string | null>(null)
  const [error, setError] = useState<string | null>(null)

  if (!cfg) return null

  const crossDeviceUl = walletUniversalLinkFor(cfg, 'cross_device')
  const sameDeviceUl = walletUniversalLinkFor(cfg, 'same_device')
  const demoPage = demoIssuerPageFor(cfg)
  const missingConfig = !crossDeviceUl

  const runCurl = async () => {
    setBusy(true); setError(null); setResponse(null)
    onPlaygroundToggle?.(true)
    try {
      const res = await fetch(`/eudi-api/${usecaseKey}/`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify([{
          attestations: [{
            attestation_type: cfg.code,
            attributes: { bsn },
          }],
          id: 'dev-portal-playground',
        }]),
      })
      const text = await res.text()
      try {
        setResponse(JSON.stringify(JSON.parse(text), null, 2))
      } catch {
        setResponse(text)
      }
      if (!res.ok) setError(`HTTP ${res.status}`)
    } catch (e) {
      setError((e as Error).message)
    } finally {
      setBusy(false)
      // playgroundActive stays on until the user clicks 'cancel' — the
      // flag keeps the QR-panel open (gate in App.tsx) so the response
      // stays visible. Late spans are safe: the watch-callback only
      // updates the arch-strip, not the result-panel.
    }
  }

  return (
    <div className="panel">
      <div className="panel-h">
        <span className="t">
          <span className="n">3.3</span>Wallet-QR · wacht op scan
        </span>
        <button className="mini" onClick={onCancel} style={{ marginLeft: 'auto' }}>
          annuleren
        </button>
      </div>
      <div className="panel-b" style={{ display: 'flex', flexDirection: 'column', gap: 12, alignItems: 'center' }}>
        {missingConfig ? (
          <div className="empty" style={{ padding: 20, textAlign: 'center' }}>
            <b>VITE_EUDI_PUBLIC_URL</b> is niet gezet — kan QR niet samenstellen.
            Zet 'm in <code className="mono">05-demo/.env</code> en herstart de compose.
          </div>
        ) : (
          <>
            <QRCodeSVG value={crossDeviceUl} size={220} bgColor="#ffffff" fgColor="#000000" marginSize={4} />
            <div style={{ fontSize: 12, color: 'var(--mute)', textAlign: 'center' }}>
              Scan met TestFlight-wallet (cross-device). PID-disclosure met een BSN
              die de bron kent — anders 404 aan het eind.
            </div>
            <div style={{ display: 'flex', gap: 10, flexWrap: 'wrap', justifyContent: 'center' }}>
              <a className="btn mini" href={sameDeviceUl} target="_blank" rel="noopener noreferrer">
                same-device link
              </a>
              <a className="btn mini" href={demoPage} target="_blank" rel="noopener noreferrer">
                demo-issuer page ↗
              </a>
            </div>

            {/* Mini-playground: bypass the wallet, curl the adapter
                directly — shows the raw attestation-response that would
                otherwise only go to the wallet. Handy for demo or
                debugging without a phone. */}
            <div style={{
              width: '100%', marginTop: 16, paddingTop: 12,
              borderTop: '1px solid var(--border)',
            }}>
              <div style={{ fontSize: 12, color: 'var(--mute)', marginBottom: 8 }}>
                Of test zonder wallet — direct curl naar adapter:
              </div>
              <div style={{ display: 'flex', gap: 8, alignItems: 'center' }}>
                <label style={{ fontSize: 12 }}>BSN</label>
                <input
                  className="input mono"
                  style={{
                    flex: 1, maxWidth: 180,
                    color: 'var(--fg)', background: 'var(--bg-alt)',
                    border: '1px solid var(--border)', borderRadius: 4,
                    padding: '4px 8px', fontFamily: 'monospace',
                  }}
                  value={bsn}
                  onChange={(e) => setBsn(e.target.value)}
                  placeholder="123456789"
                />
                <button className="btn mini" onClick={runCurl} disabled={busy}>
                  {busy ? 'bezig…' : 'verstuur'}
                </button>
              </div>
              {error && (
                <div style={{ color: 'var(--danger, #d33)', fontSize: 12, marginTop: 6 }}>
                  {error}
                </div>
              )}
              {response && (
                <pre style={{
                  marginTop: 8, padding: 10, background: 'var(--bg-alt)',
                  border: '1px solid var(--border)', borderRadius: 4,
                  maxHeight: 300, overflow: 'auto', fontSize: 11,
                }}>
                  {response}
                </pre>
              )}
            </div>
          </>
        )}
      </div>
    </div>
  )
}
