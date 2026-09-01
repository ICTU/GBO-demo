import { useEffect, useLayoutEffect, useRef, useState } from 'react'
import { Link, useSearchParams } from 'react-router'
import { demoSessionHeader } from '../lib/demoSession'
import {
  CompletedDossierItems,
  DossierUser,
  ExpandableChecklistItem,
  HeaderBar,
  HeaderStage,
} from '../components/dossier'

type Bedrag = { waarde: number; valuta?: string }

type AangifteRow = {
  belastingjaar: number
  verzamelinkomen: Bedrag | null
  box1Inkomen: Bedrag | null
  status: string | null
  indieningsdatum: string | null
}

type QueryResponse = {
  allowed: boolean
  data?: { data?: { ingeschrevenPersoon?: { heeftBelastingjaarAangifte?: AangifteRow[] } } }
  reason?: string
  trace_id?: string
  denied_years?: number[]
}

const STEPS = [
  'Beveiligde verbinding met de Belastingdienst opzetten…',
  'Uw inkomensgegevens ophalen…',
  'Gegevens ontvangen en controleren…',
]

const QUERY_TIMEOUT_MS = 35_000

const fmtEuro = (n: number | null | undefined) =>
  n == null ? '—' : '€ ' + n.toLocaleString('nl-NL', { minimumFractionDigits: 0, maximumFractionDigits: 0 })

const CheckIcon = () => (
  <svg width="11" height="11" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="3.5">
    <path d="M5 12l5 5L20 7" />
  </svg>
)

function Loading({ phase }: { phase: number }) {
  return (
    <div className="hb-loading">
      <div className="hb-spinner-wrap"><div className="hb-spinner" /></div>
      <h1 className="hb-loading-title">We halen uw gegevens op</h1>
      <div className="hb-progress">
        {STEPS.map((s, i) => {
          const state = phase > i ? 'done' : phase === i ? 'active' : 'queued'
          return (
            <div key={i} className={`hb-progress-step ${state}`}>
              <span className="hb-progress-circle">{state === 'done' && <CheckIcon />}</span>
              <span>{s}</span>
            </div>
          )
        })}
      </div>
    </div>
  )
}

