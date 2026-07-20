import { useEffect, useMemo } from 'react'
import { useNavigate, useSearchParams } from 'react-router-dom'
import MijnOverheidLayout from '../components/MijnOverheidLayout'
import ConsentTabs from '../components/consent-list/ConsentTabs'
import ConsentTable from '../components/consent-list/ConsentTable'
import EmptyState from '../components/consent-list/EmptyState'
import FaqAccordion from '../components/consent-list/FaqAccordion'
import { useConsents } from '../hooks/useConsents'
import { usePortalToken } from '../hooks/usePortalToken'

export default function MijnToestemmingenOverview() {
  const { token } = usePortalToken()
  const navigate = useNavigate()
  const [params, setParams] = useSearchParams()
  const tab = (params.get('tab') as 'actief' | 'verlopen') || 'actief'

  useEffect(() => {
    if (!token) {
      navigate(`/auth?next=${encodeURIComponent('/mijnoverheid/toestemmingen')}`)
    }
  }, [token, navigate])

  const { consents, loading, error, refresh } = useConsents()

  const { actief, verlopen } = useMemo(() => {
    // Newest → oldest by issuance date, so the most recent consent
    // is on top (same pattern as other MijnOverheid overviews).
    const all = [...(consents ?? [])].sort((a, b) =>
      b.created_at.localeCompare(a.created_at),
    )
    return {
      actief: all.filter((c) => c.effective_status === 'active'),
      verlopen: all.filter((c) => c.effective_status !== 'active'),
    }
  }, [consents])

  const rows = tab === 'actief' ? actief : verlopen

  return (
    <MijnOverheidLayout
      activeNav="toestemmingen"
      breadcrumb={[
        { label: 'Home', href: '#' },
        { label: 'Toestemmingen', href: '#' },
        { label: 'Overzicht' },
      ]}
    >
      <h1 className="page-title">Mijn toestemmingen</h1>

      <div className="content-card">
        <ConsentTabs
          active={tab}
          onChange={(t) => setParams({ tab: t })}
          counts={{ actief: actief.length, verlopen: verlopen.length }}
        />

        {loading && <div className="consent-empty">Laden…</div>}
        {error && (
          <div className="consent-error">
            Kon toestemmingen niet ophalen: {error}{' '}
            <button className="btn-link-inline" onClick={refresh}>Opnieuw proberen</button>
          </div>
        )}
        {!loading && !error && rows.length === 0 && <EmptyState tab={tab} />}
        {!loading && !error && rows.length > 0 && <ConsentTable rows={rows} />}
      </div>

      <FaqAccordion />
    </MijnOverheidLayout>
  )
}
