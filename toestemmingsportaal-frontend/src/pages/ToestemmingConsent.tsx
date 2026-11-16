import { useMemo, useState } from 'react'
import MijnOverheidLayout from '../components/MijnOverheidLayout'
import ScopeToggle from '../components/consent/ScopeToggle'
import PartialConsentWarning from '../components/consent/PartialConsentWarning'
import { requestedScopeGroups } from '../data/scopeGroups'
import type { ConsentRequest } from '../consentStart/startFlow'

function formatValidUntil(iso?: string): string {
  if (!iso) return '3 maanden na toestemming'
  const d = new Date(iso)
  if (isNaN(d.getTime())) return iso
  return d.toLocaleDateString('nl-NL', { year: 'numeric', month: 'long', day: 'numeric' })
}

type Props = {
  request: ConsentRequest
  deciding: boolean
  onGrant: (scopes: string[]) => void
  onDeny: () => void
}

// The consent screen of a resolved request. Name, purpose and data sets come
// from the backend, which takes name and purpose from the provider's
// registration. The citizen grants or refuses; there is no way back without
// a decision, because the portal does not know the return URL.
export default function ToestemmingConsent({ request, deciding, onGrant, onDeny }: Props) {
  const scopeGroups = useMemo(() => requestedScopeGroups(request.scopes).groups, [request])
  const [picked, setPicked] = useState<Record<string, boolean>>(() =>
    Object.fromEntries(scopeGroups.map((s) => [s.code, true])),
  )
  const chosen = scopeGroups.filter((s) => picked[s.code]).map((s) => s.code)
  const partial = chosen.length > 0 && chosen.length < scopeGroups.length

  return (
    <MijnOverheidLayout
      activeNav="toestemmingen"
      breadcrumb={[{ label: 'Home' }, { label: 'Toestemming geven' }]}
    >
      <div className="content-card">
        <h1 className="page-title">
          Welke gegevens mag <span className="accent">{request.client_name}</span> opvragen?
        </h1>

        <div className="summary-grid">
          <div>
            <div className="summary-label">Wie vraagt</div>
            <div className="summary-value">{request.client_name}</div>
          </div>
          <div>
            <div className="summary-label">Doel</div>
            <div className="summary-value">{request.purpose}</div>
          </div>
          <div>
            <div className="summary-label">Geldig tot</div>
            <div className="summary-value">{formatValidUntil(request.valid_until)}</div>
          </div>
        </div>

        <p className="guidance">
          Kies welke gegevens u wilt delen. U kunt uw toestemming later intrekken onder{' '}
          <strong>Toestemmingen</strong>.
        </p>

        <div className="scope-list">
          {scopeGroups.map((s) => (
            <ScopeToggle
              key={s.code}
              scope={s}
              checked={!!picked[s.code]}
              onChange={(v) => setPicked((p) => ({ ...p, [s.code]: v }))}
            />
          ))}
        </div>

        {partial && (
          <div style={{ marginTop: 16 }}>
            <PartialConsentWarning clientName={request.client_name} />
          </div>
        )}

        <div className="actions">
          <button
            className="btn btn-primary"
            onClick={() => onGrant(chosen)}
            disabled={deciding || chosen.length === 0}
          >
            {deciding ? 'Bezig…' : 'Geef toestemming'}
          </button>
          <button className="btn btn-secondary" onClick={onDeny} disabled={deciding}>
            Weiger
          </button>
          <span className="counter">
            {chosen.length} van {scopeGroups.length} onderdelen geselecteerd
          </span>
        </div>
      </div>
    </MijnOverheidLayout>
  )
}
