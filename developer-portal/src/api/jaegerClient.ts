// Jaeger Query API client. Routes through the /jaeger-api proxy
// (Vite + Nginx forward to jaeger:16686).

const BASE = '/jaeger-api'

export type JaegerSpan = {
  spanID: string
  operationName: string
  startTime: number
  duration: number
  processID: string
  references?: Array<{ refType: string; traceID: string; spanID: string }>
  tags?: Array<{ key: string; value: string | number | boolean; type: string }>
}

export type JaegerProcess = {
  serviceName: string
  tags?: Array<{ key: string; value: string | number | boolean }>
}

export type JaegerTrace = {
  traceID: string
  spans: JaegerSpan[]
  processes: Record<string, JaegerProcess>
}

type JaegerTraceResponse = {
  data: JaegerTrace[]
  errors?: unknown
}

export async function fetchTrace(traceId: string): Promise<JaegerTrace | null> {
  try {
    const res = await fetch(`${BASE}/api/traces/${encodeURIComponent(traceId)}`)
    if (!res.ok) return null
    const body = (await res.json()) as JaegerTraceResponse
    return body.data?.[0] ?? null
  } catch {
    return null
  }
}

// Cross-trace lookup on tag equality. Jaeger accepts a `tags`
// query-parameter with URL-encoded JSON. Indispensable for the EUDI-flow:
// the traceparent context breaks at FSC-Inway (FSC without OTel), so the
// adapter-trace and pdp-trace end up with different traceIDs. We correlate
// them via gbo.fsc.transaction_id.
export async function fetchTracesByTag(
  service: string,
  tagKey: string,
  tagValue: string,
  lookbackMinutes = 60,
): Promise<JaegerTrace[]> {
  try {
    const tags = encodeURIComponent(JSON.stringify({ [tagKey]: tagValue }))
    const url = `${BASE}/api/traces?service=${encodeURIComponent(service)}&tags=${tags}&limit=5&lookback=${lookbackMinutes}m`
    const res = await fetch(url)
    if (!res.ok) return []
    const body = (await res.json()) as JaegerTraceResponse
    return body.data ?? []
  } catch {
    return []
  }
}

export function isSpanError(span: JaegerSpan): boolean {
  if (!span.tags) return false
  for (const t of span.tags) {
    if (t.key === 'error' && (t.value === true || t.value === 'true')) return true
    if (t.key === 'otel.status_code' && t.value === 'ERROR') return true
    if (t.key === 'http.status_code' && typeof t.value === 'number' && t.value >= 400) return true
  }
  return false
}

export function serviceNameForSpan(trace: JaegerTrace, span: JaegerSpan): string | undefined {
  return trace.processes[span.processID]?.serviceName
}

export function spanTag(span: JaegerSpan, key: string): string | number | boolean | undefined {
  return span.tags?.find((t) => t.key === key)?.value
}

// Tag-name varies across OTel versions.
export function httpPathForSpan(span: JaegerSpan): string {
  const candidates = ['http.target', 'http.route', 'http.url', 'url.path']
  for (const k of candidates) {
    const v = spanTag(span, k)
    if (typeof v === 'string') return v
  }
  return ''
}
