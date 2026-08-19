import { useEffect, useState } from 'react'
import { useSearchParams } from 'react-router'
import {
  parseAllowedReturnOrigins,
  validatedRedirectContext,
  type RedirectContext,
} from './redirectValidation'

export type { RedirectContext } from './redirectValidation'

const STORAGE_KEY = 'gbo.redirect_context'
const DEFAULT_ALLOWED_RETURN_ORIGINS = 'http://localhost:9001'

declare global {
  interface Window {
    __GBO_CONFIG__?: {
      allowedReturnOrigins?: string
    }
  }
}

function configuredReturnOrigins() {
  const configured = window.__GBO_CONFIG__?.allowedReturnOrigins?.trim()
    || import.meta.env.VITE_ALLOWED_RETURN_ORIGINS
    || DEFAULT_ALLOWED_RETURN_ORIGINS
  return parseAllowedReturnOrigins(configured)
}

function parseFromParams(p: URLSearchParams): RedirectContext | null {
  const required = ['service', 'purpose', 'scope', 'client_oin', 'client_name', 'return_url']
  if (required.some((k) => !p.get(k))) return null
  return validatedRedirectContext({
    service: p.get('service')!,
    purpose: p.get('purpose')!,
    scope: p.get('scope')!.split(',').filter(Boolean),
    client_oin: p.get('client_oin')!,
    client_name: p.get('client_name')!,
    valid_until: p.get('valid_until') ?? '',
    return_url: p.get('return_url')!,
  }, configuredReturnOrigins())
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
    if (!stored) return null
    try {
      return validatedRedirectContext(JSON.parse(stored), configuredReturnOrigins())
    } catch {
      return null
    }
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
