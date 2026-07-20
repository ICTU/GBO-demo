import type { FscTxlogResponse } from '../api/devClient'

// FSC-txlog per hop. FSC does not export OTel spans, but records every
// request in its transaction-log per peer (fsc-logging Logius-draft).
// This panel shows per peer what got stored — proof the request travelled
// through the chain, including peer-IDs, contract-hash and direction.

export default function FscTxlogPanel({
  data, transactionId, loading,
}: {
  data: FscTxlogResponse | null
  transactionId: string | null
  loading: boolean
}) {
  if (!transactionId && !loading) return null
  return (
    <div className="panel fsc-txlog-panel">
      <div className="panel-h">
        <span className="t">
          <span className="n">3.6</span>FSC-transactielog · per peer
        </span>
        {transactionId && (
          <span className="mono" style={{ marginLeft: 'auto', fontSize: 12, color: 'var(--mute)' }}>
            {transactionId}
          </span>
        )}
      </div>
      <div className="panel-b">
        {loading && <div className="empty">Laden…</div>}
        {!loading && data?.note && <div className="empty">{data.note}</div>}
        {!loading && data && data.peers.length > 0 && (
          <div className="fsc-txlog-peers">
            {data.peers.map((p) => (
              <div key={p.peer} className="fsc-txlog-peer">
                <div className="fsc-txlog-peer-name">
                  <strong>{p.peer.toUpperCase()}</strong>
                  {p.error && <span className="fsc-txlog-err">— {p.error}</span>}
                </div>
                {!p.error && (!p.records || p.records.length === 0) && (
                  <div className="dim tiny">geen records</div>
                )}
                {p.records && p.records.length > 0 && (
                  <table className="fsc-txlog-table">
                    <thead>
                      <tr>
                        <th>direction</th>
                        <th>service</th>
                        <th>source</th>
                        <th>destination</th>
                        <th>grant_hash</th>
                      </tr>
                    </thead>
                    <tbody>
                      {p.records.map((r, i) => (
                        <tr key={i}>
                          <td>
                            <span className={r.direction.endsWith('OUTGOING') ? 'dir-out' : 'dir-in'}>
                              {r.direction.replace('DIRECTION_', '')}
                            </span>
                          </td>
                          <td><code>{r.service_name}</code></td>
                          <td><code>{r.source.outway_peer_id ?? '—'}</code></td>
                          <td><code>{r.destination.service_peer_id ?? '—'}</code></td>
                          <td className="fsc-txlog-hash" title={r.grant_hash}>
                            <code>{r.grant_hash.slice(0, 22)}…</code>
                          </td>
                        </tr>
                      ))}
                    </tbody>
                  </table>
                )}
              </div>
            ))}
          </div>
        )}
      </div>
    </div>
  )
}
