import { useEffect, useRef, useState } from 'react'
import Header from './components/Header'
import ScenarioLibrary from './components/ScenarioLibrary'
import RequestBuilder from './components/RequestBuilder'
import ResultPanel, { type ResultData } from './components/ResultPanel'
import ArchStrip from './components/ArchStrip'
import EudiQrPanel from './components/EudiQrPanel'
import FscTxlogPanel from './components/FscTxlogPanel'
import LdvPanel from './components/LdvPanel'
import { useScenarios } from './hooks/useScenarios'
import { useFscTxlog } from './hooks/useFscTxlog'
import { useLdvChain } from './hooks/useLdvChain'
import { useHistory } from './hooks/useHistory'
import { useReferenceData } from './hooks/useReferenceData'
import { useChainEvents } from './hooks/useChainEvents'
import { useWatchNext } from './hooks/useWatchNext'
import { issuanceFlow } from './api/portalClient'
import { useQuery } from './api/dvtpClient'
import { fetchDecisions } from './api/devClient'
import { createAsiRequest, getAsiRequest, listAsiAttributes } from './api/asiClient'
import { expectedOf, requestBody, traceOf, type AsiAttribute, type AsiRequestView } from './lib/asiRequest'
import { newTraceContext } from './util/trace'
import { consentTokenFor, storeConsentToken } from './util/consentTokens'
import { loadIssuanceOffers, type IssuanceOffer } from './eudi'
import { consumerFor, DEFAULT_VBO_ID, isLvgScope } from './util/consumer'
import {
  viaFsc,
  type ApiCall, type AsiPayload, type EudiPayload, type HistoryRun, type IssuancePayload, type IssuanceResponse, type Scenario,
  type Tab, type UsePayload, type UseResponse,
} from './types'

const EMPTY_ISSUANCE: IssuancePayload = {
  citizen_bsn: '',
  dienstverlener_oin: '',
  scopes: [],
  validity_seconds: 7776000,
  use_case: 'hypotheek',
}

const EMPTY_USE: UsePayload = {
  consent_id: '',
  scope_id: 'bd:ib:2025',
  belastingjaren: [2025],
}

// scopeYears extracts belastingjaren from scopes of the form bd:ib:<year>.
function scopeYears(scopes: string[]): number[] {
  return scopes
    .map((s) => /^bd:ib:(\d{4})$/.exec(s)?.[1])
    .filter((y): y is string => !!y)
    .map(Number)
    .sort((a, b) => a - b)
}

// opaReasonCode returns the reason code of a denial as the Authorization
// Decision Log recorded it (YEAR_NOT_COVERED, CONSENT_SCOPE_MISMATCH, ...),
// looked up by Fsc-Transaction-Id. OpenFTV batches its ADL writes for up to
// five seconds, so retry briefly before giving up.
async function opaReasonCode(transactionId: string): Promise<string | null> {
  for (let attempt = 0; attempt < 8; attempt++) {
    try {
      const { audit } = await fetchDecisions(transactionId, { part: 'audit' })
      for (const r of audit.records) {
        if (r.reason) return r.reason
      }
      if (audit.records.length > 0) return null
    } catch {
      // not in the ADL yet — retry
    }
    await new Promise((r) => setTimeout(r, 800))
  }
  return null
}

const EMPTY_EUDI: EudiPayload = {
  usecase: '',
}

const EMPTY_ASI: AsiPayload = {
  operation: 'verify',
  attributes: [],
}

// How long the portal follows an ASI request: as long as the QTSP keeps it.
const ASI_FOLLOW_MS = 10 * 60 * 1000

// asiResult is the result panel's view of an ASI request at the QTSP. The
// outcome is the citizen's and the ASIP's, not a PDP verdict, so the panel
// shows it in its own block (result.asi) rather than as ALLOW or DENY.
function asiResult(view: AsiRequestView): ResultData {
  return {
    outcome: view.status === 'failed' ? 'error' : 'unknown',
    status: 200,
    body: view,
    trace_id: traceOf(view) ?? undefined,
    asi: view,
  }
}

