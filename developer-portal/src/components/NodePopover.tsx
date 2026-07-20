import { useState } from 'react'
import NodeIO from './NodeIO'
import OpaDecisionContext from './OpaDecisionContext'
import type { NodeDef } from '../data/chains'
import type { NodeState } from '../hooks/useArchState'
import type { ApiCall } from '../types'
import type { OpaExplainDecision } from '../api/devClient'
import { hlJSON } from '../util/highlight'
import { useSpanInspect, type SpanInspect } from '../hooks/useSpanInspect'

const JAEGER_DEFAULT = 'http://localhost:9686'
const GRAFANA_DEFAULT = 'http://localhost:9300'

type Props = {
  node: NodeDef
  state: NodeState
  apiCall?: ApiCall
  traceId?: string
  jaegerUrl?: string
  grafanaUrl?: string
  explainDecisions?: OpaExplainDecision[]
  explainLoading?: boolean
  explainError?: string | null
}

const STATE_LABEL: Record<NodeState, string> = {
  green: 'Geslaagd',
  red: 'Gefaald',
  yellow: 'Actief',
  grey: 'Niet aangeraakt',
  'no-otel': 'Geen OTel-instrumentatie',
}

const STATE_COLOR: Record<NodeState, string> = {
  green: 'var(--allow-br)',
  red: 'var(--deny-br)',
  yellow: 'var(--warn-br)',
  grey: 'var(--mute)',
  'no-otel': 'var(--mute)',
}

export default function NodePopover({
  node, state, apiCall, traceId,
  jaegerUrl = JAEGER_DEFAULT, grafanaUrl = GRAFANA_DEFAULT,
  explainDecisions, explainLoading, explainError,
}: Props) {
  const stateColor = STATE_COLOR[state]
  const hasIO = !!apiCall
  const isOpaNode = node.id === 'opa'
  const spanInspect = useSpanInspect(traceId, node.id)
  const hasSpanInspect = spanInspect.inspects.length > 0
  const wide = isOpaNode || hasSpanInspect

  return (
    <div className={`popover${wide ? ' popover--wide' : ''}`} onClick={(e) => e.stopPropagation()}>
      <span className="pop-arrow" />
      <div className="pop-st" style={{ color: stateColor }}>
        <span
          style={{
            display: 'inline-block',
            width: 9, height: 9, borderRadius: '50%', background: stateColor,
          }}
        />
        {STATE_LABEL[state]}
      </div>
      <div className="mono" style={{ fontSize: 11, color: 'var(--mute)', marginTop: 4 }}>
        {node.svc}
      </div>

      {traceId && (
        <a className="linkbtn" href={`${jaegerUrl}/trace/${traceId}`} target="_blank" rel="noreferrer">
          Trace in Jaeger <span className="ext">↗</span>
        </a>
      )}
      {traceId && (
        <a className="linkbtn" href={`${grafanaUrl}/d/request-flow?var-trace_id=${encodeURIComponent(traceId)}`} target="_blank" rel="noreferrer">
          Logs in Grafana <span className="ext">↗</span>
        </a>
      )}

      {isOpaNode ? (
        <OpaPolicySection
          decisions={explainDecisions ?? []}
          loading={!!explainLoading}
          error={explainError ?? null}
        />
      ) : hasIO ? (
        <NodeIO
          method={apiCall?.method}
          url={apiCall?.url}
          status={apiCall?.status}
          requestBody={apiCall?.request_body}
          responseBody={apiCall?.response_body}
        />
      ) : hasSpanInspect ? (
        <SpanInspectSection inspects={spanInspect.inspects} loading={spanInspect.loading} nodeId={node.id} />
      ) : spanInspect.loading ? (
        <div style={{ marginTop: 12, fontSize: 12, color: 'var(--mute)' }}>Span-attrs ophalen…</div>
      ) : (
        <div
          style={{
            marginTop: 12, padding: '9px 11px', fontSize: 11, color: 'var(--mute)',
            borderLeft: '3px solid var(--border-2)', background: 'var(--panel-2)',
            borderRadius: '0 5px 5px 0', lineHeight: 1.5,
          }}
        >
          Geen inline request/response beschikbaar voor dit blok in deze flow —
          gebruik Jaeger/Grafana voor detail.
        </div>
      )}
    </div>
  )
}

