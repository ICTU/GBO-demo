import type { RuleMeta } from '../api/devClient'

// Shows a rule as it is written: its Rego source. Each rule states its
// checks as `deny contains "<CODE>" if <condition>`, so the source is the
// explanation of what it checks; the panel adds only the fields it covers.

type Props = {
  rule: RuleMeta
}

export default function RuleSpecPanel({ rule }: Props) {
  return (
    <div style={{
      marginTop: 10, padding: '10px 12px', borderRadius: 6,
      background: 'var(--panel-2)', border: '1px solid var(--border-2)',
    }}>
      <div style={{ fontSize: 11, color: 'var(--mute)', fontWeight: 700, marginBottom: 8 }}>
        Regel · {rule.rule_id}
        {rule.regime && <span style={{ fontWeight: 400 }}> · regime {rule.regime}</span>}
        {rule.source_id && <span className="mono" style={{ fontWeight: 400, marginLeft: 6 }}>({rule.source_id})</span>}
      </div>
      <Grid>
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
      {rule.source
        ? (
          <pre className="codeblock" style={{ marginTop: 8, maxHeight: '50vh', overflow: 'auto', fontSize: 11 }}>
            {rule.source}
          </pre>
        )
        : (
          <div style={{ fontSize: 11, color: 'var(--mute)', marginTop: 8 }}>
            Broncode niet gevonden.
          </div>
        )}
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
