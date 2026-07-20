import { useEffect, useMemo, useState } from 'react'
import {
  EUDI_ISSUANCE_NODE_IDS, ISSUANCE_NODE_IDS, USE_NODE_IDS, nodeStatesFromSpans,
  type NodeStatus, type SpanInfo,
} from '../util/spanMapping'
import type { Tab } from '../types'

type SpanEvent = {
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

function asSpanInfo(e: SpanEvent): SpanInfo {
  return {
    serviceName: e.service,
    operationName: e.name,
    httpPath: e.attributes?.['http.target'] ?? e.attributes?.['http.url'] ?? '',
    error: e.status_code === 2 || (e.attributes?.['http.status_code']
      ? parseInt(e.attributes['http.status_code'], 10) >= 400
      : false),
  }
}

// Subscribes to /events?trace_id=X via SSE. Each span event reveals nodes in
// the architecture-strip in real time — replaces the Jaeger Query API polling
// loop. Unmapped nodes are yellow while the stream is live (= "still waiting")
// and grey once ready (= "not touched by this run").
export function useChainEvents(
  traceId: string | null,
  mode: Tab,
): { states: Record<string, NodeStatus>; ready: boolean } {
  const expected = useMemo(
    () => (
      mode === 'issuance' ? ISSUANCE_NODE_IDS
      : mode === 'eudi-issuance' ? EUDI_ISSUANCE_NODE_IDS
      : USE_NODE_IDS
    ),
    [mode],
  )
  const [collected, setCollected] = useState<SpanInfo[]>([])
  const [ready, setReady] = useState(true)

  useEffect(() => {
    if (!traceId) {
      setCollected([])
      setReady(true)
      return
    }
    setCollected([])
    setReady(false)
    const es = new EventSource(`/api/dev/events?trace_id=${encodeURIComponent(traceId)}`)
    let timer: ReturnType<typeof setTimeout> | null = null
    const settle = () => {
      if (timer) clearTimeout(timer)
      timer = setTimeout(() => setReady(true), 600)
    }
    es.addEventListener('span', (e) => {
      const sp = JSON.parse((e as MessageEvent).data) as SpanEvent
      setCollected((c) => [...c, asSpanInfo(sp)])
      settle()
    })
    es.addEventListener('close', () => {
      setReady(true)
      es.close()
    })
    es.onerror = () => {
      setReady(true)
      es.close()
    }
    return () => {
      if (timer) clearTimeout(timer)
      es.close()
    }
  }, [traceId, mode])

  const states = useMemo(() => {
    const next = nodeStatesFromSpans(collected, mode)
    const merged: Record<string, NodeStatus> = {}
    for (const id of expected) {
      // While streaming: unmapped nodes are "still pending" → yellow.
      // After ready (stream settled): unmapped nodes are "not touched" → grey.
      if (next[id] !== 'grey') merged[id] = next[id]
      else merged[id] = ready ? 'grey' : 'yellow'
    }
    return merged
  }, [collected, expected, mode, ready])

  return { states, ready }
}