function Result({
  rows,
  deniedYears,
  traceId,
  onRefresh,
  refreshing,
}: {
  rows: AangifteRow[]
  deniedYears: number[]
  traceId?: string
  onRefresh: () => void
  refreshing: boolean
}) {
  const [open, setOpen] = useState(false)
  const sorted = [...rows].sort((a, b) => b.belastingjaar - a.belastingjaar)
  const deniedSorted = [...deniedYears].sort((a, b) => b - a)
  const partial = deniedSorted.length > 0
  const jaren = (years: number[]) => years.join(' en ')

  return (
    <>
      <div className="hb-eyebrow">Mijn dossier · Hypotheekaanvraag</div>
      <h1 className="hb-title">
        {partial ? 'Uw dossier is bijgewerkt' : 'Uw dossier is compleet'}
      </h1>
      <p className="hb-subtitle">
        {partial
          ? `We hebben uw inkomensgegevens over ${jaren(sorted.map((y) => y.belastingjaar))} opgehaald bij de Belastingdienst. Voor ${jaren(deniedSorted)} gaf u geen toestemming.`
          : 'Uw inkomensgegevens zijn opgehaald bij de Belastingdienst. U kunt uw aanvraag nu afronden.'}
      </p>

      <ul className="hb-checklist" role="list">
        <CompletedDossierItems />
        <ExpandableChecklistItem
          state={partial ? 'todo' : 'done'}
          title="Inkomensgegevens"
          meta={
            partial
              ? `Inkomstenbelasting ${jaren(sorted.map((y) => y.belastingjaar))} opgehaald · ${jaren(deniedSorted)} niet gedeeld`
              : `Inkomstenbelasting ${jaren(sorted.map((y) => y.belastingjaar))} · opgehaald via MijnOverheid`
          }
          status={partial ? 'Deels compleet' : 'Compleet'}
          open={open}
          onToggle={() => setOpen((v) => !v)}
        >
          <div className="hb-data-tables">
            {sorted.map((y) => (
              <div key={y.belastingjaar} className="hb-data-table">
                <div className="hb-data-thead">
                  <span className="title">Aangifte inkomstenbelasting {y.belastingjaar}</span>
                </div>
                <table>
                  <tbody>
                    <tr><td>Belastingjaar</td><td>{y.belastingjaar}</td></tr>
                    <tr><td>Verzamelinkomen</td><td>{fmtEuro(y.verzamelinkomen?.waarde)}</td></tr>
                    <tr><td>Inkomen uit werk en woning</td><td>{fmtEuro(y.box1Inkomen?.waarde)}</td></tr>
                    <tr><td>Status</td><td>{y.status ?? '—'}</td></tr>
                    <tr><td>Indieningsdatum</td><td>{y.indieningsdatum ?? '—'}</td></tr>
                  </tbody>
                </table>
              </div>
            ))}
            {deniedSorted.map((jaar) => (
              <div key={jaar} className="hb-data-table denied">
                <div className="hb-data-thead">
                  <span className="title">Aangifte inkomstenbelasting {jaar}</span>
                </div>
                <div className="denied-note">
                  U heeft voor {jaar} geen toestemming verleend — deze gegevens zijn niet opgehaald.
                </div>
              </div>
            ))}
          </div>

          <div className="hb-panel-actions">
            <button className="hb-link" onClick={onRefresh} disabled={refreshing}>
              {refreshing ? 'Bezig met opnieuw ophalen…' : '↻ Verversen'}
            </button>
            {traceId && <span className="hb-trace">trace-id: {traceId}</span>}
          </div>
        </ExpandableChecklistItem>
      </ul>

      <section className="hb-cta">
        <h2>{partial ? 'Aanvraag afronden' : 'Alles compleet'}</h2>
        <p>
          {partial
            ? 'U kunt uw aanvraag indienen met de gegevens die u heeft gedeeld. Voor de ontbrekende jaren vragen wij u om zelf inkomensbewijzen aan te leveren.'
            : 'We hebben alle onderdelen van uw dossier binnen. U kunt uw hypotheekaanvraag nu indienen — er hoeven geen papieren meer opgestuurd te worden.'}
        </p>
        <div className="hb-actions-row">
          <Link to="/" className="hb-link">Demo opnieuw starten</Link>
        </div>
      </section>
    </>
  )
}

function ErrorPanel({
  reason,
  traceId,
  onRefresh,
  refreshing,
}: {
  reason: string
  traceId?: string
  onRefresh?: () => void
  refreshing?: boolean
}) {
  return (
    <div className="hb-card">
      <h1>Ophalen mislukt</h1>
      <p>
        We konden uw gegevens nu niet ophalen. Dit kan komen doordat de toestemming is ingetrokken
        of verlopen.
      </p>
      <div className="hb-actions-row">
        {onRefresh && (
          <button className="hb-link" onClick={onRefresh} disabled={refreshing}>
            {refreshing ? 'Bezig…' : '↻ Opnieuw proberen'}
          </button>
        )}
        <Link to="/" className="hb-link">← Opnieuw beginnen</Link>
      </div>
      {traceId && (
        <div className="hb-debug-trace">
          <span>Technische details:</span> trace-id <code>{traceId}</code>
          {reason && <> · reden <code>{reason}</code></>}
        </div>
      )}
    </div>
  )
}

function Denied() {
  return (
    <div className="hb-card">
      <h1>U gaf geen toestemming</h1>
      <div className="hb-status-denied">
        Zonder toestemming kunnen we uw inkomen niet automatisch ophalen.
      </div>
      <p>
        U kunt uw aanvraag wel afronden door zelf inkomensbewijzen aan te leveren (loonstroken,
        aangifte). Of probeer het opnieuw:
      </p>
      <p><Link to="/">← Terug naar de aanvraag</Link></p>
    </div>
  )
}