// Render `gbo.*` span-attrs as grouped key/value blocks. Bodies (values that
// are JSON) get pretty-printed; headers render as plain key: value lines.
// Each span-object gets its own "sub-card" so that PEP's incoming request
// and the pep.opa_authorize outgoing AuthZEN are clearly separated.
function SpanInspectSection({ inspects, loading, nodeId }: { inspects: SpanInspect[]; loading: boolean; nodeId: string }) {
  if (loading && inspects.length === 0) {
    return <div style={{ marginTop: 12, fontSize: 12, color: 'var(--mute)' }}>Span-attrs ophalen…</div>
  }
  return (
    <div style={{ marginTop: 12, display: 'flex', flexDirection: 'column', gap: 10 }}>
      {inspects.map((ins, i) => (
        <SpanInspectCard key={ins.spanId} inspect={ins} isFirst={i === 0} nodeId={nodeId} />
      ))}
    </div>
  )
}

function SpanInspectCard({ inspect, isFirst, nodeId }: { inspect: SpanInspect; isFirst: boolean; nodeId: string }) {
  const groups = groupAttrs(inspect.attrs, nodeId)
  return (
    <div style={{
      padding: '9px 11px', fontSize: 12,
      borderLeft: `3px solid ${isFirst ? 'var(--allow-br)' : 'var(--warn-br)'}`,
      background: 'var(--panel-2)', borderRadius: '0 5px 5px 0',
    }}>
      {Object.entries(groups).map(([label, entries]) => (
        <div key={label} style={{ marginTop: 6 }}>
          <div className="mono" style={{ fontSize: 10, color: 'var(--mute)', textTransform: 'uppercase', letterSpacing: 0.4 }}>{label}</div>
          {entries.map(([k, v]) => (
            <AttrLine key={k} label={k} value={v} />
          ))}
        </div>
      ))}
    </div>
  )
}

function AttrLine({ label, value }: { label: string; value: string }) {
  const isBodyish = label.endsWith('body') || label.endsWith('request') || label.endsWith('response') || label.endsWith('input')
  let pretty = value
  if (isBodyish) {
    try {
      pretty = JSON.stringify(JSON.parse(value), null, 2)
    } catch {
      // keep raw
    }
  }
  return (
    <div style={{ marginTop: 4 }}>
      <div className="mono" style={{ fontSize: 10, color: 'var(--mute)' }}>{label}</div>
      <pre
        className="mono"
        style={{
          margin: 0, padding: '4px 6px', background: 'var(--panel)',
          borderRadius: 3, fontSize: 11, maxHeight: 200, overflow: 'auto',
          whiteSpace: 'pre-wrap', wordBreak: 'break-word',
        }}
        dangerouslySetInnerHTML={isBodyish ? { __html: hlJSON(pretty) } : undefined}
      >
        {isBodyish ? undefined : value || <span style={{ color: 'var(--mute)' }}>(leeg)</span>}
      </pre>
    </div>
  )
}

// Group attrs by prefix + explicit direction labels so the reader
// immediately sees what's inbound and what's outbound. `authzen.request`
// has opposite direction on PEP (outgoing) vs PDP (incoming) — hence the
// nodeId-context to set the label correctly.
function groupAttrs(attrs: Record<string, string>, nodeId: string): Record<string, [string, string][]> {
  const g: Record<string, [string, string][]> = {}
  const push = (label: string, k: string, v: string) => {
    ;(g[label] ??= []).push([k, v])
  }
  for (const [k, v] of Object.entries(attrs)) {
    if (k === 'gbo.request.body') push('← Request-body (binnengekomen)', 'body', v)
    else if (k.startsWith('gbo.request.header.')) push('← Request-headers (binnengekomen)', k.slice('gbo.request.header.'.length), v)
    else if (k.startsWith('gbo.request.stripped-')) push('✂ Gestript (spoof-defence)', k.slice('gbo.request.stripped-'.length), v)
    else if (k.startsWith('gbo.forwarded.header.')) push('→ Forwarded-headers (trusted, naar volgende hop)', k.slice('gbo.forwarded.header.'.length), v)
    else if (k === 'gbo.authzen.request') {
      // PEP sends to PDP; PDP receives from PEP.
      const label = nodeId === 'pdp' ? '← AuthZEN-request (binnengekomen van PEP)' : '→ AuthZEN-request (uitgaand naar PDP)'
      push(label, 'body', v)
    }
    else if (k === 'gbo.authzen.response') push('← AuthZEN-response (terug van PDP)', 'body', v)
    else if (k === 'gbo.opa.input') push('→ OPA-input (uitgaand naar OPA, na context-handler-enrichment)', 'body', v)
    else push('Other', k, v)
  }
  return g
}

