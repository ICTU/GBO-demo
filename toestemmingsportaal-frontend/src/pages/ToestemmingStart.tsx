import { useCallback, useEffect, useMemo, useReducer } from 'react'
import { useNavigate, useSearchParams } from 'react-router'
import MijnOverheidLayout from '../components/MijnOverheidLayout'
import ToestemmingConsent from './ToestemmingConsent'
import ToestemmingBevestiging from './ToestemmingBevestiging'
import ToestemmingFout from './ToestemmingFout'
import { usePortalToken } from '../hooks/usePortalToken'
import { decideRequest, resolveRequest, type DecisionBody } from '../api/portalClient'
import {
  INITIAL_PHASE,
  START_LOGIN_ROUTE,
  afterFailure,
  nextPhase,
  startAction,
  startCookie,
  startParams,
} from '../consentStart/startFlow'

const secure = () => window.location.protocol === 'https:'

// /toestemming/start?client_id=…&request_uri=…: the start of a consent flow
// from a pushed request. Reads only client_id and request_uri. Without a
// portal login the start URL goes into a short-lived cookie and the citizen
// logs in; after that the backend resolves the reference, the citizen
// decides, and the browser returns to the URL in the backend's answer.
export default function ToestemmingStart() {
  const [params] = useSearchParams()
  const start = useMemo(() => startParams(params), [params])
  const { token, clear } = usePortalToken()
  const navigate = useNavigate()
  const [phase, dispatch] = useReducer(nextPhase, INITIAL_PHASE)

  const toLogin = useCallback(() => {
    if (!start) return
    document.cookie = startCookie(start, secure())
    navigate(START_LOGIN_ROUTE, { replace: true })
  }, [start, navigate])

  useEffect(() => {
    if (phase.kind !== 'resolving') return
    const action = startAction(start, token)
    if (action === 'error') {
      dispatch({ type: 'failed' })
      return
    }
    if (action === 'login') {
      toLogin()
      return
    }
    if (!start || !token) return
    let cancelled = false
    resolveRequest(token, start.requestUri, start.clientId).then(
      (request) => {
        if (!cancelled) dispatch({ type: 'resolved', request })
      },
      (err) => {
        if (cancelled) return
        if (afterFailure(err) === 'relogin') {
          clear()
          toLogin()
        } else {
          dispatch({ type: 'failed' })
        }
      },
    )
    return () => {
      cancelled = true
    }
    // clear is a new function each render; the token it clears is a dependency.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [phase.kind, start, token, toLogin])

  const decide = async (body: DecisionBody) => {
    if (phase.kind !== 'consent' || !start || !token) return
    dispatch({ type: 'deciding' })
    try {
      const answer = await decideRequest(token, start.requestUri, body)
      dispatch({ type: 'decided', answer })
    } catch (err) {
      if (afterFailure(err) === 'relogin') {
        clear()
        toLogin()
      } else {
        dispatch({ type: 'failed' })
      }
    }
  }

  const redirectTo = phase.kind === 'confirmed' ? phase.confirmation.redirectTo : null
  const onReturn = useCallback(() => {
    if (!redirectTo) return
    clear()
    window.location.assign(redirectTo)
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [redirectTo])

  switch (phase.kind) {
    case 'resolving':
      return (
        <MijnOverheidLayout activeNav="toestemmingen" breadcrumb={[{ label: 'Home' }]}>
          <p>Verzoek ophalen…</p>
        </MijnOverheidLayout>
      )
    case 'consent':
    case 'deciding':
      return (
        <ToestemmingConsent
          request={phase.request}
          deciding={phase.kind === 'deciding'}
          onGrant={(scopes) => decide({ decision: 'grant', scopes })}
          onDeny={() => decide({ decision: 'deny' })}
        />
      )
    case 'confirmed':
      return <ToestemmingBevestiging confirmation={phase.confirmation} onReturn={onReturn} />
    case 'error':
      return <ToestemmingFout />
  }
}
