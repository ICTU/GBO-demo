import { useEffect, useMemo, useState } from 'react'
import { QRCodeSVG } from 'qrcode.react'
import { requestPresentation } from '../api/devClient'
import { claimLabel, requestState, type RPCatalog, type RPEvent } from '../lib/lifecycle'

const OUTCOMES: Record<string, string> = {
  presented: 'de wallet toonde een exemplaar', declined: 'de wallet weigerde', rejected: 'de testpartij wees de presentatie af',
}

const selectStyle = {
  color: 'var(--fg)', background: 'var(--bg-alt)', border: '1px solid var(--border)', borderRadius: 4,
  padding: '4px 8px', fontFamily: 'monospace',
}

type Catalog = Extract<RPCatalog, { enabled: true }>

// The presentation form of the test relying party (#737): choose a relying
// party, an attestation type and its claims, and get an openid4vp:// link
// as a QR code and as a link for the same device.
export default function TestRelyingPartyForm({ catalog, events }: { catalog: Catalog; events: RPEvent[] }) {
  const [party, setParty] = useState(catalog.parties[0]?.id ?? '')
  const [credential, setCredential] = useState(catalog.credentials[0]?.id ?? '')
  const [chosen, setChosen] = useState<string[]>([])
  const [busy, setBusy] = useState(false)
  const [result, setResult] = useState<{ link: string; expires_at: string; session: string } | { error: string } | null>(null)
  // Ticks each second, so an open request turns expired on time.
  const [now, setNow] = useState(() => new Date())
  useEffect(() => {
    const timer = window.setInterval(() => setNow(new Date()), 1000)
    return () => window.clearInterval(timer)
  }, [])

  const cred = useMemo(() => catalog.credentials.find((c) => c.id === credential), [catalog, credential])
  useEffect(() => {
    setChosen(cred?.claims[0] ? [claimLabel(cred.claims[0])] : [])
  }, [cred])

  const toggle = (label: string) =>
    setChosen(chosen.includes(label) ? chosen.filter((c) => c !== label) : [...chosen, label])

  const ask = async () => {
    if (!cred) return
    setBusy(true)
    const claims = cred.claims.filter((p) => chosen.includes(claimLabel(p)))
    setResult(await requestPresentation(party, cred.id, claims))
    setBusy(false)
  }

  return (
    <div style={{ display: 'flex', flexDirection: 'column', gap: 8 }}>
      <strong style={{ fontSize: 13 }}>Test-relying party: presentatie vragen</strong>
      <div className="hint" style={{ fontSize: 12, color: 'var(--mute)' }}>
        Een fictieve partij vraagt de wallet om een attestatie van de eigen issuer. De tijdlijn laat zien welk
        exemplaar de wallet toonde, aan wie, en met welke status. De waarden van de claims bewaart de testpartij niet.
      </div>
      {catalog.error && <div style={{ color: 'var(--danger, #d33)', fontSize: 12 }}>{catalog.error}</div>}
      <div style={{ display: 'flex', gap: 8, flexWrap: 'wrap', alignItems: 'center', fontSize: 12 }}>
        <label htmlFor="rp-party">partij</label>
        <select id="rp-party" style={selectStyle} value={party} onChange={(e) => setParty(e.target.value)}>
          {catalog.parties.map((p) => <option key={p.id} value={p.id}>{p.name}</option>)}
        </select>
        <label htmlFor="rp-credential">attestatietype</label>
        <select id="rp-credential" style={selectStyle} value={credential} onChange={(e) => setCredential(e.target.value)}>
          {catalog.credentials.map((c) => <option key={c.id} value={c.id}>{c.name} ({c.id})</option>)}
        </select>
      </div>
      {cred && (
        <fieldset style={{ border: '1px solid var(--border)', borderRadius: 4, padding: 8, fontSize: 12 }}>
          <legend>claims</legend>
          <div style={{ display: 'flex', gap: 12, flexWrap: 'wrap' }}>
            {cred.claims.map((p) => {
              const label = claimLabel(p)
              return (
                <label key={label} className="mono" style={{ display: 'flex', gap: 4, alignItems: 'center' }}>
                  <input type="checkbox" checked={chosen.includes(label)} onChange={() => toggle(label)} /> {label}
                </label>
              )
            })}
          </div>
        </fieldset>
      )}
      <div>
        <button className="btn mini" disabled={busy || !cred || chosen.length === 0} onClick={() => void ask()}>
          {busy ? 'bezig…' : 'Presentatie vragen'}
        </button>
      </div>
      {result && 'error' in result && <div style={{ color: 'var(--danger, #d33)', fontSize: 12 }}>{result.error}</div>}
      {result && 'link' in result && (() => {
        const state = requestState(result, events, now)
        if (state.state === 'answered') {
          return (
            <div aria-live="polite" style={{ fontSize: 12 }}>
              Beantwoord: {OUTCOMES[state.event.kind] ?? state.event.kind} (zie de tijdlijn). Een verzoek geldt één keer;
              vraag een nieuwe presentatie voor de volgende.
            </div>
          )
        }
        if (state.state === 'expired') {
          return <div aria-live="polite" style={{ fontSize: 12 }}>Verlopen zonder antwoord. Vraag een nieuwe presentatie.</div>
        }
        return (
        <div style={{ display: 'flex', gap: 12, alignItems: 'flex-start', flexWrap: 'wrap', fontSize: 12 }}>
          <QRCodeSVG value={result.link} size={180} bgColor="#ffffff" fgColor="#000000" includeMargin />
          <div style={{ display: 'flex', flexDirection: 'column', gap: 6, maxWidth: 480 }}>
            <a href={result.link}>Openen in de wallet op dit apparaat</a>
            <span>Geldig tot {new Date(result.expires_at).toLocaleTimeString('nl-NL')}.</span>
            <span style={{ color: 'var(--mute)' }}>In de iOS-simulator:</span>
            <code className="mono" style={{ wordBreak: 'break-all', fontSize: 11 }}>xcrun simctl openurl booted '{result.link}'</code>
          </div>
        </div>
        )
      })()}
    </div>
  )
}
