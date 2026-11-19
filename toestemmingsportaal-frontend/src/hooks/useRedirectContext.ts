import { useEffect, useState } from 'react'
import { useSearchParams } from 'react-router'
import {
  configuredReturnOrigins,
  validatedRedirectContext,
  type RedirectContext,
} from './redirectValidation.ts'

export type { RedirectContext } from './redirectValidation.ts'

export const REDIRECT_CONTEXT_KEY = 'gbo.redirect_context'

type ContextStorage = Pick<Storage, 'getItem' | 'setItem' | 'removeItem'>

function parseFromParams(p: URLSearchParams, allowedOrigins: Set<string>): RedirectContext | null {
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
  }, allowedOrigins)
}

/**
 * The redirect context of the URL-based flow: from the URL parameters, which
 * are then kept in storage, or else from storage. With ignore (the start of
 * a consent flow from a pushed request) there is none: the parameters are
 * not read, nothing is written and an earlier context is removed, so that
 * it cannot mix with the start flow.
 */
export function loadRedirectContext(
  params: URLSearchParams,
  storage: ContextStorage,
  allowedOrigins: Set<string>,
  ignore = false,
): RedirectContext | null {
  if (ignore) {
    storage.removeItem(REDIRECT_CONTEXT_KEY)
    return null
  }
  const fromParams = parseFromParams(params, allowedOrigins)
  if (fromParams) {
    storage.setItem(REDIRECT_CONTEXT_KEY, JSON.stringify(fromParams))
    return fromParams
  }
  const stored = storage.getItem(REDIRECT_CONTEXT_KEY)
  if (!stored) return null
  try {
    return validatedRedirectContext(JSON.parse(stored), allowedOrigins)
  } catch {
    return null
  }
}

export function useRedirectContext({ ignore = false }: { ignore?: boolean } = {}): RedirectContext | null {
  const [params] = useSearchParams()
  const [ctx, setCtx] = useState<RedirectContext | null>(() =>
    loadRedirectContext(params, sessionStorage, configuredReturnOrigins(), ignore),
  )

  useEffect(() => {
    if (ignore) {
      clearRedirectContext()
      setCtx(null)
      return
    }
    const fromParams = parseFromParams(params, configuredReturnOrigins())
    if (fromParams) {
      sessionStorage.setItem(REDIRECT_CONTEXT_KEY, JSON.stringify(fromParams))
      setCtx(fromParams)
    }
  }, [params, ignore])

  return ctx
}

export function clearRedirectContext() {
  sessionStorage.removeItem(REDIRECT_CONTEXT_KEY)
}
