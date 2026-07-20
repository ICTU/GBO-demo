import { useEffect, useState } from 'react'
import { fetchPolicySnippet, type OpaExplainDecision, type PolicySnippet } from '../api/devClient'
import { useRules } from '../hooks/useRules'
import RuleSpecPanel from './RuleSpecPanel'
import EvalTrace, { type EvalStep } from './EvalTrace'

// Renders the AuthZEN single Decision context emitted by the policy
// (RFC0052-versie-GBO convention). Two shapes supported:
//
//   ALLOW → context.granted   = [{field?, rule}]
//   DENY  → context.denied_fields = [{field, code, evaluated: [{rule, code}]}]
//                                   + reason_admin.code (top-level summary)
//
// The dev-portal-side UI groups output by FIELD primarily, with the rule
// that decided each field as a chip — matches the per-field aggregation
// from the iWlz playground (within our 1-rule scope).
//
// Legacy shape (axis-cascade with reason_admin.evaluated[{code, status}]) is
// still rendered as a fallback so older trace runs stay inspectable.

type Props = {
  decision: OpaExplainDecision
}

type GrantedEntry = { field?: string; rule: string; steps?: EvalStep[] }
type DeniedEntry = {
  field: string
  code: string
  evaluated?: { rule: string; code: string; steps?: EvalStep[] }[]
}
type LegacyEvaluated = { code: string; status: 'pass' | 'fail' | 'skipped' }

type ContextRead = {
  granted?: GrantedEntry[]
  deniedFields?: DeniedEntry[]
  reasonCode?: string
  legacyEvaluated?: LegacyEvaluated[]
}

function readContext(decision: OpaExplainDecision): ContextRead {
  const r = (decision.result ?? {}) as { context?: Record<string, unknown> }
  const ctx = (r.context ?? {}) as Record<string, unknown>
  const reasonAdmin = ctx.reason_admin as { code?: string; evaluated?: unknown } | undefined
  return {
    granted: Array.isArray(ctx.granted) ? (ctx.granted as GrantedEntry[]) : undefined,
    deniedFields: Array.isArray(ctx.denied_fields) ? (ctx.denied_fields as DeniedEntry[]) : undefined,
    reasonCode: reasonAdmin?.code,
    legacyEvaluated: Array.isArray(reasonAdmin?.evaluated)
      ? (reasonAdmin.evaluated as LegacyEvaluated[])
      : undefined,
  }
}

function lastSegment(field: string): string {
  const parts = field.split('.')
  return parts[parts.length - 1]
}

const ALLOW_COLOR = 'var(--allow-br)'
const DENY_COLOR = 'var(--deny-br)'
const MUTE_COLOR = 'var(--mute)'

export default function OpaDecisionContext({ decision }: Props) {
  const { granted, deniedFields, reasonCode, legacyEvaluated } = readContext(decision)
  const hasGBOShape = granted !== undefined || deniedFields !== undefined || legacyEvaluated !== undefined
  const [snippetOpen, setSnippetOpen] = useState(false)
  const path = decision.path ?? ''
  const decisionTrue = decision.result?.decision === true
  const { rules: allRules } = useRules()

  // Distinct rule_ids mentioned in this decision (granters + deniers).
  // Used to render their spec at the bottom — gives a "why does this rule
  // exist, what does it check?" view alongside the per-field outcome.
  const mentionedRuleIds = new Set<string>()
  granted?.forEach((g) => mentionedRuleIds.add(g.rule))
  deniedFields?.forEach((d) => d.evaluated?.forEach((e) => mentionedRuleIds.add(e.rule)))
  const mentionedRules = allRules.filter((r) => mentionedRuleIds.has(r.rule_id))

  if (!hasGBOShape) {
    return (
      <div style={{ fontSize: 11, color: MUTE_COLOR, marginTop: 8 }}>
        Deze policy retourneert geen <code>context.granted</code>,{' '}
        <code>context.denied_fields</code> of <code>context.reason_admin</code> conform
        RFC0052-versie-GBO. Zie raw decision-log hieronder voor details.
      </div>
    )
  }

  // GBO new shape — ALLOW or DENY with per-field cards
  if (granted !== undefined || deniedFields !== undefined) {
    const totalFields = (granted?.length ?? 0) + (deniedFields?.length ?? 0)
    const grantedCount = granted?.length ?? 0
    return (
      <div style={{ marginTop: 10 }}>
        <DecisionBanner
          decision={decisionTrue}
          grantedCount={grantedCount}
          totalFields={totalFields}
          reasonCode={reasonCode}
        />
        {granted && granted.length > 0 && (
          <FieldList title="Toegestaan" entries={granted.map((g) => ({
            field: g.field ?? '(geen veld)',
            color: ALLOW_COLOR,
            glyph: '✓',
            rules: [{ rule: g.rule, steps: g.steps }],
          }))} />
        )}
        {deniedFields && deniedFields.length > 0 && (
          <FieldList
            title="Geweigerd"
            entries={deniedFields.map((d) => ({
              field: d.field,
              color: DENY_COLOR,
              glyph: '✗',
              rules: d.evaluated && d.evaluated.length > 0
                ? d.evaluated.map((e) => ({ rule: e.rule, code: e.code, steps: e.steps }))
                : [{ rule: '—', code: d.code }],
              fieldCode: d.code,
            }))}
            onCauseClick={reasonCode && path ? () => setSnippetOpen(true) : undefined}
          />
        )}
        {mentionedRules.map((r) => (
          <RuleSpecPanel key={r.rule_id} rule={r} />
        ))}
        {snippetOpen && reasonCode && path && (
          <PolicySnippetModal path={path} code={reasonCode} onClose={() => setSnippetOpen(false)} />
        )}
      </div>
    )
  }

  // Legacy axis-cascade shape (older traces)
  return <LegacyEvaluatedList evaluated={legacyEvaluated ?? []} reasonCode={reasonCode} path={path} />
}

