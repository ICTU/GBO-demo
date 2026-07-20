import { useCallback, useEffect, useState } from 'react'
import { listConsents, ConsentRecord } from '../api/portalClient'
import { usePortalToken } from './usePortalToken'

type State = {
  consents: ConsentRecord[] | null
  loading: boolean
  error: string | null
}

export function useConsents() {
  const { token } = usePortalToken()
  const [state, setState] = useState<State>({ consents: null, loading: true, error: null })

  const refresh = useCallback(async () => {
    if (!token) {
      setState({ consents: null, loading: false, error: null })
      return
    }
    setState((s) => ({ ...s, loading: true, error: null }))
    try {
      const consents = await listConsents(token)
      setState({ consents, loading: false, error: null })
    } catch (err) {
      setState({ consents: null, loading: false, error: (err as Error).message })
    }
  }, [token])

  useEffect(() => {
    void refresh()
  }, [refresh])

  return { ...state, refresh }
}
