import { useEffect, useRef, useState } from 'react'
import Header from './components/Header'
import ScenarioLibrary from './components/ScenarioLibrary'
import RequestBuilder from './components/RequestBuilder'
import ResultPanel, { type ResultData } from './components/ResultPanel'
import ArchStrip from './components/ArchStrip'
import EudiQrPanel from './components/EudiQrPanel'
import FscTxlogPanel from './components/FscTxlogPanel'
import { useScenarios } from './hooks/useScenarios'
import { useFscTxlog } from './hooks/useFscTxlog'
import { useHistory } from './hooks/useHistory'
import { useReferenceData } from './hooks/useReferenceData'
import { useChainEvents } from './hooks/useChainEvents'
import { useWatchNext } from './hooks/useWatchNext'
import { issuanceFlow } from './api/portalClient'
import { useQuery } from './api/dvtpClient'
import { newTraceContext } from './util/trace'
import type {
  ApiCall, EudiPayload, IssuancePayload, IssuanceResponse, Scenario, Tab, UsePayload, UseResponse,
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

const EMPTY_EUDI: EudiPayload = {
  usecase: 'inkomensverklaring_2024',
}

export default function App() {
  const { scenarios, loading: scLoading, error: scError, remove, save } = useScenarios()
  const { history, append: appendHistory, refresh: refreshHistory } = useHistory()
  const { citizens, organizations } = useReferenceData()

  const [tab, setTab] = useState<Tab>('issuance')
  const [issuancePayload, setIssuancePayload] = useState<IssuancePayload>(EMPTY_ISSUANCE)
  const [usePayload, setUsePayload] = useState<UsePayload>(EMPTY_USE)
  const [eudiPayload, setEudiPayload] = useState<EudiPayload>(EMPTY_EUDI)
  const [result, setResult] = useState<ResultData | null>(null)
  const [submitting, setSubmitting] = useState(false)
  const [archApiCalls, setArchApiCalls] = useState<ApiCall[] | undefined>(undefined)
  const [archTraceId, setArchTraceId] = useState<string | null>(null)
  const [archMode, setArchMode] = useState<Tab>('issuance')

  const { states: archStates, ready: archReady } = useChainEvents(archTraceId, archMode)
  const { data: fscTxlog, loading: fscTxlogLoading, transactionId: fscTxID, overrides: fscOverrides } = useFscTxlog(
    archTraceId ?? undefined, archMode,
  )
  // FSC-nodes come from the txlog (audit-proof replaces the 'no-otel'
  // fallback); pdp/opa come from the cross-trace-lookup on
  // Fsc-Transaction-Id. Applies to both flows that hit pdp-service via
  // FSC-Inway (EUDI and DvTP).
  const mergedStates = archMode === 'eudi-issuance' || archMode === 'use'
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
  const { error: watchError } = useWatchNext(watching, (traceId, mode) => {
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
    // DvTP paths: if the user just clicked Submit themselves (submitting=true),
    // don't overwrite — the local run wins; watch will pick things back up
    // next round once a real citizen/consumer-flow arrives.
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
    } else {
      const scenarioUse = s.payload as UsePayload
      const latestIssued = history.find(
        (h) => h.tab === 'issuance' && h.outcome === 'allow' && h.consent_id,
      )?.consent_id ?? ''
      const effectiveCid = scenarioUse.consent_id || usePayload.consent_id || latestIssued
      setUsePayload({
        ...EMPTY_USE,
        ...scenarioUse,
        consent_id: effectiveCid,
      })
    }
  }

  const onLoadFromHistory = (run: { tab: Tab; payload: IssuancePayload | UsePayload | EudiPayload }) => {
    handleSetTab(run.tab)
    if (run.tab === 'issuance') {
      setIssuancePayload({ ...EMPTY_ISSUANCE, ...(run.payload as IssuancePayload) })
    } else if (run.tab === 'eudi-issuance') {
      setEudiPayload({ ...EMPTY_EUDI, ...(run.payload as EudiPayload) })
    } else {
      setUsePayload({ ...EMPTY_USE, ...(run.payload as UsePayload) })
    }
    window.scrollTo({ top: 0, behavior: 'smooth' })
  }

  const onSubmit = async () => {
    // EUDI-flow start externally (wallet scans QR). Dev-portal has no
    // trace_id to seed — it renders the QR (universal-link client-side
    // assembled), enables watch, and waits for eudi-adapter's first span
    // (useWatchNext handles this).
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
        await appendHistory({
          scenario_name: 'Custom issuance',
          tab: 'issuance',
          payload: issuancePayload,
          trace_id: res.trace_id ?? ctx.traceId,
          outcome: 'allow',
          consent_id: res.consent_id,
        })
        setUsePayload((u) => ({ ...u, consent_id: res.consent_id }))
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
        await appendHistory({
          scenario_name: 'Custom use',
          tab: 'use',
          payload: usePayload,
          trace_id: res.trace_id ?? ctx.traceId,
          outcome,
        })
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
      expected_outcome: result?.outcome === 'error' ? 'allow' : (result?.outcome ?? 'allow'),
      payload:
        tab === 'issuance' ? issuancePayload
        : tab === 'eudi-issuance' ? eudiPayload
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
          pdpTraceIdOverride={fscOverrides.pdpTrace?.traceID}
          watching={watching}
          onToggleWatch={() => setWatching((w) => !w)}
          watchError={watchError}
        />

        {(archMode === 'eudi-issuance' || archMode === 'use') && (
          <FscTxlogPanel data={fscTxlog} transactionId={fscTxID} loading={fscTxlogLoading} />
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
            />
          </div>
          <div className="col">
            {tab === 'eudi-issuance' && submitting && (!archTraceId || playgroundActive) ? (
              <EudiQrPanel
                usecaseKey={eudiPayload.usecase}
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
