import { useEffect, useState } from 'react'
import { ConsentUsage, getConsentUses } from '../../api/portalClient'
import { formatHistorieDate } from '../../data/onderwerpMap'

type Props = { token: string; consentId: string }

// When the consent was used: one line per delivery a source registered under
// it, newest first. "Not used" and "cannot be fetched now" stay apart: when
// the uses cannot be fetched the citizen gets a notice, never an empty list
// (decision D2, after FR-4 of the TransparantieApp report).
export default function UsageSection({ token, consentId }: Props) {
  const [usage, setUsage] = useState<ConsentUsage | null>(null)
  const [error, setError] = useState<string | null>(null)

  useEffect(() => {
    let cancelled = false
    setUsage(null)
    setError(null)
    getConsentUses(token, consentId)
      .then((u) => !cancelled && setUsage(u))
      .catch((err: Error) => !cancelled && setError(err.message))
    return () => {
      cancelled = true
    }
  }, [token, consentId])

  if (error) {
    return (
      <div className="partial-warning usage-notice" role="status">
        <span className="icon" aria-hidden>!</span>
        <span>Het gebruik van deze toestemming kan nu niet worden opgehaald. Probeer het later opnieuw.</span>
      </div>
    )
  }
  if (!usage) return <div className="consent-empty">Laden…</div>

  const indexUnavailable = usage.notices.some((n) => n.kind === 'index_unavailable')
  const sourceNotices = usage.notices.filter((n) => n.kind === 'source_unavailable' && n.source)

  if (indexUnavailable) {
    return (
      <div className="partial-warning usage-notice" role="status">
        <span className="icon" aria-hidden>!</span>
        <span>
          Op dit moment kunnen we niet ophalen wanneer deze toestemming is gebruikt. Dat betekent niet
          dat ze niet is gebruikt. Probeer het later opnieuw.
        </span>
      </div>
    )
  }

  return (
    <>
      {sourceNotices.map((n) => (
        <div key={n.source} className="partial-warning usage-notice" role="status">
          <span className="icon" aria-hidden>!</span>
          <span>
            Het gebruik bij {n.source} kan nu niet worden opgehaald.
            {usage.uses.length > 0 && ' Er kan daar meer gebruik zijn dan hieronder staat.'} Probeer het
            later opnieuw.
          </span>
        </div>
      ))}
      {usage.uses.length === 0 && sourceNotices.length === 0 && (
        <div className="consent-empty">Deze toestemming is nog niet gebruikt.</div>
      )}
      {usage.uses.length > 0 && (
        <ol className="usage-list">
          {usage.uses.map((u, i) => (
            <li key={`${u.used_at}-${i}`} className="usage-item">
              <div className="usage-date">{formatHistorieDate(u.used_at)}</div>
              <div className="usage-body">
                <span className="usage-afnemer">{u.service_provider}</span> heeft
                gegevens ontvangen van <span className="usage-source">{u.source}</span>
              </div>
            </li>
          ))}
        </ol>
      )}
    </>
  )
}
