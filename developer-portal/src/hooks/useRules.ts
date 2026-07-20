import { useEffect, useState } from 'react'
import { listRules, type RuleMeta } from '../api/devClient'

// Loads the rule-metadata once at mount. The rule-set is static within
// a policy-bundle revision, so no polling — re-fetch on OPA-bundle-reload
// would require a side-channel; for the demo, refreshing the page is fine.
export function useRules(): { rules: RuleMeta[]; loading: boolean; error: string | null } {
  const [rules, setRules] = useState<RuleMeta[]>([])
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<string | null>(null)

  useEffect(() => {
    let cancelled = false
    listRules()
      .then((rs) => { if (!cancelled) setRules(rs) })
      .catch((e: Error) => { if (!cancelled) setError(e.message) })
      .finally(() => { if (!cancelled) setLoading(false) })
    return () => { cancelled = true }
  }, [])

  return { rules, loading, error }
}
