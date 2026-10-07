import { useEffect, useState } from 'react'
import { fetchPolicySnippet, type EngineDecision, type PolicySnippet } from '../api/devClient'
import { useRules } from '../hooks/useRules'
import { readDecision, type DecisionView, type FieldOutcome, type Step } from '../lib/decisionView'
import RuleSpecPanel from './RuleSpecPanel'
import EvalTrace from './EvalTrace'

// Renders the policy's decision, as read by readDecision: per field which
// rule granted it or why it was refused, with each rule's trace. A deny
// shows both what the consumer was told and the request's own code: the
// consumer hears only the FTV GraphQL profile's code for it (never which
// field, never why), the administrator sees the rest here.

type Props = {
  decision: EngineDecision
}

function lastSegment(field: string): string {
  const parts = field.split('.')
  return parts[parts.length - 1]
}

const ALLOW_COLOR = 'var(--allow-br)'
const DENY_COLOR = 'var(--deny-br)'
const MUTE_COLOR = 'var(--mute)'

export default function OpaDecisionContext({ decision }: Props) {
  const view = readDecision(decision.result)
  const [snippetCode, setSnippetCode] = useState<string | null>(null)
  const path = decision.path ?? ''
  const { rules: allRules } = useRules()

  // The rules this decision mentions, to show their spec below the fields.
  const mentionedRuleIds = new Set<string>()
  for (const f of [...(view.granted ?? []), ...(view.denied ?? [])]) {
    f.rules.forEach((r) => mentionedRuleIds.add(r.rule))
  }
  const mentionedRules = allRules.filter((r) => mentionedRuleIds.has(r.rule_id))
  const openSnippet = path ? (code: string) => setSnippetCode(code) : undefined

  if (view.shape === 'none') {
    return (
      <div style={{ fontSize: 11, color: MUTE_COLOR, marginTop: 8 }}>
        Deze beslissing bevat geen beslissingscontext (<code>context.graphql</code>,{' '}
        <code>context.granted</code> of <code>context.denied_fields</code>). Zie de ruwe
        decision-log hieronder.
      </div>
    )
  }

  if (view.shape === 'legacy') {
    return <LegacyEvaluatedList evaluated={view.legacyEvaluated ?? []} reasonCode={view.adminCode} path={path} />
  }

  const grantedCount = view.granted?.length ?? 0
  const totalFields = grantedCount + (view.denied?.length ?? 0)
  return (
    <div style={{ marginTop: 10 }}>
      <DecisionBanner view={view} grantedCount={grantedCount} totalFields={totalFields} />
      <DecisionDetail view={view} />
      {view.granted && view.granted.length > 0 && (
        <FieldList title="Toegestaan" color={ALLOW_COLOR} glyph="✓" fields={view.granted} />
      )}
      {view.denied && view.denied.length > 0 && (
        <FieldList title="Geweigerd" color={DENY_COLOR} glyph="✗" fields={view.denied} onCauseClick={openSnippet} />
      )}
      {mentionedRules.map((r) => (
        <RuleSpecPanel key={r.rule_id} rule={r} />
      ))}
      {snippetCode && path && (
        <PolicySnippetModal path={path} code={snippetCode} onClose={() => setSnippetCode(null)} />
      )}
    </div>
  )
}

function DecisionBanner({
  view, grantedCount, totalFields,
}: { view: DecisionView; grantedCount: number; totalFields: number }) {
  const color = view.allowed ? ALLOW_COLOR : DENY_COLOR
  const bg = view.allowed ? 'rgba(80, 200, 120, 0.10)' : 'rgba(255, 80, 100, 0.10)'
  const refused = totalFields - grantedCount
  return (
    <div style={{
      padding: '8px 12px', borderRadius: 6, marginBottom: 10,
      background: bg, borderLeft: `3px solid ${color}`,
      display: 'flex', alignItems: 'baseline', justifyContent: 'space-between', gap: 12,
    }}>
      <div style={{ fontSize: 12, color: 'var(--text)', fontWeight: 700 }}>
        {view.allowed ? 'ALLOW' : 'DENY'}
      </div>
      <div style={{ fontSize: 11, color: MUTE_COLOR, textAlign: 'right' }}>
        {view.allowed
          ? `${grantedCount} van ${totalFields} velden toegestaan`
          : <>
              {view.consumerCode && (
                <span title="De code die de afnemer terugkreeg (FTV GraphQL-profiel): nooit welk veld of waarom">
                  Afnemer kreeg: <span className="mono" style={{ color: 'var(--text)' }}>{view.consumerCode}</span>
                </span>
              )}
              {view.consumerCode && view.adminCode && <> · </>}
              {view.adminCode && (
                <span title="De eigen code van het verzoek, onverhuld">
                  {view.consumerCode ? 'Beheerder' : 'Reden'}: <span className="mono" style={{ color: 'var(--text)' }}>{view.adminCode}</span>
                </span>
              )}
              {totalFields > 0 && <> · <span style={{ whiteSpace: 'nowrap' }}>{refused} van {totalFields} velden geweigerd</span></>}
            </>}
      </div>
    </div>
  )
}

