import type { ReactNode } from 'react'

/** Shared chrome for the Hypotheek-BV dossier screens (Start + Return). */

export function HeaderBar({ right }: { right?: ReactNode }) {
  return (
    <header className="hb-header">
      <div className="hb-header-left">
        <span className="hb-logo">H</span>
        <span className="hb-brand">Hypotheek-BV</span>
      </div>
      {right}
    </header>
  )
}

export const DossierUser = () => (
  <div className="hb-header-right">
    <span className="user-icon" aria-hidden /> J. de Vries · Mijn dossier
  </div>
)

export const HeaderStage = ({ label }: { label: string }) => (
  <div className="hb-header-stage">{label}</div>
)

/** The two dossier parts that are already complete on every screen. */
export const CompletedDossierItems = () => (
  <>
    <ChecklistItem
      state="done"
      title="Persoonlijke gegevens"
      meta="Naam, adres, contactgegevens"
      status="Compleet"
    />
    <ChecklistItem
      state="done"
      title="Woning & aankoop"
      meta="Aankoopprijs € 425.000 · gewenste hypotheek € 380.000"
      status="Compleet"
    />
  </>
)

export function ChecklistItem({
  state,
  title,
  meta,
  status,
}: {
  state: 'done' | 'todo'
  title: string
  meta: ReactNode
  status: ReactNode
}) {
  return (
    <li className={`hb-check-item${state === 'todo' ? ' pending' : ''}`}>
      <span className={`hb-check-icon ${state}`} aria-hidden>{state === 'done' ? '✓' : '+'}</span>
      <div className="hb-check-body">
        <div className="hb-check-title">{title}</div>
        <div className="hb-check-meta">{meta}</div>
      </div>
      <span className={`hb-check-status ${state}`}>{status}</span>
    </li>
  )
}

/** A completed checklist item that can be expanded to reveal the fetched data. */
export function ExpandableChecklistItem({
  state,
  title,
  meta,
  status,
  open,
  onToggle,
  children,
}: {
  state: 'done' | 'todo'
  title: string
  meta: ReactNode
  status: ReactNode
  open: boolean
  onToggle: () => void
  children: ReactNode
}) {
  return (
    <li className={`hb-check-item expandable${state === 'todo' ? ' pending' : ''}${open ? ' open' : ''}`}>
      <button type="button" className="hb-check-toggle" aria-expanded={open} onClick={onToggle}>
        <span className={`hb-check-icon ${state}`} aria-hidden>{state === 'done' ? '✓' : '!'}</span>
        <div className="hb-check-body">
          <div className="hb-check-title">{title}</div>
          <div className="hb-check-meta">{meta}</div>
        </div>
        <span className={`hb-check-status ${state}`}>{status}</span>
        <span className="hb-chevron" aria-hidden>
          <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2.5">
            <path d="M6 9l6 6 6-6" />
          </svg>
        </span>
      </button>
      {open && <div className="hb-check-panel">{children}</div>}
    </li>
  )
}
