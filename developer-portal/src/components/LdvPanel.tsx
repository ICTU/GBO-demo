import type { LdvChainResponse, LdvRecord } from '../api/devClient'

// Logboek Dataverwerkingen per Verantwoordelijke (Logius LDV v1.0.0).
//
// This is the third of the three logs the chain writes, next to the PDP's
// decision (ADL) and the transport records (FSC-Logging). They are separate
// stores by design — LDV has each Verantwoordelijke log its own processing —
// and what joins them is the trace id shown in the header. Seeing the same
// value on all three panels is the point of this one.
//
// It is not the observability pipeline: these records are confirmed on write
// and never sampled, so an empty logbook here means nothing was processed,
// not that a span was dropped.

const ACTIVITY = 'dpl.core.processing_activity_id'
const SUBJECT = 'dpl.core.data_subject_id'
const SUBJECT_TYPE = 'dpl.core.data_subject_id_type'
const NEXT_LOGBOOK = 'dpl.read.nextLogbookId'

function text(record: LdvRecord, key: string): string {
  const value = record.attributes?.[key]
  return typeof value === 'string' ? value : ''
}

// Records of one request form a tree through parent_span_id: a sidecar's
// forward holds the source query beneath it, and a certificate naming several
// people holds one child per further Betrokkene. Depth is what makes that
// readable, so it is computed rather than flattened away.
function depthOf(record: LdvRecord, bySpan: Map<string, LdvRecord>): number {
  let depth = 0
  let parent = record.parent_span_id
  const seen = new Set<string>([record.span_id])
  while (parent && bySpan.has(parent) && !seen.has(parent)) {
    seen.add(parent)
    depth += 1
    parent = bySpan.get(parent)?.parent_span_id
  }
  return depth
}

export default function LdvPanel({
  data, transactionId, loading,
}: {
  data: LdvChainResponse | null
  transactionId: string | null
  loading: boolean
}) {
  if (!transactionId && !loading) return null

  const configured = (data?.logbooks?.length ?? 0) > 0
  const total = data?.logbooks?.reduce((sum, l) => sum + (l.records?.length ?? 0), 0) ?? 0

  return (
    <div className="panel ldv-panel">
      <div className="panel-h">
        <span className="t">
          <span className="n">3.7</span>Logboek Dataverwerkingen · per verantwoordelijke
        </span>
        {data?.trace_id && (
          <span className="mono" style={{ marginLeft: 'auto', fontSize: 12, color: 'var(--mute)' }}>
            {data.trace_id}
          </span>
        )}
      </div>
      <div className="panel-b">
        {loading && <div className="empty">Laden…</div>}
        {!loading && !configured && (
          <div className="empty">Geen logboeken geconfigureerd.</div>
        )}
        {!loading && configured && total === 0 && (
          <div className="empty">
            Geen records voor deze trace. Records worden bevestigd weggeschreven en
            nooit gesampled, dus dit betekent dat er niets is verwerkt.
          </div>
        )}
        {!loading && configured && data && (
          <div className="ldv-logbooks">
            {data.logbooks.map((entry) => {
              const records = entry.records ?? []
              const bySpan = new Map(records.map((r) => [r.span_id, r]))
              return (
                <div key={entry.logbook.id} className="ldv-logbook">
                  <div className="ldv-logbook-name">
                    <strong>{entry.logbook.name}</strong>
                    <code className="dim tiny" style={{ marginLeft: 6 }}>{entry.logbook.id}</code>
                    {entry.error && <span className="fsc-txlog-err">— {entry.error}</span>}
                    {entry.truncated && <span className="dim tiny"> — afgekapt</span>}
                  </div>
                  {!entry.error && records.length === 0 && (
                    <div className="dim tiny">geen records</div>
                  )}
                  {records.length > 0 && (
                    <table className="fsc-txlog-table">
                      <thead>
                        <tr>
                          <th>dataverwerking</th>
                          <th>verwerkingsactiviteit</th>
                          <th>betrokkene</th>
                          <th>component</th>
                          <th>status</th>
                        </tr>
                      </thead>
                      <tbody>
                        {records.map((record) => {
                          const next = text(record, NEXT_LOGBOOK)
                          return (
                            <tr key={record.span_id}>
                              <td style={{ paddingLeft: 6 + depthOf(record, bySpan) * 14 }}>
                                <code>{record.name.replace('dataverwerking.', '')}</code>
                                {next && (
                                  <span className="dim tiny" title="dpl.read.nextLogbookId">
                                    {' '}→ {next}
                                  </span>
                                )}
                              </td>
                              <td><code>{text(record, ACTIVITY) || '—'}</code></td>
                              <td>
                                <code>{text(record, SUBJECT) || '—'}</code>
                                <span className="dim tiny"> ({text(record, SUBJECT_TYPE) || '—'})</span>
                              </td>
                              <td><code>{record.resource?.['service.name'] ?? '—'}</code></td>
                              <td>
                                <span className={record.status === 'OK' ? 'dir-in' : 'dir-out'}>
                                  {record.status}
                                </span>
                              </td>
                            </tr>
                          )
                        })}
                      </tbody>
                    </table>
                  )}
                </div>
              )
            })}
          </div>
        )}
      </div>
    </div>
  )
}
