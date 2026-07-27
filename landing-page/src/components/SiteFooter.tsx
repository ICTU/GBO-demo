import { versionString } from '../config'

export default function SiteFooter() {
  return (
    <footer className="site-footer">
      <div className="shell site-footer-row">
        <span>ICTU · Datastelsel NL · FSC-simulatie</span>
        <span className="site-footer-meta">
          <span>© {new Date().getFullYear()} ICTU</span>
          {versionString && <span>{versionString}</span>}
        </span>
      </div>
    </footer>
  )
}