function OpaPolicySection({
  decisions, loading, error,
}: { decisions: OpaExplainDecision[]; loading: boolean; error: string | null }) {
  const [activeIdx, setActiveIdx] = useState(0)

  if (loading) {
    return <div style={{ marginTop: 12, fontSize: 12, color: 'var(--mute)' }}>OPA-trace ophalen…</div>
  }
  if (error) {
    return <div style={{ marginTop: 12, fontSize: 12, color: 'var(--deny-br)' }}>Fout: {error}</div>
  }
  if (decisions.length === 0) {
    return (
      <div style={{ marginTop: 12, fontSize: 12, color: 'var(--mute)' }}>
        Geen decision-log gevonden voor deze trace (laatste 30 min).
      </div>
    )
  }

  const active = decisions[Math.min(activeIdx, decisions.length - 1)]
  const result = (active.result ?? {}) as { allow?: boolean; reason?: string }
  const allowLabel = result.allow === true ? 'ALLOW' : result.allow === false ? 'DENY' : '—'
  const allowColor = result.allow === true ? 'var(--allow-br)' : result.allow === false ? 'var(--deny-br)' : 'var(--mute)'

  return (
    <div style={{ marginTop: 14 }}>
      <div style={{ fontSize: 11, color: 'var(--mute)', fontWeight: 800, letterSpacing: '.05em', textTransform: 'uppercase', marginBottom: 6 }}>
        OPA-beslissing{decisions.length > 1 ? `en (${decisions.length})` : ''}
      </div>

      {decisions.length > 1 && (
        <div style={{ display: 'flex', gap: 4, marginBottom: 10, flexWrap: 'wrap' }}>
          {decisions.map((d, i) => {
            const r = (d.result ?? {}) as { allow?: boolean }
            const dot = r.allow === true ? 'var(--allow-br)' : r.allow === false ? 'var(--deny-br)' : 'var(--mute)'
            const isActive = i === activeIdx
            return (
              <button
                key={d.decision_id ?? i}
                onClick={() => setActiveIdx(i)}
                style={{
                  border: `1px solid ${isActive ? 'var(--accent)' : 'var(--border-2)'}`,
                  background: isActive ? 'var(--panel-2)' : 'transparent',
                  color: 'var(--text)',
                  borderRadius: 6, padding: '4px 9px', fontSize: 11, cursor: 'pointer',
                  display: 'inline-flex', alignItems: 'center', gap: 6,
                }}
              >
                <span style={{ display: 'inline-block', width: 7, height: 7, borderRadius: '50%', background: dot }} />
                <span className="mono">{d.path ?? `decision ${i + 1}`}</span>
              </button>
            )
          })}
        </div>
      )}

      <div style={{ display: 'flex', alignItems: 'center', gap: 8, marginBottom: 8 }}>
        <span style={{ color: allowColor, fontWeight: 800, fontSize: 15 }}>{allowLabel}</span>
        {result.reason && <span className="mono" style={{ fontSize: 11, color: 'var(--mute)' }}>· {result.reason}</span>}
        {active.path && (
          <span className="mono" style={{ marginLeft: 'auto', fontSize: 10.5, color: 'var(--mute)' }}>
            path: {active.path}
          </span>
        )}
      </div>

      <OpaDecisionContext decision={active} />

      <details style={{ marginTop: 10 }}>
        <summary style={{ cursor: 'pointer', fontSize: 11, color: 'var(--mute)', fontWeight: 700 }}>Raw decision-log entry</summary>
        <pre className="codeblock codeblock--scroll" style={{ marginTop: 6 }} dangerouslySetInnerHTML={{ __html: hlJSON(active) }} />
      </details>
    </div>
  )
}
