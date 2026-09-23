// What the installer gets (design screens 12, 14 and 17). Before releasing
// anything, IR asks LVG again whether the citizen still owns the building,
// with the consent token it kept (steps 29–33 of the pilot's diagram). Once
// the owner revokes the DvTP consent in MijnOverheid, that check is refused
// and IR releases nothing.
import { useCallback, useEffect, useState } from 'react'
import { Link } from 'react-router'
import { formatDay, loadAanvraag, type Aanvraag } from '../lib/aanvraag'
import { checkOwnership, type Outcome } from '../lib/ownership'
import { myConsentsUrl } from '../lib/portal'
import { GirShell } from '../components/gir'
import { HouseMark } from '../components/icons'

function useRecurringCheck(aanvraag: Aanvraag | null) {
  const [outcome, setOutcome] = useState<Outcome | 'checking'>('checking')
  const [checkedAt, setCheckedAt] = useState(0)

  const check = useCallback(() => {
    if (!aanvraag?.consentToken) {
      setOutcome('unconfirmed')
      return
    }
    setOutcome('checking')
    checkOwnership(aanvraag.consentToken, aanvraag.gebouw.vboId).then((result) => {
      setOutcome(result.outcome)
      setCheckedAt(Date.now())
    })
  }, [aanvraag?.consentToken, aanvraag?.gebouw.vboId])

  useEffect(check, [check])
  return { outcome, checkedAt, check }
}

function Checking() {
  return (
    <div className="g-note n-blue" style={{ marginBottom: '16px' }}>
      <span>…</span>
      <div>Eigendom controleren bij de LVG voordat er gegevens worden vrijgegeven…</div>
    </div>
  )
}

// Why IR releases nothing, as the pilot's diagram tells the installer: the
// PDP refused the check (no valid consent, e.g. revoked), or LVG answered
// that the citizen of the consent does not own this building.
const BLOCKED = {
  unconfirmed: {
    title: 'Geen toestemming',
    text: 'Het Installatie Register heeft geen geldige toestemming meer om het eigendom van dit pand bij de LVG te controleren, bijvoorbeeld omdat de eigenaar die heeft ingetrokken.',
    check: 'Geen geldige toestemming',
  },
  'not-owner': {
    title: 'Geen eigenaarschap vastgesteld',
    text: 'Volgens de LVG is degene die toestemming gaf niet de eigenaar van dit pand.',
    check: 'Geen eigenaarschap vastgesteld',
  },
} as const

function Blocked({ what, outcome }: { what: string; outcome: 'unconfirmed' | 'not-owner' }) {
  const reason = BLOCKED[outcome]
  return (
    <div className="g-card" style={{ borderColor: '#FCC7C2' }}>
      <div className="g-cb" style={{ display: 'flex', gap: '18px', alignItems: 'flex-start' }}>
        <span
          style={{
            width: '42px',
            height: '42px',
            borderRadius: '10px',
            background: 'var(--g-red-50)',
            color: 'var(--g-red-tx)',
            display: 'grid',
            placeItems: 'center',
            flex: '0 0 42px',
            fontWeight: 700,
          }}
        >
          !
        </span>
        <div style={{ flex: '1' }}>
          <h3 style={{ fontSize: '17px', fontWeight: 700, marginBottom: '7px' }}>{reason.title}</h3>
          <p style={{ fontSize: '13.5px', lineHeight: 1.65, color: 'var(--g-lbl)', marginBottom: '16px' }}>
            {reason.text} Er worden geen {what} vrijgegeven. Neem contact op met de eigenaar of dien een nieuwe
            aanvraag in.
          </p>
          <div style={{ background: '#F8FAFC', borderRadius: '9px', padding: '15px 17px', marginBottom: '16px' }}>
            <dl className="g-dl" style={{ gridTemplateColumns: '160px 1fr' }}>
              <dt>Laatste controle</dt>
              <dd>{reason.check}</dd>
              <dt>Gevolg</dt>
              <dd>Opvraging niet uitgevoerd</dd>
              <dt>Vervolgactie</dt>
              <dd>Nieuwe aanvraag indienen bij de eigenaar</dd>
            </dl>
          </div>
          <Link to="/" className="g-btn g-o" style={{ textDecoration: 'none' }}>
            Nieuwe aanvraag starten
          </Link>
        </div>
      </div>
    </div>
  )
}

function Actions({ onRefresh }: { onRefresh: () => void }) {
  return (
    <div style={{ display: 'flex', gap: '11px', alignItems: 'center', marginTop: '16px', paddingTop: '16px', borderTop: '1px solid var(--g-bd)' }}>
      <button className="g-btn g-o" onClick={onRefresh}>
        Opnieuw ophalen
      </button>
      {/* MijnOverheid opens beside the demo, so the presenter can switch
          back to this tab after revoking. */}
      <a href={myConsentsUrl()} target="_blank" rel="noreferrer" className="g-btn g-t" style={{ marginLeft: 'auto', textDecoration: 'none' }}>
        Als eigenaar: toestemming intrekken ↗
      </a>
    </div>
  )
}

