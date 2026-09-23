// The installer's side of the GIR portal (design screens 00–04b): starting a
// request, the three wizard steps and the request's status. All of it is
// Keyper; nothing here touches GBO.
import { useEffect, useState } from 'react'
import { Link, useNavigate } from 'react-router'
import {
  BUILDINGS,
  clearAanvraag,
  formatStamp,
  loadAanvraag,
  newAanvraag,
  saveAanvraag,
  toepassing,
  updateAanvraag,
  type Aanvraag,
  type UseCase,
} from '../lib/aanvraag'
import { GirShell, InfoBar, Overzicht, Stepper } from '../components/gir'
import {
  ArrowLeft,
  ArrowRight,
  BookIcon,
  CheckIcon,
  ClockIcon,
  DocIcon,
  PersonIcon,
  PlugIcon,
  SaveIcon,
  SearchIcon,
  TrashIcon,
  WrenchIcon,
} from '../components/icons'

// ── 00 · rol + use case ───────────────────────────────────────────────────

export function NieuweAanvraag() {
  const navigate = useNavigate()
  const [useCase, setUseCase] = useState<UseCase>('datastekker')

  // The start screen starts over: an earlier request would otherwise keep its
  // building and consent, and the next approval would silently be about that
  // one.
  useEffect(clearAanvraag, [])

  const start = () => {
    saveAanvraag(newAanvraag(useCase))
    navigate('/aanvraag/installateur')
  }

  const option = (value: UseCase | null, icon: React.ReactNode, title: string, text: string) => {
    const on = value !== null && value === useCase
    return (
      <div
        className={`g-opt${on ? ' on' : ''}`}
        style={{ cursor: value ? 'pointer' : 'default', opacity: value ? undefined : 0.55 }}
        onClick={() => value && setUseCase(value)}
      >
        <span className="ic">{icon}</span>
        <div>
          <b>
            {title} {on && <span className="g-tag">Gekozen</span>}
          </b>
          <p>{text}</p>
        </div>
      </div>
    )
  }

  return (
    <GirShell active="Nieuwe aanvraag">
      <h1 className="g-h1">Nieuwe toestemmingsaanvraag</h1>
      <p className="g-sub">
        Kies uw rol en het proces. Zakelijke eigenaren keuren goed via eHerkenning, particuliere eigenaren via
        DigiD in MijnOverheid.
      </p>
      <div style={{ display: 'grid', gridTemplateColumns: '1fr 1fr', gap: '18px', marginTop: '20px' }}>
        <div className="g-card">
          <div className="g-cb">
            <div className="g-ct">1. Rol</div>
            <p className="g-cd" style={{ marginBottom: '14px' }}>
              In welke rol dient u deze aanvraag in?
            </p>
            <div className="g-opt on">
              <span className="ic">
                <WrenchIcon />
              </span>
              <div>
                <b>
                  Installatiebedrijf <span className="g-tag">Standaard</span>
                </b>
                <p>Vraag toestemming aan de installatie-eigenaar om bepaalde data van installaties in GIR te verwerken.</p>
              </div>
            </div>
            <div className="g-eyebrow">Andere rollen</div>
            <div style={{ display: 'grid', gridTemplateColumns: '1fr 1fr', gap: '10px' }}>
              <div className="g-opt off" style={{ margin: '0', display: 'block' }}>
                <b style={{ marginBottom: '5px' }}>
                  Datadienstgebruiker <span className="g-tag soon">Binnenkort</span>
                </b>
                <p>Vraag toestemming aan de installatie-eigenaar voor het gebruik van datadiensten.</p>
              </div>
              <div className="g-opt off" style={{ margin: '0', display: 'block' }}>
                <b style={{ marginBottom: '5px' }}>
                  Installatie-eigenaar <span className="g-tag soon">Binnenkort</span>
                </b>
                <p>Geef via GIR toestemming aan anderen om data van uw installaties te verwerken.</p>
              </div>
            </div>
          </div>
        </div>
        <div className="g-card">
          <div className="g-cb">
            <div className="g-ct">2. Use case</div>
            <p className="g-cd" style={{ marginBottom: '14px' }}>
              Welk proces wilt u starten?
            </p>
            {option(
              'onderhoudsboekje',
              <BookIcon />,
              'Digitaal Onderhoudsboekje',
              'Overdracht van onderhoudshistorie tussen installateurs met toestemming van de eigenaar.',
            )}
            {option(
              null,
              <DocIcon size={16} />,
              'GIR basisregistratie',
              'Vraag als registrar toestemming aan de gebouweigenaar om basisgegevens in GIR te registreren.',
            )}
            {option('datastekker', <PlugIcon />, 'Datastekker', 'Toegang tot datastekker-gegevens via gedelegeerde toestemming.')}
          </div>
        </div>
      </div>
      <div className="g-bar" style={{ justifyContent: 'flex-end' }}>
        <button className="g-btn g-p" onClick={start}>
          Doorgaan <ArrowRight />
        </button>
      </div>
    </GirShell>
  )
}

