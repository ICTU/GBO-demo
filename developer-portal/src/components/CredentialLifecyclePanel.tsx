import { useCallback, useEffect, useState } from 'react'
import { changeLifecycleSetting, loadLifecycleEvents, loadLifecycleSettings, loadRPCatalog, loadRPEvents } from '../api/devClient'
import {
  copyOrigin, describe, describeRP, kindLabel, sessionChains, wholeNumber,
  type ConfigurationSettings, type LifecycleEvent, type LifecycleSettings, type ProviderSettings, type RPCatalog, type RPEvent,
} from '../lib/lifecycle'
import TestRelyingPartyForm from './TestRelyingPartyForm'

// The inputs as the revocation block styles them.
const inputStyle = {
  color: 'var(--fg)', background: 'var(--bg-alt)', border: '1px solid var(--border)', borderRadius: 4,
  padding: '4px 8px', fontFamily: 'monospace', width: 64,
}

const POLL_MS = 3000

// A colour per key fingerprint, so a copy is recognisable across events.
function fingerprintColour(fp: string): string {
  let h = 0
  for (const c of fp) h = (h * 31 + c.charCodeAt(0)) % 360
  return `hsl(${h} 55% 45%)`
}

function kindColour(kind: string): string {
  switch (kind) {
    case 'credential_issued': return 'var(--ok, #2a7)'
    case 'credential_refused': case 'token_refused': case 'rejected': return 'var(--danger, #d33)'
    case 'credential_deferred': case 'declined': return 'var(--warn, #c80)'
    case 'presented': return 'var(--accent, #36c)'
    default: return 'var(--mute)'
  }
}

const RP_KIND_LABELS: Record<string, string> = { presented: 'getoond', declined: 'geweigerd', rejected: 'afgewezen' }

function FingerprintChip({ fp }: { fp: string }) {
  return (
    <span className="mono" title="vingerafdruk van de sleutel van dit exemplaar"
      style={{ fontSize: 10, padding: '1px 6px', borderRadius: 10, color: '#fff', background: fingerprintColour(fp) }}>
      {fp}
    </span>
  )
}

type PolicyDraft = { policy: 'none' | 'once_only'; batch: string; trigger: string }

function policyDraft(c: ConfigurationSettings): PolicyDraft {
  return c.reuse_policy
    ? { policy: 'once_only', batch: String(c.reuse_policy.batch_size), trigger: String(c.reuse_policy.reissue_trigger_unused) }
    : { policy: 'none', batch: '3', trigger: '1' }
}

