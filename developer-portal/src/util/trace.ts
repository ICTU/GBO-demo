// Pre-generate a W3C trace context client-side so polling can start before
// the backend response arrives. otelhttp picks up the incoming traceparent
// header as parent span.

function randomHex(byteLen: number): string {
  const bytes = new Uint8Array(byteLen)
  crypto.getRandomValues(bytes)
  return Array.from(bytes, (b) => b.toString(16).padStart(2, '0')).join('')
}

export type TraceContext = {
  traceId: string
  spanId: string
  header: string
}

export function newTraceContext(): TraceContext {
  const traceId = randomHex(16)
  const spanId = randomHex(8)
  return { traceId, spanId, header: `00-${traceId}-${spanId}-01` }
}
