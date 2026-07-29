// One playground for every bron. Replaces the page graphql-server used to
// embed and serve itself: the tools are npm-dependencies here, so the CDN
// pins and SRI hashes that page carried are gone, and a second bron needs an
// entry in data/bronnen.ts plus a proxy-route — not a second copy.
//
// Two tools behind one tab bar, because neither covers both halves of what a
// reader wants from a schema:
//   Query   GraphiQL 5 + the explorer plugin. The explorer's field checkboxes
//           are the "click a query together" half; the doc explorer and the
//           editor's schema-aware completion cover the reading half.
//   Schema  GraphQL Voyager, which renders the schema as a type graph — the
//           one view GraphiQL does not have.
//
// The page reaches the bron directly: no FSC-Inway, no sidecar, no PEP, no
// PDP. That is the point (it is how you explore a bronprofiel) but it means
// no consent check and no BSN-pseudonymisation, hence the banner.
import { useEffect, useMemo, useRef, useState } from 'react'
import { GraphiQL, HISTORY_PLUGIN } from 'graphiql'
import { createGraphiQLFetcher } from '@graphiql/toolkit'
import { explorerPlugin } from '@graphiql/plugin-explorer'
import { BRON_PROFILES, bronProfileById, type BronProfile } from '../data/bronnen'
import './monacoWorkers'
import 'graphiql/style.css'
import '@graphiql/plugin-explorer/style.css'
import 'graphql-voyager/dist/voyager.css'
import './playground.css'

type Tab = 'query' | 'schema'

function bronFromUrl(): BronProfile {
  const wanted = new URLSearchParams(window.location.search).get('bron')
  return bronProfileById(wanted ?? undefined) ?? BRON_PROFILES[0]
}

// Voyager renders through its standalone bundle, imported on demand and given
// a plain DOM node — not as a component in our tree. Its published component
// build reaches into React internals that React 19 removed
// (`ReactCurrentDispatcher`), which blanks the whole page; the standalone
// bundle carries its own React and is unaffected. It is also 1.6 MB, so
// loading it only when the Schema tab is first opened is worth doing anyway.
async function renderSchemaGraph(host: HTMLElement, graphqlPath: string) {
  const voyager = await import('graphql-voyager/dist/voyager.standalone.js')
  // Voyager's own introspection query is the conservative one — no
  // specifiedByURL, no isRepeatable, no inputValueDeprecation — which is
  // exactly the subset graphql-go v0.8.1 answers. The full query is rejected
  // by the bron outright.
  const response = await fetch(graphqlPath, {
    method: 'POST',
    headers: { Accept: 'application/json', 'Content-Type': 'application/json' },
    body: JSON.stringify({ query: voyager.voyagerIntrospectionQuery }),
  })
  voyager.renderVoyager(host, {
    introspection: await response.json(),
    displayOptions: { sortByAlphabet: true },
  })
}

export default function BronPlayground() {
  const [bron, setBron] = useState<BronProfile>(bronFromUrl)
  const [tab, setTab] = useState<Tab>('query')
  // The Schema tab builds itself the first time it is opened, then per bron.
  const [schemaOpened, setSchemaOpened] = useState(false)
  const [schemaError, setSchemaError] = useState<string | null>(null)
  const [switched, setSwitched] = useState(false)
  const schemaHost = useRef<HTMLDivElement>(null)

  const fetcher = useMemo(() => createGraphiQLFetcher({ url: bron.graphqlPath }), [bron])
  const plugins = useMemo(() => [HISTORY_PLUGIN, explorerPlugin()], [])

  useEffect(() => {
    const host = schemaHost.current
    if (!schemaOpened || !host) return
    let cancelled = false
    setSchemaError(null)
    host.replaceChildren()
    renderSchemaGraph(host, bron.graphqlPath).catch((err: unknown) => {
      if (cancelled) return
      setSchemaError(err instanceof Error ? err.message : String(err))
    })
    return () => { cancelled = true }
  }, [schemaOpened, bron])

  const selectBron = (id: string) => {
    const next = bronProfileById(id)
    if (!next) return
    setBron(next)
    setSwitched(true)
    // Keep the address bar shareable: a reader who lands on this tab and
    // switches bron should be able to hand the URL to someone else.
    const url = new URL(window.location.href)
    url.searchParams.set('bron', next.id)
    window.history.replaceState(null, '', url)
  }

  const showTab = (next: Tab) => {
    if (next === 'schema') setSchemaOpened(true)
    setTab(next)
  }

  return (
    <div className="pg">
      <header className="pg-bar">
        <a className="pg-brand" href="/">
          <span className="pg-logo">G</span>
          GBO · bron-playground
        </a>

        <label className="pg-pick">
          Bron
          <select
            className="sel mono"
            value={bron.id}
            onChange={(e) => selectBron(e.target.value)}
          >
            {BRON_PROFILES.map((b) => (
              <option key={b.id} value={b.id}>{`${b.label} — ${b.bronSvc}`}</option>
            ))}
          </select>
        </label>

        <nav className="pg-tabs" role="tablist">
          <button
            className={`pg-tab${tab === 'query' ? ' on' : ''}`}
            role="tab"
            aria-selected={tab === 'query'}
            onClick={() => showTab('query')}
          >
            Query
          </button>
          <button
            className={`pg-tab${tab === 'schema' ? ' on' : ''}`}
            role="tab"
            aria-selected={tab === 'schema'}
            onClick={() => showTab('schema')}
          >
            Schema
          </button>
        </nav>

        <p
          className="pg-warn"
          title="Deze pagina praat rechtstreeks met de bron: geen FSC-Inway, sidecar, PEP of PDP, dus geen toestemmingscheck en geen BSN-pseudonimisering. De data is mock-data."
        >
          <b>Directe toegang tot de bron.</b>
          <span className="pg-warn-detail">
            {' '}Geen FSC-Inway, sidecar, PEP of PDP: geen toestemmingscheck, geen
            BSN-pseudonimisering. Mock-data.
          </span>
        </p>
      </header>

      <main className="pg-panes">
        {/* Remounted per bron (key): a fresh editor and no stale schema in the
            doc explorer. `defaultQuery` only fills an *empty* editor, so on
            arrival a returning reader keeps their own last query; after a bron
            switch that query belongs to the other schema, so the new bron's
            example is forced in with `initialQuery`. */}
        <section className="pg-pane" hidden={tab !== 'query'}>
          <GraphiQL
            key={bron.id}
            fetcher={fetcher}
            plugins={plugins}
            {...(switched
              ? { initialQuery: bron.exampleQuery }
              : { defaultQuery: bron.exampleQuery })}
            defaultEditorToolsVisibility
          />
        </section>
        {/* The host div stays mounted even while an error is shown: the effect
            needs it to still be there to retry when the bron changes. */}
        <section className="pg-pane" hidden={tab !== 'schema'}>
          {schemaError && (
            <pre className="pg-failed">
              Schema-visualisatie kon niet geladen worden:{'\n'}{schemaError}
            </pre>
          )}
          <div className="pg-voyager" ref={schemaHost} hidden={!!schemaError} />
        </section>
      </main>
    </div>
  )
}
