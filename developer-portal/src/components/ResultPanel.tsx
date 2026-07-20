import { hlJSON } from '../util/highlight'
import type { Outcome } from '../types'

export type ResultData = {
  outcome: Outcome
  status: number
  body: unknown
  trace_id?: string
  reason?: string
  narrative?: string
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
