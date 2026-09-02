import { useEffect, useState } from 'react'
import { fetchLdvChain, type LdvChainResponse } from '../api/devClient'

// The LDV half of the three-standard picture. It hangs off the same
// Fsc-Transaction-Id the FSC txlog and the PDP decision use, so this hook
// takes the transaction id useFscTxlog already resolved rather than
// rediscovering it from the Jaeger trace.
//
// Records are confirmed on write and never sampled, so unlike the Jaeger and
// Loki lookups next to it this one needs no backoff: if the request finished,
// the records are there.
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
        const chain = await fetchLdvChain(transactionId)
        if (!cancelled) setData(chain)
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