// ── Wizard frame shared by 01–03 ──────────────────────────────────────────

function useAanvraag(): Aanvraag | null {
  return loadAanvraag()
}

function WizardBar({ back, next, nextLabel = 'Volgende' }: { back: () => void; next: () => void; nextLabel?: string }) {
  return (
    <div className="g-bar">
      <button className="g-btn g-t" onClick={back}>
        <ArrowLeft /> Terug
      </button>
      <button className="g-btn g-o" style={{ marginLeft: 'auto' }}>
        <SaveIcon /> Opslaan en later afmaken
      </button>
      <button className="g-btn g-p" onClick={next}>
        {nextLabel} <ArrowRight />
      </button>
    </div>
  )
}

function NoRequest() {
  return (
    <GirShell active="Nieuwe aanvraag">
      <h1 className="g-h1">Geen aanvraag</h1>
      <p className="g-sub">
        Er is nog geen aanvraag gestart. <Link to="/">Start een nieuwe aanvraag.</Link>
      </p>
    </GirShell>
  )
}

function ReadOnlyField({ label, value, required, mono }: { label: string; value: string; required?: boolean; mono?: boolean }) {
  return (
    <div className="g-f">
      <label>
        {label} {required && <i>*</i>}
      </label>
      <input className={`g-in${mono ? ' mono ph' : ''}`} value={value} readOnly />
    </div>
  )
}

// ── 01 · installateur ─────────────────────────────────────────────────────

export function StapInstallateur() {
  const navigate = useNavigate()
  const aanvraag = useAanvraag()
  if (!aanvraag) return <NoRequest />
  const i = aanvraag.installateur
  return (
    <GirShell active="Nieuwe aanvraag">
      <Stepper current={1} />
      <InfoBar>Vertel ons wie de installateur is. Wij leiden het iSHARE-ID automatisch af uit het KvK-nummer.</InfoBar>
      <div className="g-cols">
        <div className="g-card">
          <div className="g-cb">
            <div className="g-ct">
              <PersonIcon />
              Installateur
            </div>
            <p className="g-cd" style={{ marginBottom: '16px' }}>
              Gegevens van de installateur-organisatie en contactpersoon.
            </p>
            <div className="g-grid2">
              <ReadOnlyField label="Naam" value={i.naam} required />
              <ReadOnlyField label="E-mail" value={i.email} required />
              <ReadOnlyField label="Organisatienaam" value={i.organisatie} required />
              <ReadOnlyField label="KvK-nummer (8 cijfers)" value={i.kvk} required />
            </div>
            <ReadOnlyField label="iSHARE-ID" value={`did:ishare:EU.NL.NTRNL-${i.kvk}`} mono />
            <div className="g-hr" />
            <div className="g-sec">Softwarepartij (optioneel)</div>
            <p className="g-sec-d">Vul in als een softwarepartij namens de installateur handelt.</p>
            <div className="g-grid2">
              <ReadOnlyField label="Organisatienaam" value="InstallTool" />
              <ReadOnlyField label="KvK-nummer (8 cijfers)" value="87654321" />
            </div>
            <ReadOnlyField label="iSHARE-ID" value="did:ishare:EU.NL.NTRNL-87654321" mono />
          </div>
        </div>
        <Overzicht aanvraag={aanvraag} step={1} />
      </div>
      <WizardBar back={() => navigate('/')} next={() => navigate('/aanvraag/gebouwen')} />
    </GirShell>
  )
}

// ── 02 · gebouwen ─────────────────────────────────────────────────────────

