import { useEffect, useState } from 'react'
import { fetchDecisions, type PdpDecisions } from '../api/devClient'

type State = PdpDecisions & {
  loading: boolean
  error: string | null
}

const EMPTY: PdpDecisions = { audit: { records: [] }, engine: { decisions: [] } }
const cache = new Map<string, PdpDecisions>()

const complete = (d: PdpDecisions) => d.audit.records.length > 0 && d.engine.decisions.length > 0

// useDecisions loads what is recorded about the PDP decisions for one FSC
// transaction: the Authorization Decision Log record (audit) and the
// engine's console decision-log entry (observability). Both arrive late —
// OpenFTV batches its ADL writes for up to five seconds, and promtail ships
// the console line to Loki — so it retries for about seven seconds, and
// caches only once both are in.
export function useDecisions(transactionId: string | undefined, enabled: boolean): State {
  const [state, setState] = useState<State>(() => ({
    ...(transactionId && cache.get(transactionId)) || EMPTY,
    loading: false,
    error: null,
  }))

  useEffect(() => {
    if (!enabled || !transactionId) return
    const cached = cache.get(transactionId)
    if (cached) {
      setState({ ...cached, loading: false, error: null })
      return
    }
    let cancelled = false
    let attempt = 0
    let timer: ReturnType<typeof setTimeout> | null = null
    setState({ ...EMPTY, loading: true, error: null })

    const tryFetch = () => {
      attempt++
      fetchDecisions(transactionId)
        .then((d) => {
          if (cancelled) return
          if (complete(d)) {
            cache.set(transactionId, d)
            setState({ ...d, loading: false, error: null })
            return
          }
          if (attempt >= 12) {
            setState({ ...d, loading: false, error: null })
            return
          }
          timer = setTimeout(tryFetch, Math.min(150 * attempt, 800))
        })
        .catch((e: Error) => {
          if (cancelled) return
          setState({ ...EMPTY, loading: false, error: e.message })
        })
    }
    tryFetch()

    return () => {
      cancelled = true
      if (timer) clearTimeout(timer)
    }
  }, [transactionId, enabled])

  return state
}
