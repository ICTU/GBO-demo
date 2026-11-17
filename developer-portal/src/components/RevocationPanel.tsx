import { useCallback, useEffect, useState } from 'react'
import { loadRevocationOffers, loadStatusLists, revokeEntry, revokeOffer } from '../api/devClient'
import {
  OFFERS_CHANGED, confirmRevocation, entryFrom, revokedSummary,
  type RevocationOffers, type StatusListView,
} from '../lib/revocation'

// The inputs as the EU-wallet panel styles its BSN field.
const inputStyle = {
  color: 'var(--fg)', background: 'var(--bg-alt)', border: '1px solid var(--border)', borderRadius: 4,
  padding: '4px 8px', fontFamily: 'monospace',
}

// Revocation of what the portal had the own credential issuer offer, a
// single status list entry, and the public status lists of those offers.
// Hidden when the backend has no revocation routes.
export default function RevocationPanel() {
  const [offers, setOffers] = useState<RevocationOffers>({ enabled: false })
  const [lists, setLists] = useState<StatusListView[] | { error: string }>([])
  const [busy, setBusy] = useState('')
  const [message, setMessage] = useState('')
  const [entry, setEntry] = useState({ provider: 'test-pid', list: 'pid', index: '' })

  const reload = useCallback(async () => {
    const loaded = await loadRevocationOffers()
    setOffers(loaded)
    if (loaded.enabled) setLists(await loadStatusLists())
  }, [])

  useEffect(() => {
    void reload()
    const onChange = () => void reload()
    window.addEventListener(OFFERS_CHANGED, onChange)
    return () => window.removeEventListener(OFFERS_CHANGED, onChange)
  }, [reload])

  if (!offers.enabled) return null

  const revoke = async (reference: string) => {
    const offer = offers.offers.find((o) => o.reference === reference)
    if (!offer || !window.confirm(confirmRevocation(offer))) return
    setBusy(reference)
    const outcome = await revokeOffer(reference)
    setMessage('error' in outcome ? outcome.error : `${outcome.offer.label}: ${revokedSummary(outcome.offer)}.`)
    setBusy('')
    await reload()
  }

  const revokeOne = async () => {
    const parsed = entryFrom(entry.provider, entry.list, entry.index)
    if ('error' in parsed) {
      setMessage(parsed.error)
      return
    }
    if (!window.confirm(`Plek ${parsed.index} van ${parsed.provider}/${parsed.list} intrekken?\n\nIntrekken is definitief en niet terug te draaien. Andere kopieën uit dezelfde uitgifte blijven geldig.`)) return
    setBusy('entry')
    const error = await revokeEntry(parsed.provider, parsed.list, parsed.index)
    setMessage(error || `Plek ${parsed.index} van ${parsed.provider}/${parsed.list} is ingetrokken.`)
    setBusy('')
    await reload()
  }

  return (
    <div className="panel">
      <div className="panel-h">
        <span className="t">Intrekken · status lists</span>
        <button className="mini" onClick={() => void reload()} style={{ marginLeft: 'auto' }}>vernieuwen</button>
      </div>
      <div className="panel-b" style={{ display: 'flex', flexDirection: 'column', gap: 14 }}>
        <div className="hint" style={{ fontSize: 12, color: 'var(--mute)' }}>
          Aanbiedingen die dit portal liet maken, met een kenmerk waarop de credential issuer alle kopieën van de uitgifte
          intrekt. Demo-hulpmiddel: iedereen die het portal bereikt kan alles intrekken; in het echt trekt alleen de
          aanbieder in. Het portal onthoudt de aanbiedingen tot het herstart.
        </div>
        {message && <div aria-live="polite" style={{ fontSize: 12 }}>{message}</div>}

        {offers.error ? (
          <div style={{ color: 'var(--danger, #d33)', fontSize: 12 }}>{offers.error}</div>
        ) : offers.offers.length === 0 ? (
          <div className="empty" style={{ padding: 12 }}>Nog geen aanbiedingen met kenmerk. Maak er een via de test-PID of de EU-wallet-QR.</div>
        ) : (
          <table>
            <thead>
              <tr><th>Tijd</th><th>Aanbod</th><th>BSN</th><th>Lijst</th><th>Kenmerk</th><th>Status</th><th /></tr>
            </thead>
            <tbody>
              {offers.offers.map((o) => (
                <tr key={o.reference}>
                  <td className="mono">{new Date(o.offered_at).toLocaleTimeString('nl-NL')}</td>
                  <td>{o.label}</td>
                  <td className="mono">{o.bsn}</td>
                  <td className="mono">{o.provider}/{o.list}</td>
                  <td className="mono" style={{ fontSize: 11 }}>{o.reference}</td>
                  <td>{revokedSummary(o)}</td>
                  <td>
                    <button className="btn mini" onClick={() => void revoke(o.reference)} disabled={busy !== '' || Boolean(o.revoked_at)}>
                      {busy === o.reference ? 'bezig…' : 'Intrekken'}
                    </button>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        )}

        <div style={{ display: 'flex', gap: 8, alignItems: 'center', flexWrap: 'wrap', fontSize: 12 }}>
          <span>Losse plek intrekken:</span>
          <label htmlFor="revoke-provider">aanbieder</label>
          <input id="revoke-provider" className="input mono" style={{ ...inputStyle, width: 140 }} value={entry.provider}
            onChange={(e) => setEntry({ ...entry, provider: e.target.value })} />
          <label htmlFor="revoke-list">lijst</label>
          <input id="revoke-list" className="input mono" style={{ ...inputStyle, width: 140 }} value={entry.list}
            onChange={(e) => setEntry({ ...entry, list: e.target.value })} />
          <label htmlFor="revoke-index">plek</label>
          <input id="revoke-index" className="input mono" style={{ ...inputStyle, width: 80 }} inputMode="numeric" value={entry.index}
            onChange={(e) => setEntry({ ...entry, index: e.target.value })} />
          <button className="btn mini" onClick={() => void revokeOne()} disabled={busy !== ''}>
            {busy === 'entry' ? 'bezig…' : 'Intrekken'}
          </button>
        </div>

        <div>
          <div style={{ fontSize: 13, fontWeight: 600, marginBottom: 6 }}>Status lists (publiek, gedecodeerd, niet geverifieerd)</div>
          {'error' in lists ? (
            <div style={{ color: 'var(--danger, #d33)', fontSize: 12 }}>{lists.error}</div>
          ) : lists.length === 0 ? (
            <div className="empty" style={{ padding: 12 }}>Nog geen lijsten: die verschijnen bij de eerste aanbieding.</div>
          ) : (
            lists.map((l) => (
              <div key={l.uri} style={{ borderTop: '1px solid var(--border)', padding: '8px 0', fontSize: 12 }}>
                <div className="mono">{l.uri}</div>
                {l.error ? (
                  <div style={{ color: 'var(--danger, #d33)' }}>{l.error}</div>
                ) : (
                  <>
                    <div style={{ color: 'var(--mute)' }}>
                      {l.revoked.length} van {l.size} plekken ingetrokken · {l.bits} bit per plek · iat {l.iat ? new Date(l.iat).toLocaleTimeString('nl-NL') : '–'} ·
                      exp {l.exp ? new Date(l.exp).toLocaleTimeString('nl-NL') : '–'} · ttl {l.ttl ?? '–'} s · {l.offers} aanbieding(en) van dit portal
                    </div>
                    {l.revoked.length > 0 && (
                      <div className="mono" style={{ wordBreak: 'break-word' }}>ingetrokken: {l.revoked.join(', ')}</div>
                    )}
                  </>
                )}
              </div>
            ))
          )}
        </div>
      </div>
    </div>
  )
}