export function StapGebouwen() {
  const navigate = useNavigate()
  const aanvraag = useAanvraag()
  const [selected, setSelected] = useState(aanvraag?.gebouw.vboId ?? BUILDINGS[0].vboId)
  if (!aanvraag) return <NoRequest />
  const gebouw = BUILDINGS.find((b) => b.vboId === selected) ?? BUILDINGS[0]

  const next = () => {
    updateAanvraag({ gebouw })
    navigate('/aanvraag/reikwijdte')
  }
  return (
    <GirShell active="Nieuwe aanvraag">
      <Stepper current={2} />
      <div className="g-cols">
        <div className="g-card">
          <div className="g-cb">
            <div className="g-ct">Gebouw</div>
            <p className="g-cd" style={{ marginBottom: '16px' }}>
              Kies het gebouw via adres-zoeken of directe VBO-id invoer. Een aanvraag gaat over één gebouw.
            </p>
            <div className="g-tog" style={{ marginBottom: '16px' }}>
              <span className="on">Adres zoeken</span>
              <span>VBO-id invoeren</span>
            </div>
            <div style={{ display: 'grid', gridTemplateColumns: 'repeat(4,1fr)', gap: '14px' }}>
              <ReadOnlyField label="Postcode" value="3448 XR" />
              <ReadOnlyField label="Huisnummer" value="12" />
              <div className="g-f">
                <label>Huisletter</label>
                <input className="g-in ph" value="A" readOnly />
              </div>
              <div className="g-f">
                <label>Toevoeging</label>
                <input className="g-in ph" value="bis" readOnly />
              </div>
            </div>
            <div style={{ display: 'flex', justifyContent: 'flex-end', margin: '2px 0 16px' }}>
              <button className="g-btn g-p" style={{ background: '#1E40AF' }}>
                <SearchIcon /> Zoek VBO-id
              </button>
            </div>
            {BUILDINGS.map((b) => (
              <div className="g-row" key={b.vboId}>
                <div style={{ flex: '1' }}>
                  <b>
                    {b.adres}, {b.postcode.replace(' ', '')} {b.plaats}
                  </b>
                  <small>{b.vboId} · Verblijfsobject (BAG)</small>
                </div>
                <button className="g-btn g-o" onClick={() => setSelected(b.vboId)} disabled={b.vboId === selected}>
                  {b.vboId === selected ? '✓ Gekozen' : 'Kiezen'}
                </button>
              </div>
            ))}
            <p style={{ fontSize: '11.5px', color: 'var(--g-mute)', margin: '12px 0 16px' }}>
              ⓘ Adresgegevens via BAG (PDOK Locatieserver).
            </p>
            <div style={{ fontSize: '12.5px', fontWeight: '700', marginBottom: '9px' }}>Gekozen gebouw</div>
            <div style={{ border: '1px solid var(--g-bd)', borderRadius: '10px', overflow: 'hidden' }}>
              <table className="g-tbl">
                <thead>
                  <tr>
                    <th>Adres</th>
                    <th>VBO-id</th>
                    <th>Plaats</th>
                    <th />
                  </tr>
                </thead>
                <tbody>
                  <tr>
                    <td>
                      {gebouw.adres}, {gebouw.postcode.replace(' ', '')} {gebouw.plaats}
                    </td>
                    <td className="mono" style={{ fontSize: '12px' }}>
                      {gebouw.vboId}
                    </td>
                    <td>{gebouw.plaats}</td>
                    <td style={{ textAlign: 'right', color: 'var(--g-soft)' }}>
                      <TrashIcon />
                    </td>
                  </tr>
                </tbody>
              </table>
            </div>
          </div>
        </div>
        <Overzicht aanvraag={{ ...aanvraag, gebouw }} step={2} />
      </div>
      <WizardBar back={() => navigate('/aanvraag/installateur')} next={next} />
    </GirShell>
  )
}

// ── 03 · reikwijdte ───────────────────────────────────────────────────────

