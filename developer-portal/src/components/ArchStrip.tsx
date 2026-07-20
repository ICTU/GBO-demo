import { Fragment, useState } from 'react'
import ArchNode from './ArchNode'
import NodePopover from './NodePopover'
import {
  EUDI_ISSUANCE_BRANCHES, EUDI_ISSUANCE_CHAIN,
  ISSUANCE_CHAIN, USE_BRANCHES, USE_CHAIN, type NodeDef,
} from '../data/chains'
import type { ArchStates } from '../hooks/useArchState'
import { useExplain } from '../hooks/useExplain'
import type { ApiCall, Tab } from '../types'

type Props = {
  mode: Tab
  setMode: (m: Tab) => void
  states: ArchStates
  apiCalls?: ApiCall[]
  traceId?: string
  // For EUDI the traceparent context breaks at FSC-Inway (the AuthZen
  // plugin uses context.Background()), so pdp-service creates a fresh
  // trace-id. useFscTxlog looks it up via the Fsc-Transaction-Id tag; we
  // pass it in here so useExplain can find the OPA decision-log against
  // the right trace-id and the pdp-popover can read its gbo.* attrs from
  // the PDP span.
  pdpTraceIdOverride?: string
  watching?: boolean
  onToggleWatch?: () => void
  watchError?: string | null
}

// Match api_calls on node-id for inline-popover-data.
// (Only issuance carries real api_calls; use-flow only exposes deep-links.)
function apiCallForNode(nodeId: string, calls: ApiCall[] | undefined): ApiCall | undefined {
  if (!calls) return undefined
  if (nodeId === 's02') return undefined // S02 is the "receiver" itself; no sub-call
  if (nodeId === 'bsnk') return calls.find((c) => c.url.includes('/pseudonymize'))
  if (nodeId === 's01') return calls.find((c) => c.url.includes('/consents'))
  return undefined
}

