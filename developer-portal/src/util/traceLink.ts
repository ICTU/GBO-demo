// Link to a trace in the trace UI. The base is either a URL template with
// {traceId} (the Aspire dashboard: http://localhost:15126/traces/detail/{traceId})
// or a Jaeger UI base URL, to which /trace/<id> is appended.
export function traceLink(base: string, traceId: string): string {
  const id = encodeURIComponent(traceId)
  return base.includes('{traceId}') ? base.replace('{traceId}', id) : `${base.replace(/\/$/, '')}/trace/${id}`
}
