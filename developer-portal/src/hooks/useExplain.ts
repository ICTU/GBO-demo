import { useEffect, useState } from 'react'
import { fetchExplain, type OpaExplainDecision } from '../api/devClient'

type State = {
  decisions: OpaExplainDecision[]
  loading: boolean
  error: string | null
}

const cache = new Map<string, OpaExplainDecision[]>()

// `mode` controls whether the backend re-runs OPA for the explain-trace.
// Default `none` skips the re-run; the UI relies on the policy-emitted
// context.granted / context.reason_admin instead. Use 'full'/'fails' only
// for the raw-trace toggle.
export function useExplain(
  traceId: string | undefined,
  enabled: boolean,
  mode: 'none' | 'fails' | 'full' = 'none',
): State {
  const key = traceId ? `${traceId}:${mode}` : ''
  const [state, setState] = useState<State>(() => ({
    decisions: key && cache.has(key) ? cache.get(key)! : [],
    loading: false,
    error: null,
  }))

  useEffect(() => {
    if (!enabled || !traceId) return
    const cached = cache.get(key)
    if (cached && cached.length > 0) {
      setState({ decisions: cached, loading: false, error: null })
      return
    }
    let cancelled = false
    let attempt = 0
    let timer: ReturnType<typeof setTimeout> | null = null
    setState({ decisions: [], loading: true, error: null })

    // Retry-with-backoff while Loki is still ingesting the freshly-written
    // OPA decision-log line. Caps at ~5s total; only caches a non-empty result.
    const tryFetch = () => {
      attempt++
      fetchExplain(traceId, mode)
        .then((d) => {
          if (cancelled) return
          if (d.length > 0) {
            cache.set(key, d)
            setState({ decisions: d, loading: false, error: null })
            return
          }
          if (attempt >= 8) {
            setState({ decisions: [], loading: false, error: null })
            return
          }
          const delay = Math.min(150 * attempt, 800)
          timer = setTimeout(tryFetch, delay)
        })
        .catch((e: Error) => {
          if (cancelled) return
          setState({ decisions: [], loading: false, error: e.message })
        })
    }
    tryFetch()

    return () => {
      cancelled = true
      if (timer) clearTimeout(timer)
    }
  }, [traceId, enabled, mode, key])

  return state
}
