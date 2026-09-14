import type { LdvChainResponse, LdvLogbookResult, LdvRecord } from '../api/devClient'

// Logboek Dataverwerkingen per Verantwoordelijke (Logius LDV v1.0.0).
//
// This is the third of the three logs the chain writes, next to the PDP's
// decision (ADL) and the transport records (FSC-Logging). They are separate
// stores by design — LDV has each Verantwoordelijke log its own processing —
// and the panel reads them the way the read extension says a reader does:
// from the logbook where the processing started, along dpl.read.nextLogbookId.
// Each logbook says how it was reached. One that no pointer leads to — the
// consent register's, whose status the PDP checks — is still shown, marked as
// such, rather than presented as if the chain led there.
//
// It is not the observability pipeline: these records are confirmed on write
// and never sampled, so an empty logbook here means nothing was processed,
// not that a span was dropped.

// Attribute paths, as the *read* extension renders them: nested objects in
// camelCase. The core standard writes the same attributes flat and in
// snake_case (`dpl.core.processing_activity_id`), and the logbook translates
// between the two at its read boundary — so a panel reading a read response
// has to use this shape, not the one the records are written in.
const ACTIVITY = ['dpl', 'core', 'processingActivityId']
const SUBJECT = ['dpl', 'core', 'dataSubjectId']
const SUBJECT_TYPE = ['dpl', 'core', 'dataSubjectIdType']
const NEXT_LOGBOOK = ['dpl', 'read', 'nextLogbookId']

function text(record: LdvRecord, path: string[]): string {
  let value: unknown = record.attributes
  for (const segment of path) {
    if (typeof value !== 'object' || value === null) return ''
    value = (value as Record<string, unknown>)[segment]
  }
  return typeof value === 'string' ? value : ''
}

// Records of one request form a tree through parentSpanId: a sidecar's
// forward holds the source query beneath it, and a certificate naming several
// people holds one child per further Betrokkene. Depth is what makes that
// readable, so it is computed rather than flattened away.
function depthOf(record: LdvRecord, bySpan: Map<string, LdvRecord>): number {
  let depth = 0
  let parent = record.parentSpanId
  const seen = new Set<string>([record.spanId])
  while (parent && bySpan.has(parent) && !seen.has(parent)) {
    seen.add(parent)
    depth += 1
    parent = bySpan.get(parent)?.parentSpanId
  }
  return depth
}

function reachedLabel(entry: LdvLogbookResult, names: Map<string, string>): string {
  switch (entry.reached_via) {
    case 'start':
      return 'startpunt'
    case 'pointer':
      return entry.from ? `via nextLogbookId uit ${names.get(entry.from) ?? entry.from}` : 'via nextLogbookId'
    default:
      return 'geen pointer naartoe'
  }
}

export default function LdvPanel({
  data, traceId, loading,
}: {
  data: LdvChainResponse | null
  traceId: string | null
  loading: boolean
}) {
  if (!traceId && !loading) return null

  const logbooks = data?.logbooks ?? []
  const configured = logbooks.length > 0
  const total = logbooks.reduce((sum, l) => sum + (l.records?.length ?? 0), 0)
  const names = new Map(logbooks.map((l) => [l.logbook.id, l.logbook.name]))
  // A logbook with nothing to show and nothing wrong goes on one line at the
  // bottom, so the chain above reads without gaps between its steps.
  const shown = logbooks.filter((l) => l.error || (l.records?.length ?? 0) > 0)
  const silent = logbooks.filter((l) => !l.error && (l.records?.length ?? 0) === 0)

  return (
    <div className="panel ldv-panel">
      <div className="panel-h">
        <span className="t">
          <span className="n">3.7</span>Logboek Dataverwerkingen · gevolgd via nextLogbookId
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
            {shown.map((entry) => {
              const records = entry.records ?? []
              const bySpan = new Map(records.map((r) => [r.spanId, r]))
              return (
                <div key={entry.logbook.id} className="ldv-logbook">
                  <div className="ldv-logbook-name">
                    <strong>{entry.logbook.name}</strong>
                    <code className="dim tiny" style={{ marginLeft: 6 }}>{entry.logbook.id}</code>
                    <span className="dim tiny" style={{ marginLeft: 6 }}>· {reachedLabel(entry, names)}</span>
                    {entry.error && <span className="fsc-txlog-err">— {entry.error}</span>}
                  </div>
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
                            <tr key={record.spanId}>
                              <td style={{ paddingLeft: 6 + depthOf(record, bySpan) * 14 }}>
                                <code>{record.name.replace('dataverwerking.', '')}</code>
                                {next && (
                                  <span className="dim tiny" title="dpl.read.nextLogbookId">
                                    {' '}→ {names.get(next) ?? next}
                                  </span>
                                )}
                              </td>
                              <td><code>{text(record, ACTIVITY) || '—'}</code></td>
                              <td>
                                <code>{text(record, SUBJECT) || '—'}</code>
                                <span className="dim tiny"> ({text(record, SUBJECT_TYPE) || '—'})</span>
                              </td>
                              <td><code>{String(record.resource?.attributes?.['service.name'] ?? '—')}</code></td>
                              <td>
                                <span className={record.status === 'Ok' ? 'dir-in' : 'dir-out'}>
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
            {silent.length > 0 && (
              <div className="dim tiny">
                Zonder records voor deze trace:{' '}
                {silent.map((entry, i) => (
                  <span key={entry.logbook.id}>
                    {i > 0 && ', '}
                    <strong>{entry.logbook.name}</strong> <code>{entry.logbook.id}</code>
                  </span>
                ))}
              </div>
            )}
          </div>
        )}
      </div>
    </div>
  )
}
