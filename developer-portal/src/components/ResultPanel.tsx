import { hlJSON } from '../util/highlight'
import type { Outcome } from '../types'
import { display, summary, type AsiRequestView } from '../lib/asiRequest'

export type ResultData = {
  outcome: Outcome
  status: number
  body: unknown
  trace_id?: string
  reason?: string
  narrative?: string
  // asi is an ASI request at the reference QTSP: its status, the seal
  // check and the result per attribute.
  asi?: AsiRequestView
}

type Props = {
  result: ResultData | null
  pending?: boolean
  jaegerUrl?: string
  grafanaUrl?: string
}

const JAEGER_DEFAULT = 'http://localhost:9686'
const GRAFANA_DEFAULT = 'http://localhost:9300'

export default function ResultPanel({ result, pending = false, jaegerUrl = JAEGER_DEFAULT, grafanaUrl = GRAFANA_DEFAULT }: Props) {
  return (
    <div className="panel" style={{ minHeight: 300 }}>
      <div className="panel-h">
        <span className="t">
          <span className="n">3.3</span>Resultaat · trace
        </span>
        {result && (
          <span className="mono" style={{ marginLeft: 'auto', fontSize: 12, color: 'var(--mute)' }}>
            HTTP {result.status}
          </span>
        )}
      </div>
      <div className="panel-b">
        {!result ? (
          <div className="empty">
            {pending ? 'Bezig met ophalen van trace-data…' : 'Nog geen verzending. Stel een verzoek samen en klik Verzenden.'}
          </div>
        ) : (
          <>
            {(result.outcome === 'allow' || result.outcome === 'deny') && (
              <div className={`verdict ${result.outcome}`}>
                <div className="verdict-badge">{result.outcome.toUpperCase()}</div>
                {result.narrative && <div className="verdict-sub">{result.narrative}</div>}
              </div>
            )}
            {result.outcome === 'error' && (
              <div className="verdict deny">
                <div className="verdict-badge">ERROR</div>
                {result.narrative && <div className="verdict-sub">{result.narrative}</div>}
              </div>
            )}

            {result.asi && <AsiOutcome view={result.asi} />}

            {result.reason && (
              <div className="kv">
                <span className="k">Reden</span>
                <span className="v sx-lit">{result.reason}</span>
              </div>
            )}
            {result.trace_id && (
              <>
                <div className="kv">
                  <span className="k">Trace-ID</span>
                  <span className="v">{result.trace_id}</span>
                </div>
                <a
                  className="linkbtn"
                  href={`${jaegerUrl}/trace/${result.trace_id}`}
                  target="_blank"
                  rel="noreferrer"
                >
                  Open trace in Jaeger <span className="ext">↗</span>
                </a>
                <a
                  className="linkbtn"
                  href={`${grafanaUrl}/d/request-flow?var-trace_id=${encodeURIComponent(result.trace_id)}`}
                  target="_blank"
                  rel="noreferrer"
                >
                  Open logs in Grafana <span className="ext">↗</span>
                </a>
              </>
            )}

            <div style={{ marginTop: 16 }}>
              <div style={{ fontSize: 11, color: 'var(--mute)', marginBottom: 6, fontWeight: 800, letterSpacing: '.05em', textTransform: 'uppercase' }}>
                Response body
              </div>
              <pre className="codeblock codeblock--scroll" dangerouslySetInnerHTML={{ __html: hlJSON(result.body) }} />
            </div>
          </>
        )}
      </div>
    </div>
  )
}

function AsiOutcome({ view }: { view: AsiRequestView }) {
  const accepted = view.status === 'done' && view.seal?.valid
  return (
    <div style={{ marginBottom: 12 }}>
      <div className={`verdict ${accepted ? 'allow' : view.status === 'pending' ? '' : 'deny'}`}>
        <div className="verdict-badge">
          {view.status === 'pending' ? 'WACHT' : view.status === 'done' ? (accepted ? 'RESULTAAT' : 'NIET GEACCEPTEERD') : view.status === 'denied' ? 'GEWEIGERD' : 'MISLUKT'}
        </div>
        <div className="verdict-sub">{summary(view)}</div>
      </div>
      {view.data_service && (
        <div className="kv">
          <span className="k">Dataservice</span>
          <span className="v mono">{view.data_service}{view.source ? ` · ${view.source}` : ''}</span>
        </div>
      )}
      {accepted && view.results && view.results.length > 0 && (
        <table>
          <thead>
            <tr><th>Attribuut</th><th>{view.operation === 'verify' ? 'Uitkomst' : 'Waarde'}</th></tr>
          </thead>
          <tbody>
            {view.results.map((r) => (
              <tr key={r.identifier}>
                <td>{r.label}</td>
                <td className="mono">{display(r)}</td>
              </tr>
            ))}
          </tbody>
        </table>
      )}
    </div>
  )
}
