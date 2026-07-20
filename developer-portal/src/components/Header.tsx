import { useTheme } from '../hooks/useTheme'

export default function Header() {
  const { theme, toggle } = useTheme()
  return (
    <div className="hdr">
      <div className="logo">
        <div className="logo-mark">G</div>
        <div className="logo-txt">
          <b>GBO · Developer Portal</b>
          <span>internal tooling</span>
        </div>
      </div>
      <span className="badge badge-demo">Demo-omgeving</span>
      <div className="env-pick">
        <span className="env-dot" aria-hidden />
        <select defaultValue="lokaal">
          <option value="lokaal">lokaal</option>
        </select>
      </div>
      <div style={{ marginLeft: 'auto', display: 'flex', gap: 8 }}>
        <button className="hbtn" type="button" onClick={toggle}>
          {theme === 'dark' ? '☀ Licht' : '☾ Donker'}
        </button>
      </div>
    </div>
  )
}
