import { useCallback, useEffect, useState } from 'react'
import { deleteScenario, listScenarios, saveScenario } from '../api/devClient'
import type { Scenario } from '../types'

export function useScenarios() {
  const [scenarios, setScenarios] = useState<Scenario[]>([])
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<string | null>(null)

  const refresh = useCallback(async () => {
    setLoading(true)
    try {
      const data = await listScenarios()
      setScenarios(data)
      setError(null)
    } catch (err) {
      setError((err as Error).message)
    } finally {
      setLoading(false)
    }
  }, [])

  useEffect(() => { void refresh() }, [refresh])

  const save = useCallback(
    async (s: Omit<Scenario, 'user_saved'>) => {
      await saveScenario(s)
      await refresh()
    },
    [refresh],
  )
  const remove = useCallback(
    async (id: string) => {
      await deleteScenario(id)
      await refresh()
    },
    [refresh],
  )

  return { scenarios, loading, error, refresh, save, remove }
}
