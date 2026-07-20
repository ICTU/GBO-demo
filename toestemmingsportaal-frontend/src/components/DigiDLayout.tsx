import { ReactNode } from 'react'

type Props = { children: ReactNode }

export default function DigiDLayout({ children }: Props) {
  return (
    <div className="digid-page">
      <div className="digid-container">
        <div className="digid-surface">
          <div className="digid-surface-header">
            <button className="digid-lang" type="button">
              <span>Nederlands</span>
              <svg width="12" height="12" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2.5">
                <path d="M6 9l6 6 6-6" />
              </svg>
            </button>
            <img src="/rijkswapen.png" alt="Rijksoverheid" className="digid-rijkswapen" />
          </div>

          <div className="digid-content">{children}</div>

          <div className="digid-bottombar">
            <div className="digid-bottombar-inner" aria-hidden />
          </div>
        </div>
      </div>
    </div>
  )
}