export function StapReikwijdte() {
  const navigate = useNavigate()
  const aanvraag = useAanvraag()
  if (!aanvraag) return <NoRequest />
  const r = aanvraag.reikwijdte

  // The wizard's steps 4 (Eigenaar) and 5 (Controle) are prefilled in the
  // demo: sending the request invites J. de Vries by e-mail.
  const send = () => {
    updateAanvraag({ verstuurd: new Date().toISOString() })
    navigate('/aanvraag')
  }
  return (
    <GirShell active="Nieuwe aanvraag">
      <h1 className="g-h1">Aanvraag {toepassing(aanvraag.useCase)}</h1>
      <p className="g-sub">
        Aangevraagd als installateur. Na verzending ontvangt de gebouweigenaar een goedkeuringslink per e-mail om
        toegang te verlenen.
      </p>
      <div style={{ marginTop: '18px' }}>
        <Stepper current={3} />
      </div>
      <InfoBar>Voor welke installaties geldt de toegang en hoe lang?</InfoBar>
      <div className="g-cols">
        <div className="g-card">
          <div className="g-cb">
            <div className="g-ct">Reikwijdte</div>
            <p className="g-cd" style={{ marginBottom: '16px' }}>
              Beperk de aanvraag tot een installatiesegment en geldigheidsperiode.
            </p>
            <ReadOnlyField label="NL/SfB filter" value={r.segment} />
            <div className="g-grid2">
              <ReadOnlyField label="Validiteit startdatum" value={r.van} required />
              <ReadOnlyField label="Validiteit einddatum" value={r.tot} required />
            </div>
            <div className="g-f">
              <label>Data-element set</label>
              <input className="g-in ph" value="Toegang verlenen tot subsets wordt later geïmplementeerd" readOnly />
            </div>
          </div>
        </div>
        <Overzicht aanvraag={aanvraag} step={3} />
      </div>
      <WizardBar back={() => navigate('/aanvraag/gebouwen')} next={send} nextLabel="Verzenden voor goedkeuring" />
    </GirShell>
  )
}

// ── 04 / 04b · aanvraagdetail ─────────────────────────────────────────────

type Step = { label: string; at?: string; state: 'ok' | 'now' | 'pend'; detail?: React.ReactNode }

function Timeline({ steps }: { steps: Step[] }) {
  return (
    <ul className="g-tl">
      {steps.map((s) => (
        <li key={s.label}>
          <span className={`d ${s.state === 'pend' ? '' : s.state}`}>
            {s.state === 'ok' ? '✓' : s.state === 'now' ? <ClockIcon /> : null}
          </span>
          {s.state === 'pend' ? (
            <div className="g-tl pend" style={{ display: 'block' }}>
              <b>{s.label}</b>
            </div>
          ) : (
            <div style={{ flex: 1 }}>
              <b>{s.label}</b>
              <span>{s.at}</span>
              {s.detail}
            </div>
          )}
        </li>
      ))}
    </ul>
  )
}