// What the decision was taken on: the mapper's explanation of a request it
// could not verify, and the schema the request was checked against.
function DecisionDetail({ view }: { view: DecisionView }) {
  if (!view.mapperMessage && !view.schemaDigest) return null
  return (
    <div style={{ fontSize: 11, color: MUTE_COLOR, marginBottom: 8, display: 'flex', flexDirection: 'column', gap: 2 }}>
      {view.mapperMessage && (
        <div>
          Toelichting mapper: <span className="mono" style={{ color: 'var(--text)' }}>{view.mapperMessage}</span>
        </div>
      )}
      {view.schemaDigest && (
        <div title={view.schemaDigest}>
          Schema: <span className="mono">{view.schemaDigest.slice(0, 19)}…</span>
        </div>
      )}
    </div>
  )
}

function FieldList({
  title, color, glyph, fields, onCauseClick,
}: {
  title: string
  color: string
  glyph: string
  fields: FieldOutcome[]
  onCauseClick?: (code: string) => void
}) {
  return (
    <div style={{ marginTop: 8 }}>
      <div style={{ fontSize: 11, color: MUTE_COLOR, fontWeight: 700, marginBottom: 6 }}>
        {title}
      </div>
      <div style={{ display: 'flex', flexDirection: 'column', gap: 4 }}>
        {fields.map((f, i) => {
          const clickable = Boolean(f.code && onCauseClick)
          return (
            <div key={i} style={{ padding: '4px 6px', borderRadius: 4, background: 'transparent' }}>
              <div
                onClick={clickable ? () => onCauseClick!(f.code!) : undefined}
                style={{
                  display: 'grid', gridTemplateColumns: '18px 1fr auto', gap: 8, alignItems: 'baseline',
                  cursor: clickable ? 'pointer' : 'default',
                }}
                title={clickable ? `Klik om de Rego-regel voor ${f.code} te tonen` : f.key}
              >
                <span style={{ color, fontWeight: 800, textAlign: 'center' }}>{glyph}</span>
                <div style={{ minWidth: 0, overflow: 'hidden', textOverflow: 'ellipsis' }}>
                  <span className="mono" style={{ fontSize: 11.5, color: 'var(--text)' }}>
                    {lastSegment(f.path)}
                  </span>
                  <span className="mono" style={{ fontSize: 10, color: MUTE_COLOR, marginLeft: 6 }}>
                    {f.path}
                  </span>
                  {f.key && (
                    <span className="mono" style={{ fontSize: 10, color: MUTE_COLOR, marginLeft: 6 }}>
                      ({f.key})
                    </span>
                  )}
                </div>
                <div style={{ display: 'flex', gap: 4, flexShrink: 0, alignItems: 'baseline' }}>
                  {f.code && (
                    <span className="mono" style={{ fontSize: 10, color: DENY_COLOR }}>{f.code}</span>
                  )}
                  {f.rules.map((r, j) => (
                    <span
                      key={j}
                      className="mono"
                      style={{
                        fontSize: 10, padding: '1px 6px', borderRadius: 3,
                        background: 'var(--panel-2)', color: r.code ? DENY_COLOR : 'var(--text)',
                        border: '1px solid var(--border)',
                      }}
                      title={r.code ? `${r.rule}: ${r.code}` : r.rule}
                    >
                      {r.rule}
                    </span>
                  ))}
                </div>
              </div>
              {f.rules.map((r, j) => (
                r.steps && r.steps.length > 0 && (
                  <div key={`tr-${j}`} style={{ marginLeft: 26 }}>
                    <EvalTrace ruleId={r.rule} steps={r.steps as Step[]} />
                  </div>
                )
              ))}
            </div>
          )
        })}
      </div>
    </div>
  )
}

