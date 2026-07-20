import { useEffect, useRef, useState } from 'react'
import type { Tab } from '../types'

type WatchEvent = { trace_id: string; service: string }

// Map the originating service (= first span seen for a new trace) to the
// tab/mode the dev-portal should switch into. Burger-FE flows always enter
// via one of these three backends.
function modeForService(service: string): Tab | null {
  if (service === 'consent-portal-backend') return 'issuance'
  if (service === 'dienstverlener-backend') return 'use'
  // Wallet-side (wallet/demo-issuer/issuance-server) has no OTel; the first
  // GBO-side span for an EUDI-flow is always eudi-adapter.
  if (service === 'eudi-adapter') return 'eudi-issuance'
  return null
}

// Subscribes to /api/dev/watch-next. As long as `active`, fires the callback
// for every new trace the hub sees (burger-FE, afnemer-mock, curl, …) and
// auto-reconnects after each event — so one click on "watch" keeps catching
// subsequent flows until the user toggles it off. Backend is one-shot per
// connection; this hook re-arms by reopening the EventSource.
export function useWatchNext(
  active: boolean,
  onTrace: (traceId: string, mode: Tab | null) => void,
) {
  const [error, setError] = useState<string | null>(null)
  const cbRef = useRef(onTrace)
  cbRef.current = onTrace

  useEffect(() => {
    if (!active) return
    setError(null)
    let cancelled = false
    let es: EventSource | null = null

    const connect = () => {
      if (cancelled) return
      es = new EventSource('/api/dev/watch-next')
      es.addEventListener('trace', (e) => {
        const data = JSON.parse((e as MessageEvent).data) as WatchEvent
        cbRef.current(data.trace_id, modeForService(data.service))
        es?.close()
        // brief gap before reopening so we don't race the same trace event
        setTimeout(connect, 50)
      })
      es.onerror = () => {
        es?.close()
        if (cancelled) return
        setError('verbinding verbroken — opnieuw verbinden…')
        setTimeout(connect, 1000)
      }
    }
    connect()

    return () => {
      cancelled = true
      es?.close()
    }
  }, [active])

  return { error }
}