export function AanvraagDetail() {
  const aanvraag = useAanvraag()
  if (!aanvraag?.verstuurd) return <NoRequest />
  const approved = Boolean(aanvraag.goedgekeurd)
  const rejected = Boolean(aanvraag.afgewezen)
  const fetchLabel = aanvraag.useCase === 'onderhoudsboekje' ? 'Onderhoudshistorie ophalen' : 'Installatiegegevens ophalen'
  const fetchTo = aanvraag.useCase === 'onderhoudsboekje' ? '/onderhoudshistorie' : '/installatiegegevens'

  const steps: Step[] = approved
    ? [
        { label: 'Concept aangemaakt', at: formatStamp(aanvraag.aangemaakt), state: 'ok' },
        { label: 'Naar Keyper gestuurd', at: formatStamp(aanvraag.verstuurd), state: 'ok' },
        { label: 'Goedgekeurd', at: formatStamp(aanvraag.goedgekeurd), state: 'ok' },
        { label: 'Eigenaarschap vastgesteld bij LVG', at: formatStamp(aanvraag.eigendomBevestigd), state: 'ok' },
        { label: 'Toestemming geregistreerd in GIR', at: formatStamp(aanvraag.goedgekeurd), state: 'ok' },
      ]
    : [
        { label: 'Concept aangemaakt', at: formatStamp(aanvraag.aangemaakt), state: 'ok' },
        { label: 'Naar Keyper gestuurd', at: formatStamp(aanvraag.verstuurd), state: 'ok' },
        {
          label: rejected ? 'Afgewezen' : 'Wacht op goedkeuring',
          at: rejected ? formatStamp(aanvraag.afgewezen) : 'eigenaar is per e-mail uitgenodigd',
          state: 'now',
          detail: !rejected && (
            <Link
              to="/mail"
              className="g-btn g-p"
              style={{ marginTop: '9px', textDecoration: 'none', padding: '8px 14px', fontSize: '12.5px' }}
            >
              Open goedkeuringsverzoek <ArrowRight size={14} />
            </Link>
          ),
        },
        { label: 'Goedgekeurd / Afgewezen', state: 'pend' },
        { label: 'Toestemming geregistreerd in GIR', state: 'pend' },
      ]

  const status = approved ? 'Goedgekeurd' : rejected ? 'Afgewezen' : 'Wacht op goedkeuring'
  const chip = approved ? 'c-ok' : rejected ? 'c-bad' : 'c-wait'
  return (
    <GirShell active="Vorige aanvragen">
      <div style={{ display: 'flex', alignItems: 'flex-start', gap: '14px' }}>
        <div>
          <h1 className="g-h1">Aanvraag {aanvraag.id}</h1>
          <p className="g-sub">
            {approved
              ? `Goedgekeurd door de eigenaar op ${formatStamp(aanvraag.goedgekeurd)} — toegang is actief.`
              : `${status} door ${aanvraag.eigenaar.naam} — verstuurd op ${formatStamp(aanvraag.verstuurd)}.`}
          </p>
        </div>
        <span className={`g-chip ${chip}`} style={{ marginLeft: 'auto', marginTop: '4px' }}>
          {status}
        </span>
      </div>
      <div className="g-cols" style={{ marginTop: '18px' }}>
        <div>
          <div className="g-card" style={{ marginBottom: '16px' }}>
            <div className="g-cb">
              <div className="g-ct" style={{ marginBottom: '12px' }}>
                Tijdlijn
              </div>
              <Timeline steps={steps} />
              {approved && (
                <Link to={fetchTo} className="g-btn g-p" style={{ marginTop: '14px', textDecoration: 'none' }}>
                  {fetchLabel} <ArrowRight />
                </Link>
              )}
            </div>
          </div>
          <div className="g-card">
            <div className="g-cb">
              <div className="g-ct" style={{ marginBottom: '14px' }}>
                Installatiebedrijf
              </div>
              <dl className="g-dl">
                <dt>Naam</dt>
                <dd>{aanvraag.installateur.naam}</dd>
                <dt>E-mail</dt>
                <dd>{aanvraag.installateur.email}</dd>
                <dt>Organisatie</dt>
                <dd>{aanvraag.installateur.organisatie}</dd>
                <dt>KvK</dt>
                <dd>{aanvraag.installateur.kvk}</dd>
                <dt>iSHARE-ID</dt>
                <dd className="mono" style={{ fontSize: '12px' }}>
                  did:ishare:EU.NL.NTRNL-{aanvraag.installateur.kvk}
                </dd>
              </dl>
            </div>
          </div>
        </div>
        <div className="g-card g-side">
          <div className="g-cb">
            <b>Aanvraag-overzicht</b>
            <dl>
              <div className="g-kv">
                <dt>Toepassing</dt>
                <dd>{toepassing(aanvraag.useCase)}</dd>
              </div>
              <div className="g-kv">
                <dt>Gebouw</dt>
                <dd style={{ fontSize: '11.5px' }}>
                  {aanvraag.gebouw.adres}, {aanvraag.gebouw.plaats}
                </dd>
              </div>
              <div className="g-kv">
                <dt>VBO-id</dt>
                <dd className="mono" style={{ fontSize: '11px' }}>
                  {aanvraag.gebouw.vboId}
                </dd>
              </div>
              <div className="g-kv">
                <dt>Eigenaar</dt>
                <dd>{aanvraag.eigenaar.naam}</dd>
              </div>
              <div className="g-kv">
                <dt>Reikwijdte</dt>
                <dd>{aanvraag.reikwijdte.segment}</dd>
              </div>
              <div className="g-kv">
                <dt>Validiteit</dt>
                <dd style={{ fontSize: '11.5px' }}>
                  {aanvraag.reikwijdte.van} → {aanvraag.reikwijdte.tot}
                </dd>
              </div>
              <div className="g-kv">
                <dt>Status</dt>
                <dd>
                  <span className={`g-chip ${chip}`}>
                    {approved && <CheckIcon size={11} />}
                    {status}
                  </span>
                </dd>
              </div>
            </dl>
          </div>
        </div>
      </div>
    </GirShell>
  )
}