const JAEGER_PUBLIC_URL =
  window.__GBO_RUNTIME_CONFIG__?.jaegerPublicUrl ||
  import.meta.env.VITE_JAEGER_PUBLIC_URL ||
  'http://localhost:9686'

const GRAFANA_PUBLIC_URL =
  window.__GBO_RUNTIME_CONFIG__?.grafanaPublicUrl ||
  import.meta.env.VITE_GRAFANA_PUBLIC_URL ||
  'http://localhost:9300'

export default function App() {
  const { scenarios, loading: scLoading, error: scError, remove, save } = useScenarios()
  const { history, append: appendHistory, refresh: refreshHistory } = useHistory()
  const { citizens, organizations } = useReferenceData()

  const [tab, setTab] = useState<Tab>('issuance')
  const [issuancePayload, setIssuancePayload] = useState<IssuancePayload>(EMPTY_ISSUANCE)
  const [usePayload, setUsePayload] = useState<UsePayload>(EMPTY_USE)
  const [eudiPayload, setEudiPayload] = useState<EudiPayload>(EMPTY_EUDI)
  const [eudiOffers, setEudiOffers] = useState<IssuanceOffer[]>([])
  const [eudiOffersError, setEudiOffersError] = useState('')
  const [asiPayload, setAsiPayload] = useState<AsiPayload>(EMPTY_ASI)
  const [asiAttributes, setAsiAttributes] = useState<AsiAttribute[]>([])
  const [asiAttributesError, setAsiAttributesError] = useState('')
  const [asiView, setAsiView] = useState<AsiRequestView | null>(null)
  // The ASI request this portal laid down and still follows. A new request
  // replaces the old one, which then stops being followed; once it ends, the
  // watch shows other QTSPs' requests again.
  const asiOwnRef = useRef<string | null>(null)
  const [result, setResult] = useState<ResultData | null>(null)
  const [submitting, setSubmitting] = useState(false)
  const [archApiCalls, setArchApiCalls] = useState<ApiCall[] | undefined>(undefined)
  const [archTraceId, setArchTraceId] = useState<string | null>(null)
  const [archMode, setArchMode] = useState<Tab>('issuance')

  useEffect(() => {
    void loadIssuanceOffers()
      .then((offers) => {
        setEudiOffers(offers)
        setEudiPayload((current) => ({
          ...current,
          usecase: offers.some((offer) => offer.key === current.usecase)
            ? current.usecase
            : offers[0].key,
        }))
      })
      .catch((error: Error) => setEudiOffersError(error.message))
    void listAsiAttributes()
      .then((attributes) => {
        setAsiAttributes(attributes)
        // Like the QTSP's own form: the first attribute, with its example.
        setAsiPayload((current) => current.attributes.length > 0 || attributes.length === 0
          ? current
          : { ...current, attributes: [{ identifier: attributes[0].identifier, value: JSON.stringify(attributes[0].example) }] })
      })
      .catch((error: Error) => setAsiAttributesError(error.message))
  }, [])

  const { states: archStates, ready: archReady, bron: archBron, consumer: archConsumer } = useChainEvents(archTraceId, archMode)
  const { data: fscTxlog, loading: fscTxlogLoading, transactionId: fscTxID, overrides: fscOverrides } = useFscTxlog(
    archTraceId ?? undefined, archMode,
  )
  // The LDV records hang off the same Fsc-Transaction-Id, so this reuses the
  // id the txlog hook already resolved instead of finding it again.
  // LDV records carry the request's trace id, not the transaction id the
  // txlog and the decision log key on.
  const { data: ldvChain, loading: ldvLoading } = useLdvChain(archTraceId)
  // FSC-nodes come from the txlog (audit-proof replaces the 'no-otel'
  // fallback); pdp/opa come from the cross-trace-lookup on
  // Fsc-Transaction-Id. Applies to both flows that hit pdp-service via
  // FSC-Inway (EUDI and DvTP).
  const mergedStates = viaFsc(archMode)
    ? { ...archStates, ...fscOverrides.states }
    : archStates

  // Watch-mode — lets the dev-portal pick up every new citizen/consumer flow.
  // Stays on until the user turns it off, so successive issuance → use flows
  // appear automatically without re-clicking.
  const [watching, setWatching] = useState(false)
  // While the playground-curl is running we do NOT want the watch-callback
  // to jump to the result-tab — that would hide the user's own curl-response.
  // Using a ref (not state) so we can read it inside the watch-closure
  // without triggering a re-render.
  const playgroundActiveRef = useRef(false)
  // Playground-active also mirrored in state (next to the ref): triggers a
  // re-render so the QR-panel-guard can read it (a ref alone wouldn't rerender).
  const [playgroundActive, setPlaygroundActive] = useState(false)
  // Trace_id of a watched run whose history-bubble we're still waiting for
  // (that's how the citizen/consumer FE ships the response to us).
  const [pendingWatchedTrace, setPendingWatchedTrace] = useState<string | null>(null)
  // The hub handed this run to several sessions at once, so what the user is
  // looking at may be someone else's — say so rather than pretend.
  const [watchShared, setWatchShared] = useState(false)
  // Only while the wallet-QR is up: that flow starts out-of-band, so the
  // usecase is all that ties an incoming trace back to this session.
  const watchUsecase = tab === 'eudi-issuance' ? eudiPayload.usecase : null
  const { error: watchError } = useWatchNext(watching, watchUsecase, (traceId, mode, shared) => {
    setWatchShared(shared)
    // EUDI: this IS the trace we've been waiting for since the user opened
    // the QR-page. Release the "wachten op wallet…" button and log locally
    // (no bubble-POST from the adapter — history-entry is portal-side only).
    if (mode === 'eudi-issuance') {
      // Playground-curl: user is on the QR-panel running the bypass-adapter.
      // The QR-panel + playground-response must stay visible, but the
      // arch-strip + submit-button do need to update (otherwise it looks
      // like the flow never ran).
      if (playgroundActiveRef.current) {
        // Light up the arch-strip only; keep submitting=true (so the
        // QR-panel stays open via its own guard) and skip the result-flip
        // so the playground-response stays visible. The user clicks
        // 'cancel' in the QR-panel when done.
        setArchApiCalls(undefined)
        setArchTraceId(traceId)
        setArchMode('eudi-issuance')
        return
      }
      setArchApiCalls(undefined)
      // Outcome is deliberately left as 'unknown'. The trace-context
      // breaks at bd-inway (FSC has no OTel), so this root-span carries
      // no decision-outcome. The real allow/deny sits in the OPA
      // decision-log (queryable via input.fsc.transaction_id) or in the
      // per-hop FSC-txlog — the UI points there for per-hop evidence.
      setResult({
        outcome: 'unknown',
        status: 0,
        body: { trace_id: traceId, usecase: eudiPayload.usecase },
        trace_id: traceId,
        narrative: 'EUDI-flow gedetecteerd. Zie 3.5 FSC-transactielog voor per-hop-bewijs; PDP-decision (allow/deny) kan via Jaeger op tag gbo.fsc.transaction_id worden opgezocht.',
      })
      setArchTraceId(traceId)
      setArchMode('eudi-issuance')
      setTab('eudi-issuance')
      setSubmitting(false)
      void appendHistory({
        scenario_name: 'EUDI issuance (wallet)',
        tab: 'eudi-issuance',
        payload: eudiPayload,
        trace_id: traceId,
        outcome: 'unknown',
      })
      return
    }
    // ASI: a QTSP's verify or retrieve at the ASIP. While this portal follows
    // a request of its own, the chain comes from the trace the QTSP reports
    // for it, so any ASI trace the watch sees meanwhile is someone else's and
    // is left alone. For other QTSP requests there is no response to show,
    // and the chain, the FSC transaction log and the PDP decision are the
    // evidence.
    if (mode === 'asi-verify') {
      if (asiOwnRef.current || submitting) return
      setArchApiCalls(undefined)
      setResult({
        outcome: 'unknown',
        status: 0,
        body: { trace_id: traceId },
        trace_id: traceId,
        narrative: 'ASI-verzoek van een QTSP gedetecteerd. Het antwoord gaat naar de QTSP; zie de keten, het FSC-transactielog en de PDP-beslissing per hop.',
      })
      setArchTraceId(traceId)
      setArchMode('asi-verify')
      setTab('asi-verify')
      return
    }
    // DvTP paths: if the user just clicked Submit themselves
    // (submitting=true), don't overwrite — the local run wins; watch will
    // pick things back up next round once a real citizen/consumer-flow
    // arrives.
    if (submitting) return
    setArchApiCalls(undefined)
    setResult(null)
    setArchTraceId(traceId)
    if (mode) { setArchMode(mode); setTab(mode) }
    setPendingWatchedTrace(traceId)
    // Snappy: fire immediately + again after 500ms/2s so the bubble-POST
    // shows up quickly (the 5s-polling would otherwise be too slow).
    void refreshHistory()
    setTimeout(() => { void refreshHistory() }, 500)
    setTimeout(() => { void refreshHistory() }, 2000)
  })

  // When the watched trace's history-entry arrives: build the result.
  useEffect(() => {
    if (!pendingWatchedTrace) return
    const entry = history.find((h) => h.trace_id === pendingWatchedTrace && h.response)
    if (!entry || !entry.response) return
    const isIssuance = entry.tab === 'issuance'
    const body = entry.response
    setResult({
      outcome: entry.outcome,
      status: 200,
      body,
      trace_id: entry.trace_id,
      reason: !isIssuance ? (body as UseResponse).reason : undefined,
      narrative: isIssuance
        ? 'Burger heeft toestemming verleend — consent ligt in S01.'
        : entry.outcome === 'allow'
          ? 'Afnemer-query toegestaan — bron heeft data teruggegeven.'
          : 'Afnemer-query geweigerd door PDP.',
    })
    if (isIssuance) {
      setArchApiCalls((body as IssuanceResponse).api_calls)
    }
    setPendingWatchedTrace(null)
  }, [history, pendingWatchedTrace])

  // Clear trace_id on tab/mode switch so we don't paint old spans onto the other chain.
  const handleSetTab = (t: Tab) => {
    if (t !== tab) { setArchTraceId(null); setArchApiCalls(undefined) }
    setTab(t); setArchMode(t)
  }
  const handleSetArchMode = (m: Tab) => {
    if (m !== archMode) { setArchTraceId(null); setArchApiCalls(undefined) }
    setArchMode(m); setTab(m)
  }

  const onLoad = (s: Scenario) => {
    handleSetTab(s.tab)
    if (s.tab === 'issuance') {
      setIssuancePayload({ ...EMPTY_ISSUANCE, ...(s.payload as IssuancePayload) })
    } else if (s.tab === 'eudi-issuance') {
      setEudiPayload({ ...EMPTY_EUDI, ...(s.payload as EudiPayload) })
    } else if (s.tab === 'asi-verify') {
      setAsiPayload({ ...EMPTY_ASI, ...(s.payload as AsiPayload) })
    } else {
      const scenarioUse = s.payload as UsePayload
      // A consent is only usable through its own consumer: the scenario's
      // scope picks that consumer, and a consent issued for the other one
      // would be refused on actor binding rather than show what the
      // scenario is about. Within the consumer any consent will do — deny
      // scenarios deliberately ask for a scope it does not cover.
      const consumer = consumerFor(scenarioUse.scope_id)
      const sameConsumer = (h: HistoryRun) =>
        ((h.payload as IssuancePayload).scopes ?? []).some((s) => consumerFor(s) === consumer)
      const latestIssued = history.find(
        (h) => h.tab === 'issuance' && h.outcome === 'allow' && h.consent_id && sameConsumer(h),
      )?.consent_id ?? ''
      const currentCid = consumerFor(usePayload.scope_id) === consumer ? usePayload.consent_id : ''
      const effectiveCid = scenarioUse.consent_id || currentCid || latestIssued
      setUsePayload({
        ...EMPTY_USE,
        ...scenarioUse,
        consent_id: effectiveCid,
        consent_token: scenarioUse.consent_token || consentTokenFor(effectiveCid),
      })
    }
  }

  const onLoadFromHistory = (run: { tab: Tab; payload: IssuancePayload | UsePayload | EudiPayload | AsiPayload }) => {
    handleSetTab(run.tab)
    if (run.tab === 'issuance') {
      setIssuancePayload({ ...EMPTY_ISSUANCE, ...(run.payload as IssuancePayload) })
    } else if (run.tab === 'eudi-issuance') {
      setEudiPayload({ ...EMPTY_EUDI, ...(run.payload as EudiPayload) })
    } else if (run.tab === 'asi-verify') {
      setAsiPayload({ ...EMPTY_ASI, ...(run.payload as AsiPayload) })
    } else {
      const p = run.payload as UsePayload
      setUsePayload({ ...EMPTY_USE, ...p, consent_token: p.consent_token || consentTokenFor(p.consent_id) })
    }
    window.scrollTo({ top: 0, behavior: 'smooth' })
  }

  const onSubmit = async () => {
    // EUDI-flow start externally (wallet scans QR). Dev-portal has no
    // trace_id to seed — it renders the QR (universal-link client-side
    // assembled), enables watch, and waits for eudi-adapter's first span
    // (useWatchNext handles this).
    // ASI: lay the request down at the reference QTSP, send a new tab to its
    // start URL (the citizen goes to the authorization server there), and
    // follow the outcome. The tab is opened in the click, so the browser
    // allows it, and gets its address once the QTSP answers.
    if (tab === 'asi-verify') {
      const labels = Object.fromEntries(asiAttributes.map((a) => [a.identifier, a.label]))
      const built = requestBody(asiPayload, labels)
      if ('error' in built) {
        setResult({ outcome: 'error', status: 0, body: { error: built.error }, narrative: built.error })
        return
      }
      const win = window.open('about:blank', '_blank')
      asiOwnRef.current = null
      setAsiView(null)
      setResult(null)
      setArchApiCalls(undefined)
      setArchTraceId(null)
      setArchMode('asi-verify')
      if (!watching) setWatching(true)
      setSubmitting(true)
      let view: AsiRequestView
      try {
        view = await createAsiRequest(built.body)
      } catch (err) {
        win?.close()
        setResult({
          outcome: 'error',
          status: 0,
          body: { error: (err as Error).message },
          narrative: `De QTSP nam de aanvraag niet aan: ${(err as Error).message}`,
        })
        return
      } finally {
        setSubmitting(false)
      }
      if (win) {
        win.opener = null
        win.location.href = view.start_url
      } else {
        window.open(view.start_url, '_blank', 'noopener')
      }
      const id = view.id
      asiOwnRef.current = id
      setAsiView(view)
      setResult(asiResult(view))
      const deadline = Date.now() + ASI_FOLLOW_MS
      while (view.status === 'pending' && Date.now() < deadline) {
        await new Promise((r) => setTimeout(r, 1000))
        if (asiOwnRef.current !== id) return
        try {
          view = await getAsiRequest(id)
        } catch {
          // The QTSP was briefly unreachable: try again until the deadline.
        }
      }
      if (asiOwnRef.current !== id) return
      asiOwnRef.current = null
      setAsiView(view)
      setResult(asiResult(view))
      const traceId = traceOf(view)
      if (traceId) {
        setArchApiCalls(undefined)
        setArchTraceId(traceId)
        setArchMode('asi-verify')
      }
      return
    }
    if (tab === 'eudi-issuance') {
      setResult(null)
      setArchApiCalls(undefined)
      setArchTraceId(null)
      setArchMode('eudi-issuance')
      setSubmitting(true)
      if (!watching) setWatching(true)
      // submitting resets when the watched trace arrives (see useWatchNext-
      // callback below) or when the user cancels via the QR-panel.
      return
    }

    const ctx = newTraceContext()
    setSubmitting(true)
    setResult(null)
    setArchApiCalls(undefined)
    setArchTraceId(ctx.traceId)
    setArchMode(tab)
    try {
      if (tab === 'issuance') {
        const res: IssuanceResponse = await issuanceFlow(issuancePayload, ctx.header)
        setResult({
          outcome: 'allow',
          status: 200,
          body: res,
          trace_id: res.trace_id ?? ctx.traceId,
          narrative: 'Consent vastgelegd in S01 — afnemer kan binnen scope opvragen.',
        })
        setArchApiCalls(res.api_calls)
        storeConsentToken(res.consent_id, res.consent_token)
        await appendHistory({
          scenario_name: 'Custom issuance',
          tab: 'issuance',
          payload: issuancePayload,
          trace_id: res.trace_id ?? ctx.traceId,
          outcome: 'allow',
          consent_id: res.consent_id,
        })
        // Prefill the use-form from the issued consent: scope_id = first
        // scope, belastingjaren = every bd:ib:<year> scope — so a
        // both-years consent yields a both-years query by default.
        const issuedYears = scopeYears(issuancePayload.scopes)
        const lvg = issuancePayload.scopes.find(isLvgScope)
        setUsePayload((u) => ({
          ...u,
          consent_id: res.consent_id,
          consent_token: res.consent_token,
          scope_id: lvg ?? issuancePayload.scopes[0] ?? u.scope_id,
          belastingjaren: issuedYears.length > 0 ? issuedYears : u.belastingjaren,
          vbo_id: lvg ? u.vbo_id || DEFAULT_VBO_ID : u.vbo_id,
        }))
      } else {
        const res: UseResponse = await useQuery(usePayload, ctx.header)
        const outcome = res.allowed ? 'allow' : 'deny'
        setResult({
          outcome,
          status: 200,
          body: res,
          trace_id: res.trace_id ?? ctx.traceId,
          reason: res.reason,
          narrative: res.allowed
            ? 'Query toegestaan — bron heeft data teruggegeven.'
            : 'Query geweigerd door PDP. Reden hieronder.',
        })
        const historyPayload = { ...usePayload }
        delete historyPayload.consent_token
        await appendHistory({
          scenario_name: 'Custom use',
          tab: 'use',
          payload: historyPayload,
          trace_id: res.trace_id ?? ctx.traceId,
          outcome,
        })
        // On deny: enrich with the OPA reason-code (YEAR_NOT_COVERED,
        // CONSENT_SCOPE_MISMATCH, …). The FSC-inway only returns a bare
        // 401; the real reason lives in the OPA decision log, shipped
        // via Loki — hence a short retry loop while promtail catches up.
        // Look the decision up by the identifier that actually reached
        // OPA: the FSC transaction-id when the backend reports one.
        const decisionId = res.fsc_transaction_id ?? res.trace_id
        if (!res.allowed && decisionId) {
          const code = await opaReasonCode(decisionId)
          if (code) {
            setResult((r) =>
              r ? { ...r, reason: `${code}${res.reason ? ` — ${res.reason}` : ''}` } : r,
            )
          }
        }
      }
    } catch (err) {
      setResult({
        outcome: 'error',
        status: 0,
        body: { error: (err as Error).message },
        trace_id: ctx.traceId,
        narrative: 'Netwerk- of backend-fout. Controleer de logs.',
      })
    } finally {
      setSubmitting(false)
    }
  }

  const onSaveAsScenario = async () => {
    const name = prompt('Naam voor dit scenario?')
    if (!name) return
    const desc = prompt('Korte beschrijving?') ?? ''
    await save({
      id: '',
      name,
      desc,
      tab,
      expected_outcome:
        tab === 'asi-verify' ? (asiView ? expectedOf(asiView) : undefined)
        : result?.outcome === 'error' ? 'allow' : (result?.outcome ?? 'allow'),
      payload:
        tab === 'issuance' ? issuancePayload
        : tab === 'eudi-issuance' ? eudiPayload
        : tab === 'asi-verify' ? asiPayload
        : usePayload,
    } as Scenario)
  }

  return (
    <>
      <Header />
      <div className="wrap">
        <ArchStrip
          mode={archMode}
          setMode={handleSetArchMode}
          states={mergedStates}
          apiCalls={archApiCalls}
          traceId={archTraceId ?? undefined}
          pdpTraceIdOverride={fscOverrides.decisionTraceKey ?? undefined}
          bron={archBron}
          // The trace names its consumer once its backend reports; until
          // then, the one the use form would go through.
          consumer={archConsumer ?? consumerFor(usePayload.scope_id)}
          watching={watching}
          onToggleWatch={() => { setWatchShared(false); setWatching((w) => !w) }}
          watchError={watchError}
          watchShared={watchShared}
          jaegerUrl={JAEGER_PUBLIC_URL}
          grafanaUrl={GRAFANA_PUBLIC_URL}
        />

        {viaFsc(archMode) && (
          <>
            <FscTxlogPanel data={fscTxlog} transactionId={fscTxID} loading={fscTxlogLoading} />
            <LdvPanel data={ldvChain} traceId={archTraceId} loading={ldvLoading} />
          </>
        )}

        <div className="grid4">
          <div className="col">
            <ScenarioLibrary
              scenarios={scenarios}
              loading={scLoading}
              error={scError}
              onLoad={onLoad}
              onDelete={remove}
            />
          </div>
          <div className="col">
            <RequestBuilder
              tab={tab}
              setTab={handleSetTab}
              issuancePayload={issuancePayload}
              setIssuancePayload={setIssuancePayload}
              usePayload={usePayload}
              setUsePayload={setUsePayload}
              eudiPayload={eudiPayload}
              setEudiPayload={setEudiPayload}
              citizens={citizens}
              organizations={organizations}
              history={history}
              onSubmit={onSubmit}
              onSaveAsScenario={onSaveAsScenario}
              submitting={submitting}
              eudiOffers={eudiOffers}
              eudiOffersError={eudiOffersError}
              asiPayload={asiPayload}
              setAsiPayload={setAsiPayload}
              asiAttributes={asiAttributes}
              asiAttributesError={asiAttributesError}
            />
          </div>
          <div className="col">
            {tab === 'eudi-issuance' && submitting && (!archTraceId || playgroundActive) ? (
              <EudiQrPanel
                usecaseKey={eudiPayload.usecase}
                offers={eudiOffers}
                onCancel={() => { setSubmitting(false); setPlaygroundActive(false) }}
                onPlaygroundToggle={(active) => {
                  playgroundActiveRef.current = active
                  setPlaygroundActive(active)
                }}
              />
            ) : (
              <ResultPanel
                result={archReady ? result : null}
                pending={!archReady && (submitting || result !== null)}
                jaegerUrl={JAEGER_PUBLIC_URL}
                grafanaUrl={GRAFANA_PUBLIC_URL}
              />
            )}
          </div>
        </div>

        <div className="panel">
          <div className="panel-h">
            <span className="t"><span className="n">3.4</span>History · replay</span>
          </div>
          {history.length === 0 ? (
            <div className="empty" style={{ padding: '26px' }}>Nog geen runs gelogd.</div>
          ) : (
            <table>
              <thead>
                <tr>
                  <th>Tijd</th>
                  <th>Scenario</th>
                  <th>Tab</th>
                  <th>Uitkomst</th>
                  <th>Trace</th>
                  <th>Acties</th>
                </tr>
              </thead>
              <tbody>
                {history.map((h) => (
                  <tr key={h.run_id}>
                    <td className="mono" style={{ color: 'var(--mute)' }}>
                      {new Date(h.ts).toLocaleTimeString('nl-NL', { hour: '2-digit', minute: '2-digit', second: '2-digit' })}
                    </td>
                    <td style={{ fontWeight: 600 }}>{h.scenario_name}</td>
                    <td><span className="chip tab">{h.tab}</span></td>
                    <td><span className={`hist-out ${h.outcome}`}>{h.outcome.toUpperCase()}</span></td>
                    <td><span className="tlink">{h.trace_id ? h.trace_id.slice(0, 12) + '…' : '—'}</span></td>
                    <td>
                      <button
                        className="mini"
                        title="Laadt deze payload terug in de builder zodat je 'm kunt aanpassen vóór opnieuw verzenden"
                        onClick={() => onLoadFromHistory({ tab: h.tab, payload: h.payload })}
                      >
                        ↺ Laad
                      </button>
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          )}
        </div>
      </div>
    </>
  )
}