export default function Return() {
  const [params] = useSearchParams()
  const status = params.get('status')
  const consentId = params.get('consent_id')
  const [consentToken] = useState(() =>
    new URLSearchParams(window.location.hash.slice(1)).get('consent_token'))
  useLayoutEffect(() => {
    if (consentToken) {
      window.history.replaceState(null, '', window.location.pathname + window.location.search)
    }
  }, [consentToken])
  const denied = status === 'denied'

  const [phase, setPhase] = useState(0)
  const [response, setResponse] = useState<QueryResponse | null>(null)
  const [fetchError, setFetchError] = useState<string | null>(null)
  const [refreshing, setRefreshing] = useState(false)
  const startedRef = useRef(false)

  // runQuery performs the fetch; when `quick=true` we skip the UX loading steps
  // (used for the Refresh button after a previous successful response).
  const runQuery = (quick: boolean) => {
    if (!consentToken) return
    setFetchError(null)
    if (!quick) {
      setPhase(0)
      setResponse(null)
      const t1 = setTimeout(() => setPhase(1), 700)
      const t2 = setTimeout(() => setPhase(2), 1500)
      // cleanup happens implicitly on new runs; fine for the loading flow
      void t1; void t2
    } else {
      setRefreshing(true)
    }

    const minDelay = quick ? 400 : 2300
    const start = Date.now()
    const controller = new AbortController()
    const timeout = window.setTimeout(() => controller.abort(), QUERY_TIMEOUT_MS)

    fetch('/api/dvtp/query', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json', ...demoSessionHeader() },
      body: JSON.stringify({ consent_token: consentToken }),
      signal: controller.signal,
    })
      .then((r) => r.json())
      .then((data: QueryResponse) => {
        const wait = Math.max(0, minDelay - (Date.now() - start))
        setTimeout(() => {
          setPhase(3)
          setResponse(data)
          setRefreshing(false)
        }, wait)
      })
      .catch((err: unknown) => {
        const message =
          err instanceof DOMException && err.name === 'AbortError'
            ? 'het ophalen duurde te lang'
            : err instanceof Error
              ? err.message
              : 'onbekende netwerkfout'
        setFetchError(message)
        setRefreshing(false)
      })
      .finally(() => window.clearTimeout(timeout))
  }

  useEffect(() => {
    if (denied || !consentId || !consentToken || startedRef.current) return
    startedRef.current = true
    runQuery(false)
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [denied, consentId, consentToken])

  const rows = response?.allowed
    ? response.data?.data?.ingeschrevenPersoon?.heeftBelastingjaarAangifte
    : undefined
  const success = !denied && !!consentId && !!consentToken && !fetchError && !!rows

  return (
    <div className="hb-shell">
      <HeaderBar
        right={
          success ? (
            <DossierUser />
          ) : denied ? undefined : (
            <HeaderStage label={`Mijn aanvraag · Stap ${response ? 4 : 3} van 4`} />
          )
        }
      />
      <main className={success ? 'hb-main' : 'hb-return-main'}>
        {denied ? (
          <Denied />
        ) : !consentId || !consentToken ? (
          <ErrorPanel reason="geen geldige consent-context ontvangen" />
        ) : fetchError ? (
          <ErrorPanel
            reason={`netwerkfout: ${fetchError}`}
            onRefresh={() => runQuery(true)}
            refreshing={refreshing}
          />
        ) : !response ? (
          <Loading phase={phase} />
        ) : rows ? (
          <Result
            rows={rows}
            deniedYears={response.denied_years ?? []}
            traceId={response.trace_id}
            onRefresh={() => runQuery(true)}
            refreshing={refreshing}
          />
        ) : (
          <ErrorPanel
            reason={response.reason ?? 'onbekende fout'}
            traceId={response.trace_id}
            onRefresh={() => runQuery(true)}
            refreshing={refreshing}
          />
        )}
      </main>
    </div>
  )
}
