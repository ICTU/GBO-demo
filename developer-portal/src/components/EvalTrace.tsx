import { useState } from 'react'

// Renders a rule's cascade-trace: each check-axis with its status
// (pass / fail / skipped) — equivalent to iWlz' "Toon PDP-evaluatie
// (N stappen)" feature, but driven by our RFC0052-versie-GBO lib
// output where each rule's evaluate() emits the full step-list.
//
// Steps come from policy via:
//   ALLOW: granted[i].steps          (engine: granted_steps)
//   DENY:  denied_fields[i].evaluated[j].steps
//
// Each step: {code, label, expected, status: "pass"|"fail"|"skipped"}.

export type EvalStep = {
  code: string
  label: string
  expected: string
  status: 'pass' | 'fail' | 'skipped'
}

const GLYPH: Record<EvalStep['status'], string> = {
  pass: '✓', fail: '✗', skipped: '○',
}
const COLOR: Record<EvalStep['status'], string> = {
  pass: 'var(--allow-br)', fail: 'var(--deny-br)', skipped: 'var(--mute)',
}

type Props = {
  ruleId: string
  steps: EvalStep[]
  defaultOpen?: boolean
}

export default function EvalTrace({ ruleId, steps, defaultOpen }: Props) {
  const [open, setOpen] = useState(!!defaultOpen)
  if (steps.length === 0) return null
  const passCount = steps.filter((s) => s.status === 'pass').length
  const failCount = steps.filter((s) => s.status === 'fail').length
  return (
    <div style={{ marginTop: 6 }}>
      <button
        onClick={(e) => { e.stopPropagation(); setOpen(!open) }}
        style={{
          background: 'transparent', border: 'none', cursor: 'pointer',
          fontSize: 11, color: 'var(--text-2)', padding: '2px 0',
          display: 'flex', alignItems: 'baseline', gap: 6,
        }}
      >
        <span style={{ fontSize: 9, color: 'var(--mute)' }}>{open ? '▼' : '▶'}</span>
        <span>Toon PDP-evaluatie ({steps.length} stappen — {passCount} pass{failCount > 0 ? `, ${failCount} fail` : ''})</span>
      </button>
      {open && (
        <div style={{
          marginTop: 4, padding: '8px 10px', borderRadius: 5,
          background: 'var(--panel-2)', border: '1px solid var(--border-2)',
        }}>
          <div style={{ display: 'flex', flexDirection: 'column', gap: 3 }}>
            {steps.map((s, i) => (
              <div
                key={s.code + i}
                style={{
                  display: 'grid', gridTemplateColumns: '14px 1fr', gap: 8,
                  alignItems: 'baseline', padding: '2px 0',
                  opacity: s.status === 'skipped' ? 0.55 : 1,
                }}
              >
                <span style={{ color: COLOR[s.status], fontWeight: 800, textAlign: 'center' }}>
                  {GLYPH[s.status]}
                </span>
                <div style={{ minWidth: 0 }}>
                  <span style={{ fontSize: 11.5, color: s.status === 'fail' ? 'var(--text)' : 'var(--text-2)' }}>
                    {i + 1}. {s.label}
                  </span>
                  <div className="mono" style={{ fontSize: 10, color: 'var(--mute)', marginTop: 1 }}>
                    {s.expected}
                  </div>
                </div>
              </div>
            ))}
          </div>
          <div style={{ fontSize: 10, color: 'var(--mute)', marginTop: 6, fontStyle: 'italic' }}>
            Cascade van regel <span className="mono" style={{ color: 'var(--text-2)' }}>{ruleId}</span>.
            "○ skipped" = short-circuit, gestopt na eerste fail.
          </div>
        </div>
      )}
    </div>
  )
}
