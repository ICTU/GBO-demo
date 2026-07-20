import { useEffect, useState } from 'react'
import { fetchTrace, serviceNameForSpan, spanTag, type JaegerSpan, type JaegerTrace } from '../api/jaegerClient'

// Dev-portal inspection from span-attributes. Services put `gbo.*` attrs
// on their existing otelhttp/tracer-span with the request-body and any
// interesting headers. This hook fetches the trace, picks the first span
// that matches on serviceName + (optional) path-prefix, and returns the
// `gbo.*` attrs for the popover.

type Match = {
  service: string
  pathPrefix?: string
  // Optional: match on operationName (for sub-spans like pep.opa_authorize).
  operation?: string
}

// Mapping node-id → span-match. `undefined` means "no inspection available
// via spans" (e.g. actor, s02 which has no backend-span, or nodes that
// already surface their data via another channel).
// For the root request-span we match on operationName = serviceName
// (that's what otelhttp.NewHandler(mux, "<name>") produces). Without that
// filter pickSpan could grab a child-span (e.g. pep.opa_authorize) which
// doesn't carry the incoming-request-attrs.
const NODE_SPAN_MATCH: Record<string, Match | undefined> = {
  // DvTP and EUDI now share the same AuthZen path; pdp is the policy-node
  // in both flows. FSC containers (hv-outway/bd-inway/edi-outway/
  // hv-manager/edi-manager) don't export OTel-spans out-of-the-box, so
  // popovers for those nodes stay empty (matches the 'no-otel' visual
  // status).
  'pdp': { service: 'pdp-service', operation: 'pdp-service' },
  'sidecar': { service: 'bron-sidecar', operation: 'bron-sidecar' },
}

function pickSpan(trace: JaegerTrace, m: Match): JaegerSpan | undefined {
  return trace.spans.find((s) => {
    if (serviceNameForSpan(trace, s) !== m.service) return false
    if (m.operation && s.operationName !== m.operation) return false
    if (m.pathPrefix) {
      const path = String(spanTag(s, 'http.target') ?? spanTag(s, 'http.route') ?? spanTag(s, 'url.path') ?? '')
      if (!path.startsWith(m.pathPrefix)) return false
    }
    return true
  })
}

// Extract all attributes with prefix `gbo.` — that's our inspection set.
function gboAttrs(span: JaegerSpan): Record<string, string> {
  const out: Record<string, string> = {}
  for (const t of span.tags ?? []) {
    if (typeof t.key === 'string' && t.key.startsWith('gbo.') && typeof t.value === 'string') {
      out[t.key] = t.value
    }
  }
  return out
}

export type SpanInspect = {
  spanId: string
  attrs: Record<string, string>
}

export function useSpanInspect(traceId: string | undefined, nodeId: string): {
  inspects: SpanInspect[]
  loading: boolean
} {
  const [inspects, setInspects] = useState<SpanInspect[]>([])
  const [loading, setLoading] = useState(false)

  useEffect(() => {
    if (!traceId) { setInspects([]); return }
    const matches = collectMatches(nodeId)
    if (matches.length === 0) { setInspects([]); return }
    setLoading(true)
    let cancelled = false
    fetchTrace(traceId).then((trace) => {
      if (cancelled || !trace) return
      const out: SpanInspect[] = []
      for (const m of matches) {
        const span = pickSpan(trace, m)
        if (!span) continue
        const attrs = gboAttrs(span)
        if (Object.keys(attrs).length > 0) out.push({ spanId: span.spanID, attrs })
      }
      setInspects(out)
    }).finally(() => { if (!cancelled) setLoading(false) })
    return () => { cancelled = true }
  }, [traceId, nodeId])

  return { inspects, loading }
}

function collectMatches(nodeId: string): Match[] {
  const primary = NODE_SPAN_MATCH[nodeId]
  if (!primary) return []
  return [primary]
}