// The batch settings of the own credential issuer per source and per
// attestation type, and a timeline of what wallets did with them. Hidden
// when the backend has no lifecycle routes.
export default function CredentialLifecyclePanel() {
  const [settings, setSettings] = useState<LifecycleSettings>({ enabled: false })
  const [events, setEvents] = useState<LifecycleEvent[] | { error: string }>([])
  const [batchDrafts, setBatchDrafts] = useState<Record<string, string>>({})
  const [policyDrafts, setPolicyDrafts] = useState<Record<string, PolicyDraft>>({})
  const [message, setMessage] = useState('')
  const [busy, setBusy] = useState('')
  const [live, setLive] = useState(true)
  const [rpCatalog, setRPCatalog] = useState<RPCatalog>({ enabled: false })
  const [rpEvents, setRPEvents] = useState<RPEvent[]>([])

  const reloadSettings = useCallback(async () => {
    const loaded = await loadLifecycleSettings()
    setSettings(loaded)
    if (loaded.enabled) {
      setBatchDrafts(Object.fromEntries(loaded.providers.map((p) => [p.name, String(p.batch_size)])))
      setPolicyDrafts(Object.fromEntries(loaded.configurations.map((c) => [c.id, policyDraft(c)])))
    }
    return loaded.enabled
  }, [])

  const reloadEvents = useCallback(async () => {
    setEvents(await loadLifecycleEvents())
    const presented = await loadRPEvents()
    setRPEvents(Array.isArray(presented) ? presented : [])
  }, [])

  useEffect(() => {
    void reloadSettings().then((on) => { if (on) void reloadEvents() })
    void loadRPCatalog().then(setRPCatalog)
  }, [reloadSettings, reloadEvents])

  useEffect(() => {
    if (!settings.enabled || !live) return
    const timer = window.setInterval(() => void reloadEvents(), POLL_MS)
    return () => window.clearInterval(timer)
  }, [settings.enabled, live, reloadEvents])

  if (!settings.enabled) return null

  const apply = async (key: string, label: string, change: () => Promise<string>) => {
    setBusy(key)
    const error = await change()
    setMessage(error || `${label}: opgeslagen. Geldt vanaf het volgende verzoek van een wallet.`)
    setBusy('')
    await reloadSettings()
  }

  const saveProvider = (p: ProviderSettings) => {
    const n = wholeNumber(batchDrafts[p.name] ?? '', 0)
    if (n === null) {
      setMessage(`${p.name}: vul een geheel getal in.`)
      return
    }
    void apply(`p:${p.name}`, p.name, () => changeLifecycleSetting({ provider: p.name }, { batch_size: n }))
  }

  const savePolicy = (c: ConfigurationSettings) => {
    const d = policyDrafts[c.id] ?? policyDraft(c)
    if (d.policy === 'none') {
      void apply(`c:${c.id}`, c.name || c.id, () => changeLifecycleSetting({ configuration: c.id }, null))
      return
    }
    const batch = wholeNumber(d.batch, 0)
    const trigger = wholeNumber(d.trigger, 0)
    if (batch === null || trigger === null) {
      setMessage(`${c.name || c.id}: vul gehele getallen in.`)
      return
    }
    void apply(`c:${c.id}`, c.name || c.id, () => changeLifecycleSetting({ configuration: c.id },
      { reuse_policy: { method: 'once_only', batch_size: batch, reissue_trigger_unused: trigger } }))
  }

  const setDraft = (id: string, patch: Partial<PolicyDraft>, c: ConfigurationSettings) =>
    setPolicyDrafts({ ...policyDrafts, [id]: { ...(policyDrafts[id] ?? policyDraft(c)), ...patch } })

  const chains = Array.isArray(events) ? sessionChains(events) : []

  return (
    <div className="panel">
      <div className="panel-h">
        <span className="t">Credential lifecycle · batch size</span>
        <button className="mini" onClick={() => void reloadSettings().then(() => reloadEvents())} style={{ marginLeft: 'auto' }}>vernieuwen</button>
      </div>
      <div className="panel-b" style={{ display: 'flex', flexDirection: 'column', gap: 14 }}>
        <div className="hint" style={{ fontSize: 12, color: 'var(--mute)' }}>
          Hoeveel exemplaren de eigen credential issuer per uitgifte geeft. Per bron publiceert de issuer
          <span className="mono"> batch_credential_issuance</span>; een attestatietype kan een eigen hergebruikbeleid krijgen
          (<span className="mono">credential_reuse_policy</span>, <span className="mono">once_only</span>, ETSI TS 119 472-3), dat een
          EUDI-wallet boven de batch size van de bron stelt. Het beleid staat niet op het nl-profiel. Demo-hulpmiddel:
          de issuer houdt wijzigingen in het geheugen; na een herstart gelden weer de geconfigureerde waarden.
        </div>
        {message && <div aria-live="polite" style={{ fontSize: 12 }}>{message}</div>}
        {settings.error && <div style={{ color: 'var(--danger, #d33)', fontSize: 12 }}>{settings.error}</div>}

        <table>
          <thead>
            <tr><th>Bron</th><th>Batch size</th><th>Waarde uit</th><th /></tr>
          </thead>
          <tbody>
            {settings.providers.map((p) => (
              <tr key={p.name}>
                <td className="mono">{p.name}</td>
                <td>
                  <input aria-label={`batch size ${p.name}`} style={inputStyle} inputMode="numeric"
                    value={batchDrafts[p.name] ?? ''} onChange={(e) => setBatchDrafts({ ...batchDrafts, [p.name]: e.target.value })} />
                </td>
                <td>{p.overridden ? 'ingesteld' : 'configuratie'}</td>
                <td style={{ display: 'flex', gap: 6 }}>
                  <button className="btn mini" disabled={busy !== ''} onClick={() => saveProvider(p)}>
                    {busy === `p:${p.name}` ? 'bezig…' : 'Opslaan'}
                  </button>
                  <button className="btn mini" disabled={busy !== '' || !p.overridden}
                    onClick={() => void apply(`p:${p.name}`, p.name, () => changeLifecycleSetting({ provider: p.name }, null))}>
                    Terug naar configuratie
                  </button>
                </td>
              </tr>
            ))}
          </tbody>
        </table>

        <table>
          <thead>
            <tr><th>Attestatietype</th><th>Beleid</th><th>Batch size · trigger ongebruikt</th><th>Max. voor EUDI-wallet</th><th /></tr>
          </thead>
          <tbody>
            {settings.configurations.map((c) => {
              const d = policyDrafts[c.id] ?? policyDraft(c)
              const off = d.policy === 'none'
              return (
                <tr key={c.id}>
                  <td>
                    <div>{c.name || c.id}</div>
                    <div className="mono" style={{ fontSize: 11, color: 'var(--mute)' }}>{c.id}</div>
                  </td>
                  <td>
                    <select aria-label={`beleid ${c.id}`} value={d.policy} style={{ ...inputStyle, width: 110 }}
                      onChange={(e) => setDraft(c.id, { policy: e.target.value as PolicyDraft['policy'] }, c)}>
                      <option value="none">geen</option>
                      <option value="once_only">once_only</option>
                    </select>
                  </td>
                  <td>
                    {off ? (
                      <span style={{ color: 'var(--mute)', fontSize: 12 }}>batch size van de bron</span>
                    ) : (
                      <span style={{ display: 'flex', gap: 6, alignItems: 'center' }}>
                        <input aria-label={`beleid batch size ${c.id}`} style={inputStyle} inputMode="numeric"
                          value={d.batch} onChange={(e) => setDraft(c.id, { batch: e.target.value }, c)} />
                        <span aria-hidden="true">·</span>
                        <input aria-label={`trigger ongebruikt ${c.id}`} style={inputStyle} inputMode="numeric"
                          value={d.trigger} onChange={(e) => setDraft(c.id, { trigger: e.target.value }, c)} />
                      </span>
                    )}
                  </td>
                  <td className="mono">{c.batch_size}{c.reuse_policy ? ' (beleid)' : ' (bron)'}</td>
                  <td>
                    <button className="btn mini" disabled={busy !== ''} onClick={() => savePolicy(c)}>
                      {busy === `c:${c.id}` ? 'bezig…' : 'Opslaan'}
                    </button>
                  </td>
                </tr>
              )
            })}
          </tbody>
        </table>

        {rpCatalog.enabled && <TestRelyingPartyForm catalog={rpCatalog} events={rpEvents} />}

        <div style={{ display: 'flex', alignItems: 'center', gap: 10 }}>
          <strong style={{ fontSize: 13 }}>Tijdlijn per wallet-sessie</strong>
          <span style={{ fontSize: 12, color: 'var(--mute)' }}>laatste 50 minuten · geen attribuutwaarden</span>
          <label style={{ marginLeft: 'auto', fontSize: 12, display: 'flex', gap: 4, alignItems: 'center' }}>
            <input type="checkbox" checked={live} onChange={(e) => setLive(e.target.checked)} /> live
          </label>
        </div>
        {!Array.isArray(events) ? (
          <div style={{ color: 'var(--danger, #d33)', fontSize: 12 }}>{events.error}</div>
        ) : chains.length === 0 ? (
          <div className="empty" style={{ padding: 12 }}>Nog geen wallet-sessies. Maak een aanbod via de test-PID of de EU-wallet-QR en haal het op met een wallet.</div>
        ) : (
          chains.map((chain) => (
            <div key={chain.sessions[0]} style={{ border: '1px solid var(--border)', borderRadius: 6, padding: 8 }}>
              <div style={{ fontSize: 12, marginBottom: 6, display: 'flex', gap: 8, flexWrap: 'wrap' }}>
                <span className="mono">{chain.provider}/{chain.profile}</span>
                <span style={{ color: 'var(--mute)' }}>
                  sessie {chain.sessions.join(' → ')}{chain.sessions.length > 1 ? ' (refresh)' : ''}
                </span>
              </div>
              <table>
                <tbody>
                  {timelineRows(chain).map((row, i) => row.issuer ? (
                    <tr key={`${row.issuer.at}-${i}`}>
                      <td className="mono" style={{ whiteSpace: 'nowrap', width: 1 }}>{new Date(row.issuer.at).toLocaleTimeString('nl-NL')}</td>
                      <td style={{ whiteSpace: 'nowrap', width: 1, color: kindColour(row.issuer.kind), fontWeight: 600 }}>{kindLabel(row.issuer.kind)}</td>
                      <td style={{ fontSize: 12 }}>
                        {describe(row.issuer)}
                        {(row.issuer.key_fingerprints ?? []).length > 0 && (
                          <div style={{ display: 'flex', gap: 4, flexWrap: 'wrap', marginTop: 4 }}>
                            {row.issuer.key_fingerprints!.map((fp) => <FingerprintChip key={fp} fp={fp} />)}
                          </div>
                        )}
                      </td>
                    </tr>
                  ) : (
                    <tr key={`${row.rp!.at}-rp-${i}`}>
                      <td className="mono" style={{ whiteSpace: 'nowrap', width: 1 }}>{new Date(row.rp!.at).toLocaleTimeString('nl-NL')}</td>
                      <td style={{ whiteSpace: 'nowrap', width: 1, color: kindColour(row.rp!.kind), fontWeight: 600 }}>{RP_KIND_LABELS[row.rp!.kind] ?? row.rp!.kind}</td>
                      <td style={{ fontSize: 12 }}>
                        {describeRP(row.rp!, copyOrigin(chains, row.rp!.key_fingerprint))}
                        {row.rp!.key_fingerprint && <div style={{ marginTop: 4 }}><FingerprintChip fp={row.rp!.key_fingerprint} /></div>}
                      </td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          ))
        )}

        {rpCatalog.enabled && (
          <>
            <strong style={{ fontSize: 13 }}>Presentaties aan de testpartijen</strong>
            {rpEvents.length === 0 ? (
              <div className="empty" style={{ padding: 12 }}>Nog geen presentaties. Vraag er een met het formulier hierboven.</div>
            ) : (
              <table>
                <tbody>
                  {[...rpEvents].reverse().map((e, i) => (
                    <tr key={`${e.at}-${i}`}>
                      <td className="mono" style={{ whiteSpace: 'nowrap', width: 1 }}>{new Date(e.at).toLocaleTimeString('nl-NL')}</td>
                      <td style={{ whiteSpace: 'nowrap', width: 1, color: kindColour(e.kind), fontWeight: 600 }}>{RP_KIND_LABELS[e.kind] ?? e.kind}</td>
                      <td style={{ fontSize: 12 }}>
                        {describeRP(e, copyOrigin(chains, e.key_fingerprint))}
                        {e.key_fingerprint && <div style={{ marginTop: 4 }}><FingerprintChip fp={e.key_fingerprint} /></div>}
                      </td>
                    </tr>
                  ))}
                </tbody>
              </table>
            )}
          </>
        )}
      </div>
    </div>
  )

  // The events of a chain with the presentations of its copies, by time.
  function timelineRows(chain: ReturnType<typeof sessionChains>[number]): { issuer?: LifecycleEvent; rp?: RPEvent }[] {
    const rows: { at: string; issuer?: LifecycleEvent; rp?: RPEvent }[] = chain.events.map((e) => ({ at: e.at, issuer: e }))
    for (const e of rpEvents) {
      if (e.kind === 'presented' && copyOrigin(chains, e.key_fingerprint)?.chain === chain) rows.push({ at: e.at, rp: e })
    }
    return rows.sort((a, b) => a.at.localeCompare(b.at))
  }
}