function DecisionBanner({
  decision, grantedCount, totalFields, reasonCode,
}: { decision: boolean; grantedCount: number; totalFields: number; reasonCode?: string }) {
  const color = decision ? ALLOW_COLOR : DENY_COLOR
  const bg = decision ? 'rgba(80, 200, 120, 0.10)' : 'rgba(255, 80, 100, 0.10)'
  return (
    <div style={{
      padding: '8px 12px', borderRadius: 6, marginBottom: 10,
      background: bg, borderLeft: `3px solid ${color}`,
      display: 'flex', alignItems: 'baseline', justifyContent: 'space-between', gap: 12,
    }}>
      <div style={{ fontSize: 12, color: 'var(--text)', fontWeight: 700 }}>
        {decision ? 'ALLOW' : 'DENY'}
      </div>
      <div style={{ fontSize: 11, color: MUTE_COLOR }}>
        {decision
          ? `${grantedCount} van ${totalFields} velden toegestaan`
          : reasonCode
            ? <>
                Reden: <span className="mono" style={{ color: 'var(--text)' }}>{reasonCode}</span>
                {totalFields > 0 && <> · {totalFields - grantedCount} van {totalFields} velden geweigerd</>}
              </>
            : `${totalFields - grantedCount} van ${totalFields} velden geweigerd`}
      </div>
    </div>
  )
}

type FieldEntry = {
  field: string
  color: string
  glyph: string
  rules: { rule: string; code?: string; steps?: EvalStep[] }[]
  fieldCode?: string
}

function FieldList({
  title, entries, onCauseClick,
}: { title: string; entries: FieldEntry[]; onCauseClick?: () => void }) {
  return (
    <div style={{ marginTop: 8 }}>
      <div style={{ fontSize: 11, color: MUTE_COLOR, fontWeight: 700, marginBottom: 6 }}>
        {title}
      </div>
      <div style={{ display: 'flex', flexDirection: 'column', gap: 4 }}>
        {entries.map((e, i) => {
          const hasTrace = e.rules.some((r) => r.steps && r.steps.length > 0)
          return (
            <div
              key={i}
              style={{
                padding: '4px 6px', borderRadius: 4, background: 'transparent',
              }}
            >
              <div
                onClick={e.fieldCode && onCauseClick ? onCauseClick : undefined}
                style={{
                  display: 'grid', gridTemplateColumns: '18px 1fr auto', gap: 8, alignItems: 'baseline',
                  cursor: e.fieldCode && onCauseClick ? 'pointer' : 'default',
                }}
                title={e.fieldCode && onCauseClick ? 'Klik om de Rego-regel te tonen' : undefined}
              >
                <span style={{ color: e.color, fontWeight: 800, textAlign: 'center' }}>{e.glyph}</span>
                <div style={{ minWidth: 0, overflow: 'hidden', textOverflow: 'ellipsis' }}>
                  <span className="mono" style={{ fontSize: 11.5, color: 'var(--text)' }}>
                    {lastSegment(e.field)}
                  </span>
                  <span className="mono" style={{ fontSize: 10, color: MUTE_COLOR, marginLeft: 6 }}>
                    {e.field}
                  </span>
                </div>
                <div style={{ display: 'flex', gap: 4, flexShrink: 0 }}>
                  {e.rules.map((r, j) => (
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
              {hasTrace && e.rules.map((r, j) => (
                r.steps && r.steps.length > 0 && (
                  <div key={`tr-${j}`} style={{ marginLeft: 26 }}>
                    <EvalTrace ruleId={r.rule} steps={r.steps} />
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
}: { evaluated: LegacyEvaluated[]; reasonCode?: string; path: string }) {
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
