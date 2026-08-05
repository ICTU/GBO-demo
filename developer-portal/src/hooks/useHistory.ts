import { useCallback, useEffect, useState } from 'react'
import { listHistory, logHistory } from '../api/devClient'
import { demoSessionId } from '../util/demoSession'
import type { HistoryRun } from '../types'

// This session's runs plus every unattributable one. Not only a display: the
// Use-form prefills its consent_id from the most recent issuance here, so an
// unfiltered timeline would hand one developer a colleague's consent.
export function useHistory() {
  const [history, setHistory] = useState<HistoryRun[]>([])
  const [loading, setLoading] = useState(true)

  const refresh = useCallback(async () => {
    setLoading(true)
    try {
      setHistory(await listHistory(demoSessionId()))
    } catch {
      // silent fail in demo
    } finally {
      setLoading(false)
    }
  }, [])

  useEffect(() => { void refresh() }, [refresh])

  // Poll periodically so externally-triggered runs (e.g. burger flows from
  // the toestemmingsportaal) appear without the user manually refreshing.
  useEffect(() => {
    const id = setInterval(() => { void refresh() }, 5000)
    return () => clearInterval(id)
  }, [refresh])

  const append = useCallback(
    async (run: Omit<HistoryRun, 'run_id' | 'ts'>) => {
      await logHistory(run)
      await refresh()
    },
    [refresh],
  )

  return { history, loading, refresh, append }
}