function NoAccess() {
  return (
    <GirShell active="Installatiegegevens">
      <h1 className="g-h1">Geen actieve toegang</h1>
      <p className="g-sub">
        Er is geen goedgekeurde aanvraag. <Link to="/">Start een nieuwe aanvraag.</Link>
      </p>
    </GirShell>
  )
}

const INSTALLATIES = [
  { id: 'INS-0632-4471', type: 'Warmtepomp (lucht/water)', model: 'Thermex AW-8', jaar: 2021, onderhoud: '14 maart 2026', status: ['c-ok', 'In bedrijf'] },
  { id: 'INS-0632-4472', type: 'Cv-ketel (HR107)', model: 'Verwarm B24', jaar: 2012, onderhoud: '2 november 2025', status: ['c-ok', 'In bedrijf'] },
  { id: 'INS-0632-4473', type: 'Mechanische ventilatie', model: 'Airflow MV-3', jaar: 1998, onderhoud: '18 juni 2024', status: ['c-wait', 'Keuring verlopen'] },
  { id: 'INS-0632-4474', type: 'Zonneboiler', model: 'SolTherm 200L', jaar: 2019, onderhoud: '9 april 2026', status: ['c-ok', 'In bedrijf'] },
]

// ── 12 / 14 · installatiegegevens ─────────────────────────────────────────