function LegacyEvaluatedList({
  evaluated, reasonCode, path,
}: { evaluated: NonNullable<DecisionView['legacyEvaluated']>; reasonCode?: string; path: string }) {
  const [snippetOpen, setSnippetOpen] = useState(false)
  const glyph = { pass: '✓', fail: '✗', skipped: '○' } as const
  const color = { pass: ALLOW_COLOR, fail: DENY_COLOR, skipped: MUTE_COLOR } as const
  return (
    <div style={{ marginTop: 10 }}>
      <div style={{ fontSize: 11, color: MUTE_COLOR, fontWeight: 700, marginBottom: 6 }}>
        Geëvalueerde axes (legacy)
      </div>
      <div style={{ display: 'flex', flexDirection: 'column', gap: 4 }}>
        {evaluated.map((e) => {
          const isCause = e.code === reasonCode
          return (
            <div
              key={e.code}
              onClick={isCause ? () => setSnippetOpen(true) : undefined}
              style={{
                display: 'grid', gridTemplateColumns: '18px 1fr', gap: 8, alignItems: 'baseline',
                background: isCause ? 'rgba(255, 80, 100, 0.10)' : 'transparent',
                borderLeft: isCause ? `3px solid ${DENY_COLOR}` : '3px solid transparent',
                paddingLeft: 6, paddingTop: 2, paddingBottom: 2, borderRadius: 4,
                cursor: isCause ? 'pointer' : 'default',
              }}
            >
              <span style={{ color: color[e.status], fontWeight: 800, textAlign: 'center' }}>
                {glyph[e.status]}
              </span>
              <div style={{ fontSize: 12 }}>
                <span style={{ color: e.status === 'fail' ? 'var(--text)' : MUTE_COLOR }}>{e.code}</span>
              </div>
            </div>
          )
        })}
      </div>
      {snippetOpen && reasonCode && path && (
        <PolicySnippetModal path={path} code={reasonCode} onClose={() => setSnippetOpen(false)} />
      )}
    </div>
  )
}

function PolicySnippetModal({ path, code, onClose }: { path: string; code: string; onClose: () => void }) {
  const [snippet, setSnippet] = useState<PolicySnippet | null>(null)
  const [error, setError] = useState<string | null>(null)

  useEffect(() => {
    let cancelled = false
    fetchPolicySnippet(path, code)
      .then((s) => { if (!cancelled) setSnippet(s) })
      .catch((e: Error) => { if (!cancelled) setError(e.message) })
    return () => { cancelled = true }
  }, [path, code])

  const lines = snippet?.raw.split('\n') ?? []
  const target = snippet?.line ?? 0
  const start = Math.max(0, target - 5)
  const end = Math.min(lines.length, target + 6)
  const window = lines.slice(start, end)

  return (
    <div
      style={{
        position: 'fixed', inset: 0, background: 'rgba(0,0,0,0.55)',
        display: 'flex', alignItems: 'center', justifyContent: 'center', zIndex: 100,
      }}
      onClick={onClose}
    >
      <div
        style={{
          width: 'min(720px, 92vw)', maxHeight: '80vh', overflow: 'auto',
          background: 'var(--panel-3)', border: '1px solid var(--border-2)',
          borderRadius: 10, padding: 18, boxShadow: '0 24px 60px rgba(0,0,0,0.6)',
        }}
        onClick={(e) => e.stopPropagation()}
      >
        <div style={{ display: 'flex', alignItems: 'baseline', justifyContent: 'space-between', marginBottom: 10 }}>
          <span className="mono" style={{ fontSize: 12, color: 'var(--text)', fontWeight: 700 }}>
            {snippet ? `${snippet.id}:${snippet.line}` : `Rego voor "${code}"…`}
          </span>
          <button onClick={onClose} style={{ background: 'transparent', border: 'none', color: MUTE_COLOR, cursor: 'pointer', fontSize: 14 }}>✕</button>
        </div>
        {error && <div style={{ color: DENY_COLOR, fontSize: 12 }}>Fout: {error}</div>}
        {!snippet && !error && <div style={{ fontSize: 12, color: MUTE_COLOR }}>Source ophalen…</div>}
        {snippet && (
          <pre className="codeblock" style={{ marginTop: 4, maxHeight: '60vh', overflowY: 'auto' }}>
            {window.map((ln, i) => {
              const lineNo = start + i + 1
              const isTarget = lineNo === target
              return (
                <div key={lineNo} style={{
                  display: 'grid', gridTemplateColumns: '36px 1fr', gap: 8,
                  background: isTarget ? 'rgba(255,200,0,0.10)' : 'transparent',
                  borderLeft: isTarget ? '3px solid var(--warn-br)' : '3px solid transparent',
                  paddingLeft: 8,
                }}>
                  <span style={{ color: MUTE_COLOR, textAlign: 'right' }}>{lineNo}</span>
                  <span style={{ color: isTarget ? 'var(--text)' : MUTE_COLOR, whiteSpace: 'pre' }}>{ln}</span>
                </div>
              )
            })}
          </pre>
        )}
      </div>
    </div>
  )
}