export default function ArchStrip({
  mode, setMode, states, apiCalls, traceId, pdpTraceIdOverride,
  watching, onToggleWatch, watchError,
}: Props) {
  // In EUDI mode we use the PDP trace (cross-lookup) for OPA/PDP details,
  // because the adapter trace doesn't contain them. Other nodes still
  // resolve against the adapter trace.
  // The cross-trace-lookup applies to both flows that hit pdp-service via
  // FSC-Inway (EUDI + DvTP). The AuthZen plugin's context.Background()
  // breaks the OTel trace; pdp-service creates a fresh trace-id which we
  // locate via the Fsc-Transaction-Id.
  const useCrossTrace = (mode === 'eudi-issuance' || mode === 'use') && !!pdpTraceIdOverride
  const explainTraceId = useCrossTrace ? pdpTraceIdOverride : traceId
  const pdpSpanTraceId = useCrossTrace ? pdpTraceIdOverride : traceId
  const [openNode, setOpenNode] = useState<string | null>(null)
  const chain: NodeDef[] =
    mode === 'issuance' ? ISSUANCE_CHAIN
    : mode === 'eudi-issuance' ? EUDI_ISSUANCE_CHAIN
    : USE_CHAIN
  const branches: NodeDef[] =
    mode === 'use' ? USE_BRANCHES
    : mode === 'eudi-issuance' ? EUDI_ISSUANCE_BRANCHES
    : []

  // Fetch decision-log entries whenever a use- or EUDI-trace is shown —
  // used both for the popover content AND for colouring the OPA node by
  // policy outcome (DENY → red) rather than HTTP status (which is always
  // 200 when OPA was reached, even on policy DENY). OPA has no OTel
  // instrumentation, so the decision-log is the only source for its state.
  const explain = useExplain(
    explainTraceId,
    !!explainTraceId && (mode === 'use' || mode === 'eudi-issuance'),
  )

  // Two override-flavours with different timing:
  //   - "Soothing" (downgrades alarm: red→green): apply immediately during
  //     the reveal so misleading red flashes never show on relay-nodes.
  //   - "Highlight" (upgrades alarm: green→red on a policy-DENY at OPA):
  //     apply only after the reveal-animation completes, so the DENY-
  //     punchline lands when the rest of the chain is already in place.
  //
  //   1. OPA-node colour from policy decision (use-tab only):
  //        ALLOW (context.granted)      → green
  //        DENY  (context.reason_admin) → red
  //      OPA always returns HTTP 200 even on policy-DENY, so the span-derived
  //      colour can't distinguish ALLOW from DENY without the decision-log.
  //
  //   2. Relay soothing: when ANY downstream node is red, the relay services
  //      (afnemer, fsc-outway, fsc-manager, fsc-inway) that only propagated
  //      the 4xx upstream are forced back to green. Applies to both policy-
  //      DENY (PEP/OPA red) and system errors (e.g. consent-pip 404 bubbling
  //      out as PEP 400). They did their job correctly — only the actual
  //      source-of-error should stay red.
  const decisionCtx = (() => {
    if ((mode !== 'use' && mode !== 'eudi-issuance') || explain.decisions.length === 0) return undefined
    const d = explain.decisions[0]
    return ((d.result ?? {}) as { context?: Record<string, unknown> }).context as
      | { granted?: unknown; reason_admin?: unknown }
      | undefined
  })()

  const animationDone =
    Object.keys(states).length > 0 &&
    Object.values(states).every((s) => s !== 'yellow')

  const effectiveStates = (() => {
    const out = { ...states }

    if (decisionCtx && animationDone) {
      // Color BOTH the PDP node (the logical decision-unit) and the OPA
      // branch (the engine). HTTP-status alone can't distinguish ALLOW
      // from DENY because OPA returns 200 for both — only the decision-
      // log tells them apart. Two shapes are accepted: denied_fields
      // (per-field) and reason_admin (single reason).
      const ctx = decisionCtx as { granted?: unknown; reason_admin?: unknown; denied_fields?: unknown }
      const isAllow = Array.isArray(ctx.granted) && (ctx.granted as unknown[]).length > 0
      const isDeny = !isAllow && (ctx.reason_admin || (Array.isArray(ctx.denied_fields) && (ctx.denied_fields as unknown[]).length > 0))
      if (isAllow) {
        out.pdp = 'green'
        out.opa = 'green'
      } else if (isDeny) {
        out.pdp = 'red'
        out.opa = 'red'
      }
    }

    // PDP is the logical "umbrella unit": if one of its sub-components
    // (the OPA decision-engine or the consent-pip PIP-lookup) fails, the
    // PDP-call fails as a whole. Force red as soon as a branch is red —
    // even when decisionCtx is missing (PIP error before OPA is reached).
    if (out['opa'] === 'red' || out['consent-pip'] === 'red') {
      out.pdp = 'red'
    }

    // Relay soothing: independent of decisionCtx, so it also fires on
    // pre-OPA system errors (BSNk timeout, sidecar unreachable, …).
    const downstreamRed = ['pdp', 'opa', 'consent-pip', 'sidecar', 'bsnk', 'bron']
      .some((id) => out[id] === 'red')
    if (downstreamRed) {
      for (const id of ['afnemer', 'hv-outway', 'hv-manager', 'bd-inway', 'edi-outway', 'edi-manager', 'eudi-adapter']) {
        if (out[id] === 'red') out[id] = 'green'
      }
    }

    return out
  })()

  const handleNodeClick = (id: string) => setOpenNode((cur) => (cur === id ? null : id))

  return (
    <div className="panel arch" onClick={() => setOpenNode(null)}>
      <div className="panel-h">
        <span className="t">
          <span className="n">3.5</span>
          Architectuur · {
            mode === 'issuance' ? 'issuance-keten'
            : mode === 'eudi-issuance' ? 'EUDI-issuance-keten'
            : 'use-keten'
          }
        </span>
        <div className="arch-modes">
          {onToggleWatch && (
            <button
              className={`watch ${watching ? 'on' : ''}`}
              title="Pikt automatisch de eerstvolgende run op die ergens anders (burger-FE, afnemer-mock, curl) wordt gestart."
              onClick={(e) => { e.stopPropagation(); onToggleWatch() }}
            >
              {watching ? '◉ watch…' : '○ watch'}
            </button>
          )}
          {watchError && <span className="watch-err" title={watchError}>!</span>}
          <button
            className={mode === 'issuance' ? 'on' : ''}
            onClick={(e) => { e.stopPropagation(); setMode('issuance') }}
          >
            Issuance
          </button>
          <button
            className={mode === 'use' ? 'on' : ''}
            onClick={(e) => { e.stopPropagation(); setMode('use') }}
          >
            Use
          </button>
          <button
            className={mode === 'eudi-issuance' ? 'on' : ''}
            onClick={(e) => { e.stopPropagation(); setMode('eudi-issuance') }}
          >
            EUDI
          </button>
        </div>
      </div>
      <div className="panel-b" style={{ padding: 0, overflow: 'visible' }}>
        <div className="chain">
          {chain.map((node, idx) => {
            const state = effectiveStates[node.id] ?? 'grey'
            const nodeBranches = branches.filter((b) => b.branchOf === node.id)
            const isOpen = openNode === node.id
            return (
              <Fragment key={node.id}>
                {idx > 0 && (
                  <div className={`arrow ${state === 'grey' ? '' : state}`.trim()}>
                    →
                  </div>
                )}
                <div className="node" key={node.id} style={{ position: 'relative' }}>
                  <ArchNode
                    node={node}
                    state={state}
                    selected={isOpen}
                    onClick={() => handleNodeClick(node.id)}
                  />
                  {nodeBranches.length > 0 && (
                    <div style={{ display: 'flex', flexDirection: 'row', gap: 12, marginTop: 10, alignItems: 'flex-start', justifyContent: 'center' }}>
                      {nodeBranches.map((branchDef) => {
                        const branchState = effectiveStates[branchDef.id] ?? 'grey'
                        const isBranchOpen = openNode === branchDef.id
                        return (
                          <div key={branchDef.id} className="branch" style={{ position: 'relative' }}>
                            <div className={`branch-conn ${branchState === 'grey' ? '' : branchState}`.trim()} style={{ fontSize: 22 }}>
                              ↓
                            </div>
                            <ArchNode
                              node={branchDef}
                              state={branchState}
                              selected={isBranchOpen}
                              branch
                              onClick={() => handleNodeClick(branchDef.id)}
                            />
                            {isBranchOpen && (
                              <NodePopover
                                node={branchDef}
                                state={branchState}
                                apiCall={undefined}
                                traceId={branchDef.id === 'opa' ? pdpSpanTraceId : traceId}
                                explainDecisions={branchDef.id === 'opa' && (mode === 'use' || mode === 'eudi-issuance') ? explain.decisions : undefined}
                                explainLoading={branchDef.id === 'opa' ? explain.loading : undefined}
                                explainError={branchDef.id === 'opa' ? explain.error : undefined}
                              />
                            )}
                          </div>
                        )
                      })}
                    </div>
                  )}
                  {isOpen && (
                    <NodePopover
                      node={node}
                      state={state}
                      apiCall={apiCallForNode(node.id, apiCalls)}
                      traceId={node.id === 'pdp' ? pdpSpanTraceId : traceId}
                      explainDecisions={node.id === 'pdp' ? explain.decisions : undefined}
                      explainLoading={node.id === 'pdp' ? explain.loading : undefined}
                      explainError={node.id === 'pdp' ? explain.error : undefined}
                    />
                  )}
                </div>
              </Fragment>
            )
          })}
        </div>
      </div>
    </div>
  )
}