export function Installatiegegevens() {
  const aanvraag = loadAanvraag()
  const { outcome, checkedAt, check } = useRecurringCheck(aanvraag)
  if (!aanvraag?.goedgekeurd) return <NoAccess />
  const g = aanvraag.gebouw
  const blocked = outcome === 'not-owner' || outcome === 'unconfirmed'

  return (
    <GirShell
      active="Installatiegegevens"
      tabs={[
        { label: 'Installatiegegevens', to: '/installatiegegevens' },
        { label: 'Vorige aanvragen', to: '/aanvraag' },
      ]}
    >
      <div style={{ display: 'flex', alignItems: 'flex-start', gap: '14px', marginBottom: '14px' }}>
        <div>
          <h1 className="g-h1" style={{ fontSize: '20px' }}>
            Installatiegegevens — {g.adres}, {g.plaats}
          </h1>
          <p className="g-sub">
            VBO-id <span className="mono">{g.vboId}</span>
            {!blocked && ` · toestemming geldig tot ${formatDay(aanvraag.reikwijdte.tot)}`}
          </p>
        </div>
        {outcome !== 'checking' && (
          <span className={`g-chip ${blocked ? 'c-bad' : 'c-ok'}`} style={{ marginLeft: 'auto', marginTop: '4px' }}>
            {blocked ? BLOCKED[outcome].title : 'Toegang actief'}
          </span>
        )}
      </div>

      {outcome === 'checking' && <Checking />}
      {blocked && <Blocked outcome={outcome} what="installatiegegevens" />}
      {outcome === 'owner' && (
        <>
          <div className="g-note n-ok" style={{ marginBottom: '16px' }} key={checkedAt}>
            <span>✓</span>
            <div>
              Eigendom opnieuw gecontroleerd bij de LVG vóór het ophalen — de eigenaar is nog steeds eigenaar van dit
              pand.
            </div>
          </div>
          <div className="g-card">
            <div className="g-cb" style={{ padding: '18px 8px 8px' }}>
              <table className="g-tbl">
                <thead>
                  <tr>
                    <th>Installatie-ID</th>
                    <th>Type</th>
                    <th>Merk / model</th>
                    <th>Bouwjaar</th>
                    <th>Laatste onderhoud</th>
                    <th>Status</th>
                  </tr>
                </thead>
                <tbody>
                  {INSTALLATIES.map((row) => (
                    <tr key={row.id}>
                      <td className="mono">{row.id}</td>
                      <td>{row.type}</td>
                      <td>{row.model}</td>
                      <td>{row.jaar}</td>
                      <td>{row.onderhoud}</td>
                      <td>
                        <span className={`g-chip ${row.status[0]}`}>{row.status[1]}</span>
                      </td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          </div>
          <p style={{ fontSize: '11.5px', color: 'var(--g-soft)', marginTop: '12px', lineHeight: 1.5 }}>
            Deze gegevens zijn opgehaald op basis van de toestemming van de eigenaar. Elke opvraging wordt gelogd en is
            terug te vinden in de audittrail van het Installatie Register.
          </p>
        </>
      )}
      {outcome !== 'checking' && !blocked && <Actions onRefresh={check} />}
      {blocked && (
        <div style={{ marginTop: '16px' }}>
          <button className="g-btn g-t" onClick={check}>
            Opnieuw proberen
          </button>
        </div>
      )}
    </GirShell>
  )
}

// ── 17 · onderhoudshistorie in de installateurssoftware (use case 2) ─────

const HISTORIE = [
  { datum: '14 maart 2026', installatie: 'INS-0632-4471', handeling: 'Jaarlijkse keuring warmtepomp', resultaat: ['c-ok', 'Goedgekeurd'] },
  { datum: '2 november 2025', installatie: 'INS-0632-4472', handeling: 'Onderhoud cv-ketel', resultaat: ['c-ok', 'Goedgekeurd'] },
  { datum: '21 mei 2025', installatie: 'INS-0632-4471', handeling: 'Vervanging expansievat', resultaat: ['c-ok', 'Afgerond'] },
  { datum: '18 juni 2024', installatie: 'INS-0632-4473', handeling: 'Keuring mechanische ventilatie', resultaat: ['c-wait', 'Opmerking'] },
]

export function Onderhoudshistorie() {
  const aanvraag = loadAanvraag()
  const { outcome, check } = useRecurringCheck(aanvraag)
  if (!aanvraag?.goedgekeurd) return <NoAccess />
  const g = aanvraag.gebouw
  const blocked = outcome === 'not-owner' || outcome === 'unconfirmed'

  return (
    <div className="fr">
      <div className="g-top" style={{ background: '#0B3B36' }}>
        <div className="g-brand">
          <span className="g-mark" style={{ background: '#12B76A' }}>
            <HouseMark />
          </span>
          <b style={{ color: '#fff' }}>InstallTool</b>
          <span className="g-pill" style={{ background: 'rgba(255,255,255,.12)', borderColor: 'rgba(255,255,255,.3)', color: '#D7EDE9' }}>
            {aanvraag.installateur.organisatie}
          </span>
        </div>
        <nav className="g-nav" style={{ borderBottomColor: 'rgba(255,255,255,.18)' }}>
          <a className="on" style={{ color: '#fff', borderBottomColor: '#fff' }}>
            Onderhoudsboekje
          </a>
          <a style={{ color: '#9BC5BE' }}>Werkbonnen</a>
          <a style={{ color: '#9BC5BE' }}>Planning</a>
        </nav>
      </div>
      <div className="g-body">
        {outcome === 'checking' && <Checking />}
        {blocked && <Blocked outcome={outcome} what="onderhoudsgegevens" />}
        {outcome === 'owner' && (
          <>
            <div className="g-note n-ok" style={{ marginBottom: '18px' }}>
              <span>✓</span>
              <div>
                <b style={{ fontWeight: 700 }}>Onderhoudshistorie opgehaald bij Installatiebedrijf De Wit.</b> Toegang
                geverifieerd door het Installatie Register; de gegevens zijn rechtstreeks via DICO uitgewisseld tussen
                beide softwarepakketten.
              </div>
            </div>
            <div style={{ display: 'flex', alignItems: 'flex-start', gap: '14px', marginBottom: '14px' }}>
              <div>
                <h1 className="g-h1" style={{ fontSize: '20px' }}>
                  Onderhoudshistorie — {g.adres}, {g.plaats}
                </h1>
                <p className="g-sub">
                  Bron: Installatiebedrijf De Wit · VBO-id <span className="mono">{g.vboId}</span> · toegang geldig
                  tot {formatDay(aanvraag.reikwijdte.tot)}
                </p>
              </div>
              <span className="g-chip c-ok" style={{ marginLeft: 'auto', marginTop: '4px' }}>
                Toegang geverifieerd
              </span>
            </div>
            <div className="g-card">
              <div className="g-cb" style={{ padding: '18px 8px 8px' }}>
                <table className="g-tbl">
                  <thead>
                    <tr>
                      <th>Datum</th>
                      <th>Installatie</th>
                      <th>Handeling</th>
                      <th>Uitgevoerd door</th>
                      <th>Resultaat</th>
                    </tr>
                  </thead>
                  <tbody>
                    {HISTORIE.map((row) => (
                      <tr key={row.datum}>
                        <td>{row.datum}</td>
                        <td className="mono">{row.installatie}</td>
                        <td>{row.handeling}</td>
                        <td>Installatiebedrijf De Wit</td>
                        <td>
                          <span className={`g-chip ${row.resultaat[0]}`}>{row.resultaat[1]}</span>
                        </td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              </div>
            </div>
            <p style={{ fontSize: '11.5px', color: 'var(--g-soft)', marginTop: '12px', lineHeight: 1.5 }}>
              Het Installatie Register legt alleen het toegangsrecht vast en verifieert het — de onderhoudsgegevens zelf
              lopen niet via IR. Voor DvTP is deze use case identiek aan Datastekker-toegang: dezelfde eigendomscheck bij
              de LVG.
            </p>
            <Actions onRefresh={check} />
          </>
        )}
      </div>
    </div>
  )
}
