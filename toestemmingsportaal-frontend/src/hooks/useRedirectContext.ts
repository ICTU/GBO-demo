import { useEffect, useState } from 'react'
import { useSearchParams } from 'react-router-dom'

export type RedirectContext = {
  service: string
  purpose: string
  scope: string[]
  client_oin: string
  client_name: string
  valid_until: string
  return_url: string
}

const STORAGE_KEY = 'gbo.redirect_context'

function parseFromParams(p: URLSearchParams): RedirectContext | null {
  const required = ['service', 'purpose', 'scope', 'client_oin', 'client_name', 'return_url']
  if (required.some((k) => !p.get(k))) return null
  return {
    service: p.get('service')!,
    purpose: p.get('purpose')!,
    scope: p.get('scope')!.split(',').filter(Boolean),
    client_oin: p.get('client_oin')!,
    client_name: p.get('client_name')!,
    valid_until: p.get('valid_until') ?? '',
    return_url: p.get('return_url')!,
  }
}

export function useRedirectContext(): RedirectContext | null {
  const [params] = useSearchParams()
  const [ctx, setCtx] = useState<RedirectContext | null>(() => {
    const fromParams = parseFromParams(params)
    if (fromParams) {
      sessionStorage.setItem(STORAGE_KEY, JSON.stringify(fromParams))
      return fromParams
    }
    const stored = sessionStorage.getItem(STORAGE_KEY)
    return stored ? (JSON.parse(stored) as RedirectContext) : null
  })

  useEffect(() => {
    const fromParams = parseFromParams(params)
    if (fromParams) {
      sessionStorage.setItem(STORAGE_KEY, JSON.stringify(fromParams))
      setCtx(fromParams)
    }
  }, [params])

  return ctx
}

export function clearRedirectContext() {
  sessionStorage.removeItem(STORAGE_KEY)
}
