import { ReactNode } from 'react'

type NavKey =
  | 'home'
  | 'berichtenbox'
  | 'lopende-zaken'
  | 'toestemmingen'
  | 'identiteit'
  | 'financien'
  | 'werk'
  | 'gezondheid'
  | 'wonen'
  | 'vervoer'
  | 'onderwijs'
  | 'instellingen'

const I = {
  home: (
    <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.6">
      <path d="M3 11l9-7 9 7v9a1 1 0 0 1-1 1h-5v-6h-6v6H4a1 1 0 0 1-1-1v-9z" />
    </svg>
  ),
  mail: (
    <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.6">
      <rect x="3" y="5" width="18" height="14" rx="1.5" />
      <path d="M3 7l9 6 9-6" />
    </svg>
  ),
  list: (
    <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.6">
      <rect x="4" y="4" width="16" height="16" rx="1.5" />
      <path d="M8 9h8M8 13h8M8 17h5" />
    </svg>
  ),
  check: (
    <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.6">
      <rect x="4" y="4" width="16" height="16" rx="1.5" />
      <path d="M8 12l3 3 5-6" />
    </svg>
  ),
  user: (
    <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.6">
      <circle cx="12" cy="8" r="4" />
      <path d="M4 21c1-4 5-6 8-6s7 2 8 6" />
    </svg>
  ),
  money: (
    <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.6">
      <rect x="3" y="6" width="18" height="12" rx="2" />
      <circle cx="12" cy="12" r="2.5" />
    </svg>
  ),
  briefcase: (
    <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.6">
      <rect x="3" y="7" width="18" height="13" rx="1.5" />
      <path d="M9 7V5a1 1 0 0 1 1-1h4a1 1 0 0 1 1 1v2" />
    </svg>
  ),
  heart: (
    <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.6">
      <path d="M12 21s-7-4.5-7-10a4 4 0 0 1 7-2.6A4 4 0 0 1 19 11c0 5.5-7 10-7 10z" />
    </svg>
  ),
  house: (
    <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.6">
      <path d="M4 11l8-6 8 6v9h-5v-6h-6v6H4z" />
    </svg>
  ),
  car: (
    <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.6">
      <path d="M4 14l1.5-5h13L20 14M4 14v4h2v-2h12v2h2v-4M4 14h16" />
      <circle cx="7.5" cy="16.5" r="1.2" />
      <circle cx="16.5" cy="16.5" r="1.2" />
    </svg>
  ),
  cap: (
    <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.6">
      <path d="M2 9l10-4 10 4-10 4L2 9z" />
      <path d="M6 11v4c0 1.5 3 3 6 3s6-1.5 6-3v-4" />
    </svg>
  ),
  gear: (
    <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.6">
      <circle cx="12" cy="12" r="3" />
      <path d="M19 12a7 7 0 0 0-.1-1.2l2-1.5-2-3.4-2.4.9a7 7 0 0 0-2-1.1L14 3.5h-4l-.5 2.2a7 7 0 0 0-2 1.1l-2.4-.9-2 3.4 2 1.5A7 7 0 0 0 5 12c0 .4 0 .8.1 1.2l-2 1.5 2 3.4 2.4-.9a7 7 0 0 0 2 1.1L10 20.5h4l.5-2.2a7 7 0 0 0 2-1.1l2.4.9 2-3.4-2-1.5c.1-.4.1-.8.1-1.2z" />
    </svg>
  ),
}

const NAV_ITEMS: { key: NavKey; label: string; icon: ReactNode }[] = [
  { key: 'home', label: 'Home', icon: I.home },
  { key: 'berichtenbox', label: 'Berichtenbox', icon: I.mail },
  { key: 'lopende-zaken', label: 'Lopende zaken', icon: I.list },
  { key: 'toestemmingen', label: 'Toestemmingen', icon: I.check },
  { key: 'identiteit', label: 'Identiteit', icon: I.user },
  { key: 'financien', label: 'Financiën', icon: I.money },
  { key: 'werk', label: 'Werk', icon: I.briefcase },
  { key: 'gezondheid', label: 'Gezondheid', icon: I.heart },
  { key: 'wonen', label: 'Wonen', icon: I.house },
  { key: 'vervoer', label: 'Vervoer', icon: I.car },
  { key: 'onderwijs', label: 'Onderwijs', icon: I.cap },
  { key: 'instellingen', label: 'Instellingen', icon: I.gear },
]

type Props = {
  activeNav: NavKey
  breadcrumb: { label: string; href?: string }[]
  children: ReactNode
}

export default function MijnOverheidLayout({ activeNav, breadcrumb, children }: Props) {
  return (
    <div className="mo-shell">
      <header className="mo-topbar">
        <div className="mo-topbar-inner">
          <div className="mo-wordmark">MijnOverheid</div>
        </div>
        <img src="/rijkswapen.png" alt="Rijksoverheid" className="mo-rijkswapen" />
      </header>

      <div className="mo-container">
        <div className="mo-body">
          <nav className="mo-nav" aria-label="Hoofdnavigatie">
            {NAV_ITEMS.map((item) => (
              <a
                key={item.key}
                href="#"
                className={`mo-nav-item${activeNav === item.key ? ' active' : ''}`}
                onClick={(e) => e.preventDefault()}
              >
                <span className="mo-nav-icon" aria-hidden>{item.icon}</span>
                <span>{item.label}</span>
              </a>
            ))}
          </nav>
          <main className="mo-content">
            <nav className="mo-breadcrumb" aria-label="Kruimelpad">
              {breadcrumb.map((b, i) => (
                <span key={i}>
                  {i > 0 && <span className="sep">›</span>}
                  {b.href ? <a href={b.href} onClick={(e) => e.preventDefault()}>{b.label}</a> : b.label}
                </span>
              ))}
            </nav>
            {children}
          </main>
        </div>
      </div>
    </div>
  )
}
