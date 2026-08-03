import { useEffect, useState } from 'react'
import { fetchTrace, spanTag } from '../api/jaegerClient'
import { fetchExplain, fetchFscTxlog, type FscTxlogResponse } from '../api/devClient'
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
//   3. Fetch the PDP decision from the OpenFTV decision log via
//      /explain?trace_id=<Fsc-Transaction-Id> — the OpenFTV request-
//      mapper copies Fsc-Transaction-Id into context.trace_id, so the
//      decision log correlates directly (no more Jaeger cross-trace-
//      lookup; the OpenFTV PDP has no OTel instrumentation).
//   4. Compute node-status overrides for ArchStrip:
//        - edi-outway / edi-manager: 'green' if edi-peer has records
//        - bd-inway:                 'green' if bd-peer has records
//        - pdp / opa:                'green' when decision=true,
//                                    'red' when false, 'grey' if no
//                                    decision-log entry was found
//   5. Also return decisionTraceKey so ArchStrip can point useExplain at
//      the Fsc-Transaction-Id for the PDP-popover.
//
// Works for both flows that pass through FSC-Inway (EUDI and DvTP).

export type FscOverrides = {
  states: Record<string, NodeStatus>
  decisionTraceKey: string | null
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
  const [overrides, setOverrides] = useState<FscOverrides>({ states: {}, decisionTraceKey: null })

  useEffect(() => {
    if (!traceId || (mode !== 'eudi-issuance' && mode !== 'use')) {
      setData(null); setTransactionId(null); setOverrides({ states: {}, decisionTraceKey: null }); return
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
          setOverrides({ states: {}, decisionTraceKey: null })
          return
        }
        setTransactionId(txID)

        // Parallel: txlog per peer + decision via the decision log.
        // Backoff-retry: promtail→Loki ingestion lags a few seconds
        // behind the request, like OTel batching did before.
        let decision: boolean | undefined
        let decisionFound = false
        const txlogRes = await fetchFscTxlog(txID)
        if (cancelled) return
        setData(txlogRes)
        for (const delay of [0, 500, 1000, 1500, 2000]) {
          if (delay > 0) await new Promise((r) => setTimeout(r, delay))
          if (cancelled) return
          try {
            const decisions = await fetchExplain(txID)
            if (decisions.length > 0) {
              decision = decisions[0].result?.decision === true
              decisionFound = true
              break
            }
          } catch {
            // no decision-log entry (yet) — retry
          }
        }

        setOverrides({
          states: computeOverrides(txlogRes, decision, decisionFound),
          decisionTraceKey: decisionFound ? txID : null,
        })
      } finally {
        if (!cancelled) setLoading(false)
      }
    })()
    return () => { cancelled = true }
  }, [traceId, mode])

  return { data, loading, transactionId, overrides }
}

function computeOverrides(
  txlog: FscTxlogResponse | null,
  decision: boolean | undefined,
  decisionFound: boolean,
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
  if (decisionFound) {
    if (decision === true) {
      out['pdp'] = 'green'
      out['opa'] = 'green'
    } else {
      out['pdp'] = 'red'
      out['opa'] = 'red'
    }
  }
  return out
}
