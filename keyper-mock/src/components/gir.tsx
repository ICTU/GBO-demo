import type { ReactNode } from 'react'
import { Link } from 'react-router'
import { toepassing, type Aanvraag } from '../lib/aanvraag'
import { DocIcon, HistoryIcon, HouseMark, InfoIcon } from './icons'

type NavTab = { label: string; to: string; icon?: ReactNode }

const DEFAULT_TABS: NavTab[] = [
  { label: 'Nieuwe aanvraag', to: '/', icon: <DocIcon /> },
  { label: 'Vorige aanvragen', to: '/aanvraag', icon: <HistoryIcon /> },
]

// The GIR Toestemmingsportaal chrome: brand, preview pill and tab nav.
export function GirShell({
  active,
  tabs = DEFAULT_TABS,
  children,
}: {
  active: string
  tabs?: NavTab[]
  children: ReactNode
}) {
  return (
    <div className="fr">
      <div className="g-top">
        <div className="g-brand">
          <span className="g-mark">
            <HouseMark />
          </span>
          <b>GIR Toestemmingsportaal</b>
          <span className="g-pill">Preview</span>
        </div>
        <nav className="g-nav">
          {tabs.map((tab) => (
            <Link key={tab.label} to={tab.to} className={tab.label === active ? 'on' : undefined}>
              {tab.icon}
              {tab.label}
            </Link>
          ))}
        </nav>
      </div>
      <div className="g-body">{children}</div>
    </div>
  )
}

// The owner's approval pages carry a slimmer brand bar.
export function ApprovalShell({ children }: { children: ReactNode }) {
  return (
    <div className="fr">
      <div className="g-mini">
        <span className="m">
          <HouseMark />
        </span>
        Gebouw Installatie Registratie
      </div>
      <div className="g-body" style={{ padding: '34px 26px' }}>
        <div className="g-ctr">{children}</div>
      </div>
    </div>
  )
}

const STEPS = ['Installateur', 'Gebouw', 'Reikwijdte', 'Eigenaar', 'Controle']

export function Stepper({ current }: { current: number }) {
  return (
    <div className="g-step" style={{ marginBottom: '14px' }}>
      {STEPS.map((step, i) => {
        const n = i + 1
        const state = n < current ? 'done' : n === current ? 'on' : ''
        return (
          <span key={step} style={{ display: 'contents' }}>
            {i > 0 && <div className="g-sp" />}
            <div className={`g-si ${state}`}>
              <span className="g-sn">{n < current ? '✓' : n}</span>
              {step}
            </div>
          </span>
        )
      })}
    </div>
  )
}

export function InfoBar({ children }: { children: ReactNode }) {
  return (
    <div className="g-info" style={{ marginBottom: '16px' }}>
      <InfoIcon />
      {children}
    </div>
  )
}

// The request summary beside the wizard; fields not yet filled show dimmed.
export function Overzicht({ aanvraag, step }: { aanvraag: Aanvraag; step: number }) {
  const rows: [string, string | null][] = [
    ['Toepassing', toepassing(aanvraag.useCase)],
    ['Modus', 'Installateur'],
    ['Installateur', aanvraag.installateur.naam],
    ['Gebouw', step >= 2 ? `${aanvraag.gebouw.adres}, ${aanvraag.gebouw.plaats}` : null],
    ['Eigenaar', step >= 4 ? aanvraag.eigenaar.naam : null],
    ['Validiteit', step >= 3 ? `${aanvraag.reikwijdte.van} → ${aanvraag.reikwijdte.tot}` : null],
  ]
  return (
    <div className="g-card g-side">
      <div className="g-cb">
        <b>Aanvraag-overzicht</b>
        <dl>
          {rows.map(([label, value]) => (
            <div className="g-kv" key={label}>
              <dt>{label}</dt>
              {value === null ? (
                <dd className="dim">{label === 'Gebouw' ? 'Nog geen' : '—'}</dd>
              ) : (
                <dd>{value}</dd>
              )}
            </div>
          ))}
        </dl>
      </div>
    </div>
  )
}
