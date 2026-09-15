import { useEffect, useState } from 'react'
import { fetchDecisions, type PdpDecisions } from '../api/devClient'

// Which parts are still being asked for.
export type PendingParts = { audit: boolean; engine: boolean }

type State = PdpDecisions & {
  pending: PendingParts
  error: string | null
}

const EMPTY: PdpDecisions = { audit: { records: [] }, engine: { decisions: [] } }
const IDLE: PendingParts = { audit: false, engine: false }
const MAX_ATTEMPTS = 12
const cache = new Map<string, PdpDecisions>()

// useDecisions loads what is recorded about the PDP decisions for one FSC
// transaction: the Authorization Decision Log record (audit) and the
// engine's console decision-log entry (observability). The two are fetched
// and retried apart, so an ADL record shows as soon as it is in, whatever
// Loki does. Both arrive late — OpenFTV batches its ADL writes for up to
// five seconds, and promtail ships the console line to Loki — so each part
// is asked again while it has nothing yet, for about seven seconds, and
// stops once it returned data or an error. Only a complete result is cached.
export function useDecisions(transactionId: string | undefined, enabled: boolean): State {
  const [state, setState] = useState<State>(() => ({
    ...((transactionId && cache.get(transactionId)) || EMPTY),
    pending: IDLE,
    error: null,
  }))

  useEffect(() => {
    if (!enabled || !transactionId) return
    const cached = cache.get(transactionId)
    if (cached) {
      setState({ ...cached, pending: IDLE, error: null })
      return
    }
    let cancelled = false
    const timers: ReturnType<typeof setTimeout>[] = []
    const got: PdpDecisions = { ...EMPTY }
    setState({ ...EMPTY, pending: { audit: true, engine: true }, error: null })

    const poll = (part: 'audit' | 'engine', attempt: number) => {
      fetchDecisions(transactionId, { part })
        .then((d) => {
          if (cancelled) return
          const has = part === 'audit' ? d.audit.records.length > 0 : d.engine.decisions.length > 0
          const settled = has || !!d[part].error || attempt >= MAX_ATTEMPTS
          if (part === 'audit') got.audit = d.audit
          else got.engine = d.engine
          if (got.audit.records.length > 0 && got.engine.decisions.length > 0) cache.set(transactionId, { ...got })
          setState((s) => part === 'audit'
            ? { ...s, audit: d.audit, pending: { ...s.pending, audit: !settled } }
            : { ...s, engine: d.engine, pending: { ...s.pending, engine: !settled } })
          if (!settled) timers.push(setTimeout(() => poll(part, attempt + 1), Math.min(150 * attempt, 800)))
        })
        .catch((e: Error) => {
          if (cancelled) return
          setState((s) => ({
            ...s,
            error: e.message,
            pending: part === 'audit' ? { ...s.pending, audit: false } : { ...s.pending, engine: false },
          }))
        })
    }
    poll('audit', 1)
    poll('engine', 1)

    return () => {
      cancelled = true
      timers.forEach(clearTimeout)
    }
  }, [transactionId, enabled])

  return state
}
