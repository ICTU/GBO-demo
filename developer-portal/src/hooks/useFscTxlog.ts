import { useEffect, useState } from 'react'
import { fetchTrace, fetchTracesByTag, spanTag, type JaegerTrace } from '../api/jaegerClient'
import { fetchFscTxlog, type FscTxlogResponse } from '../api/devClient'
import type { Tab } from '../types'
import type { NodeStatus } from '../util/spanMapping'

// Bridge between our adapter-trace and the rest of the FSC chain. Broken
// traceparent propagation (FSC without OTel + AuthZen-plugin using
// context.Background() for the authz call) prevents cross-hop tracing via
// traceparent. This hook bridges via the FSC-standard Fsc-Transaction-Id:
//
//   1. Fetch the adapter-trace → read the gbo.fsc.transaction_id tag.
//   2. Fetch /api/dev/fsc/txlog/<uuid> → per FSC-peer a record with
//      peer-IDs, service, contract-hash and direction.
//   3. Fetch the pdp-service trace via cross-trace-tag-lookup on the
//      Fsc-Transaction-Id (Jaeger `tags` parameter). Yields
//      gbo.fsc.authzen.decision + gbo.opa.input + gbo.opa.output —
//      enough for node-status and the NodePopover.
//   4. Compute node-status overrides for ArchStrip:
//        - edi-outway / edi-manager: 'green' if edi-peer has records
//        - bd-inway:                 'green' if bd-peer has records
//        - pdp / opa:                'green' when authzen.decision=true,
//                                    'red' when false, 'grey' if the
//                                    pdp span was not found
//   5. Also return the pdp-trace so useSpanInspect can reuse it for the
//      PDP-popover.
//
// Works for both flows that pass through FSC-Inway (EUDI and DvTP).

export type FscOverrides = {
  states: Record<string, NodeStatus>
  pdpTrace: JaegerTrace | null
}

export function useFscTxlog(traceId: string | undefined, mode: Tab): {
  data: FscTxlogResponse | null
  loading: boolean
  transactionId: string | null
  overrides: FscOverrides
} {
  const [data, setData] = useState<FscTxlogResponse | null>(null)
  const [loading, setLoading] = useState(false)
  const [transactionId, setTransactionId] = useState<string | null>(null)
  const [overrides, setOverrides] = useState<FscOverrides>({ states: {}, pdpTrace: null })

  useEffect(() => {
    if (!traceId || (mode !== 'eudi-issuance' && mode !== 'use')) {
      setData(null); setTransactionId(null); setOverrides({ states: {}, pdpTrace: null }); return
    }
    let cancelled = false
    setLoading(true)
    ;(async () => {
      try {
        // Poll Jaeger with backoff — OTel batching can leave a fresh trace
        // invisible for up to ~5s after the run completes. Without retries
        // the trace-id-tag lookup silently fails and PDP/OPA stay grey.
        let trace = null
        let txID: string | null = null
        for (const delay of [0, 500, 1000, 1500, 2000]) {
          if (delay > 0) await new Promise((r) => setTimeout(r, delay))
          if (cancelled) return
          trace = await fetchTrace(traceId)
          for (const s of trace?.spans ?? []) {
            const v = spanTag(s, 'gbo.fsc.transaction_id')
            if (typeof v === 'string' && v.length > 0) { txID = v; break }
          }
          if (txID) break
        }
        if (!txID) {
          setData(null); setTransactionId(null)
          setOverrides({ states: {}, pdpTrace: null })
          return
        }
        setTransactionId(txID)

        // Parallel: txlog per peer + pdp-trace via cross-tag-lookup.
        // Same backoff-retry rationale as above — pdp-service spans may
        // still be batching when the ingress trace is already indexed.
        let pdpTrace = null
        const txlogRes = await fetchFscTxlog(txID)
        if (cancelled) return
        setData(txlogRes)
        for (const delay of [0, 500, 1000, 1500, 2000]) {
          if (delay > 0) await new Promise((r) => setTimeout(r, delay))
          if (cancelled) return
          const pdpTraces = await fetchTracesByTag('pdp-service', 'gbo.fsc.transaction_id', txID)
          pdpTrace = pickTraceWithTag(pdpTraces, 'gbo.fsc.transaction_id', txID)
          if (pdpTrace) break
        }

        setOverrides({
          states: computeOverrides(txlogRes, pdpTrace),
          pdpTrace,
        })
      } finally {
        if (!cancelled) setLoading(false)
      }
    })()
    return () => { cancelled = true }
  }, [traceId, mode])

  return { data, loading, transactionId, overrides }
}

function pickTraceWithTag(traces: JaegerTrace[], key: string, value: string): JaegerTrace | null {
  for (const tr of traces) {
    for (const s of tr.spans) {
      if (spanTag(s, key) === value) return tr
    }
  }
  return null
}

function computeOverrides(
  txlog: FscTxlogResponse | null,
  pdpTrace: JaegerTrace | null,
): Record<string, NodeStatus> {
  const out: Record<string, NodeStatus> = {}
  if (txlog) {
    for (const peer of txlog.peers) {
      const hasRecords = peer.records && peer.records.length > 0 && !peer.error
      if (!hasRecords) continue
      if (peer.peer === 'edi') {
        out['edi-outway'] = 'green'
        out['edi-manager'] = 'green'
      } else if (peer.peer === 'hv') {
        out['hv-outway'] = 'green'
        out['hv-manager'] = 'green'
      } else if (peer.peer === 'bd') {
        out['bd-inway'] = 'green'
      }
    }
  }
  if (pdpTrace) {
    let decision: boolean | undefined
    for (const s of pdpTrace.spans) {
      const v = spanTag(s, 'gbo.fsc.authzen.decision')
      if (typeof v === 'boolean') { decision = v; break }
    }
    if (decision === true) {
      out['pdp'] = 'green'
      out['opa'] = 'green'
    } else if (decision === false) {
      out['pdp'] = 'red'
      out['opa'] = 'red'
    } else {
      out['pdp'] = 'green' // trace found but no decision-tag — treat as reached
      out['opa'] = 'green'
    }
  }
  return out
}
