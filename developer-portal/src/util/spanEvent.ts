import type { SpanInfo } from './spanMapping'

// One span as the dev-portal-backend streams it over /events.
export type SpanEvent = {
  trace_id: string
  span_id: string
  parent_id?: string
  service: string
  name: string
  start_nanos: number
  end_nanos: number
  status_code: number
  attributes?: Record<string, string>
}

// Both OTel HTTP semantic conventions are live side by side: the older
// services emit http.target/http.url/http.status_code, the EUDI services the
// stable url.path/url.full/http.response.status_code.
function firstAttr(attrs: Record<string, string> | undefined, keys: string[]): string | undefined {
  for (const k of keys) {
    const v = attrs?.[k]
    if (v) return v
  }
  return undefined
}

export function asSpanInfo(e: SpanEvent): SpanInfo {
  const status = firstAttr(e.attributes, ['http.status_code', 'http.response.status_code'])
  return {
    serviceName: e.service,
    operationName: e.name,
    httpPath: firstAttr(e.attributes, ['http.target', 'http.url', 'url.path', 'url.full']) ?? '',
    error: e.status_code === 2 || (status ? parseInt(status, 10) >= 400 : false),
    sourceOIN: e.attributes?.['gbo.source_oin'] || undefined,
  }
}
