import { useEffect, useState } from 'react'
import { fetchLdvChain, type LdvChainResponse } from '../api/devClient'

// The LDV half of the three-standard picture. It hangs off the same
// Fsc-Transaction-Id the FSC txlog and the PDP decision use, so this hook
// takes the transaction id useFscTxlog already resolved rather than
// rediscovering it from the Jaeger trace.
//
// Records are confirmed on write and never sampled, so a record that exists is
// never lost — but "written" and "readable here" stopped being the same moment
// when the writers gained an outbox. Each one now makes its record durable
// locally and delivers to the logbook on a ~2s ticker, and several services
// write to one logbook on tickers of their own. Fetching once, immediately,
// therefore catches whichever records happened to have flushed: the panel
// showed one row of a five-row chain and called the rest absent.
//
// So this polls like the Jaeger and Loki lookups beside it, and stops early on
// the first tick that adds nothing. An empty result after the last attempt
// still means what the panel says it means — nothing was processed.
export function useLdvChain(transactionId: string | null): {
  data: LdvChainResponse | null
  loading: boolean
} {
  const [data, setData] = useState<LdvChainResponse | null>(null)
  const [loading, setLoading] = useState(false)

  useEffect(() => {
    if (!transactionId) {
      setData(null)
      return
    }
    let cancelled = false
    setLoading(true)
    ;(async () => {
      try {
        let best: LdvChainResponse | null = null
        let bestCount = -1
        for (const delay of [0, 500, 1000, 1500, 2500]) {
          if (delay > 0) await new Promise((r) => setTimeout(r, delay))
          if (cancelled) return
          const chain = await fetchLdvChain(transactionId)
          const count = countRecords(chain)
          if (count > bestCount) {
            best = chain
            bestCount = count
            setData(chain)
          } else if (bestCount > 0) {
            // A tick that added nothing, with something already in hand: the
            // outboxes have drained.
            break
          }
        }
        if (!cancelled && bestCount <= 0) setData(best)
      } finally {
        if (!cancelled) setLoading(false)
      }
    })()
    return () => {
      cancelled = true
    }
  }, [transactionId])

  return { data, loading }
}

function countRecords(chain: LdvChainResponse | null): number {
  return chain?.logbooks?.reduce((sum, l) => sum + (l.records?.length ?? 0), 0) ?? 0
}
