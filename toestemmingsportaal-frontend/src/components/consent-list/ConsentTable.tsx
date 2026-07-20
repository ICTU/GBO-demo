import { useNavigate } from 'react-router-dom'
import { ConsentRecord } from '../../api/portalClient'
import { deriveAfnemerName, deriveOnderwerp, formatGeldigTot } from '../../data/onderwerpMap'

type Props = { rows: ConsentRecord[] }

const ChevronRight = () => (
  <svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="#01689B" strokeWidth="2.5">
    <path d="M9 6l6 6-6 6" />
  </svg>
)

export default function ConsentTable({ rows }: Props) {
  const navigate = useNavigate()

  return (
    <div className="consent-table" role="table">
      <div className="consent-table-head" role="row">
        <div role="columnheader">Afnemer</div>
        <div role="columnheader">Onderwerp</div>
        <div role="columnheader" className="consent-table-geldig">Geldig tot</div>
      </div>
      {rows.map((r) => (
        <button
          key={r.consent_id}
          role="row"
          className="consent-row"
          onClick={() => navigate(`/mijnoverheid/toestemmingen/${encodeURIComponent(r.consent_id)}`)}
        >
          <div role="cell" className="consent-row-afnemer">
            {deriveAfnemerName(r.dienstverlener_oin)}
          </div>
          <div role="cell">{deriveOnderwerp(r.scopes)}</div>
          <div role="cell" className="consent-row-geldig">
            <span className="mono">{formatGeldigTot(r.valid_until)}</span>
            <ChevronRight />
          </div>
        </button>
      ))}
    </div>
  )
}
