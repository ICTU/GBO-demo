import type { RuleMeta } from '../api/devClient'

// Renders a rule's declared bereik + evaluation criteria in human-readable
// form. Mirrors the iWlz playground's "Toon rule-spec & bron" panel but
// adapted to our consent-driven spec-shape (vs iWlz' role-driven).
//
// Spec-driven rendering: every truthy field in spec produces a row. Unknown
// future fields fall through to a generic key:value row instead of being
// silently ignored — keeps the panel discoverable as the rule-model grows.

type Props = {
  rule: RuleMeta
}

const KNOWN: Record<string, string> = {
  consent_required: 'Toestemming vereist',
  consent_must_cover_scope: 'Toestemming dekt scope',
  consent_must_cover_fields: 'Toestemming dekt velden',
  pip: 'PIP-lookup',
}

function fmtBool(v: unknown): string {
  if (v === true) return 'ja'
  if (v === false) return 'nee'
  if (v === null || v === undefined) return 'n.v.t.'
  return String(v)
}

export default function RuleSpecPanel({ rule }: Props) {
  const spec = rule.spec
  const constraints = spec.constraint_binding ?? []
  const knownKeys = Object.keys(KNOWN)
  const extraKeys = Object.keys(spec).filter(
    (k) => k !== 'rule_id' && k !== 'constraint_binding' && !knownKeys.includes(k),
  )
  return (
    <div style={{
      marginTop: 10, padding: '10px 12px', borderRadius: 6,
      background: 'var(--panel-2)', border: '1px solid var(--border-2)',
    }}>
      <div style={{ fontSize: 11, color: 'var(--mute)', fontWeight: 700, marginBottom: 8 }}>
        Regel-spec · {rule.rule_id}
      </div>
      <Grid>
        {knownKeys.map((k) => (
          <Row key={k} label={KNOWN[k]} value={fmtBool((spec as Record<string, unknown>)[k])} />
        ))}
        {constraints.length > 0 && (
          <Row
            label="Constraint-binding"
            value={constraints.map((c, i) => (
              <div key={i} className="mono" style={{ fontSize: 11 }}>
                {c.arg} <span style={{ color: 'var(--mute)' }}>==</span> resource.{c.resource_field}
              </div>
            ))}
          />
        )}
        {extraKeys.map((k) => (
          <Row key={k} label={k} value={fmtBool((spec as Record<string, unknown>)[k])} />
        ))}
        {(rule.covers_types?.length ?? 0) > 0 && (
          <Row
            label="Dekt types"
            value={
              <div style={{ display: 'flex', gap: 4, flexWrap: 'wrap' }}>
                {rule.covers_types!.map((t) => <Chip key={t}>{t}</Chip>)}
              </div>
            }
          />
        )}
        {(rule.covers_fields?.length ?? 0) > 0 && (
          <Row
            label="Dekt velden (expliciet)"
            value={
              <div style={{ display: 'flex', gap: 4, flexWrap: 'wrap' }}>
                {rule.covers_fields!.map((f) => <Chip key={f}>{f}</Chip>)}
              </div>
            }
          />
        )}
      </Grid>
    </div>
  )
}

function Grid({ children }: { children: React.ReactNode }) {
  return (
    <div style={{
      display: 'grid', gridTemplateColumns: '160px 1fr', gap: '4px 12px',
      alignItems: 'baseline', fontSize: 11.5,
    }}>
      {children}
    </div>
  )
}

function Row({ label, value }: { label: string; value: React.ReactNode }) {
  return (
    <>
      <div style={{ color: 'var(--mute)' }}>{label}</div>
      <div style={{ color: 'var(--text)' }}>{value}</div>
    </>
  )
}

function Chip({ children }: { children: React.ReactNode }) {
  return (
    <span
      className="mono"
      style={{
        fontSize: 10, padding: '1px 6px', borderRadius: 3,
        background: 'var(--panel-3)', color: 'var(--text)',
        border: '1px solid var(--border)',
      }}
    >
      {children}
    </span>
  )
}
