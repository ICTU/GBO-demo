// The owner's side (design screens 05, 06, 10/16, 11). The owner first
// consents in MijnOverheid that the Installatie Register may ask LVG whether
// the building is theirs, and comes straight back to IR's own consent for the
// installer. Only once both are given does IR ask LVG; it registers the
// installer's access only when LVG confirms the ownership.
import { useEffect, useRef, useState } from 'react'
import { Link, useLocation, useNavigate } from 'react-router'
import { formatDay, loadAanvraag, updateAanvraag, type Aanvraag } from '../lib/aanvraag'
import { checkOwnership, type Outcome } from '../lib/ownership'
import { consentRequestUrl, readPortalReturn } from '../lib/portal'
import { ApprovalShell } from '../components/gir'
import { ArrowRight, CheckIcon, HouseMark, InfoIcon, ShieldIcon } from '../components/icons'

const returnUrl = () => `${window.location.origin}/verzoek/terug`

function NoRequest() {
  return (
    <ApprovalShell>
      <div className="g-card">
        <div className="g-cb" style={{ textAlign: 'center' }}>
          <h2 style={{ fontSize: '19px', fontWeight: 700, marginBottom: '8px' }}>Geen openstaand verzoek</h2>
          <p className="g-sub">
            Er is geen goedkeuringsverzoek gevonden. <Link to="/">Start de demo opnieuw.</Link>
          </p>
        </div>
      </div>
    </ApprovalShell>
  )
}

function WhyNote() {
  return (
    <div className="g-note n-amb" style={{ marginBottom: '16px' }}>
      <InfoIcon />
      <span style={{ flex: '1' }}>Waarom krijg ik dit verzoek?</span>
      <span style={{ color: 'var(--g-soft)' }}>›</span>
    </div>
  )
}

function dataText(aanvraag: Aanvraag): string {
  return aanvraag.useCase === 'onderhoudsboekje'
    ? `${aanvraag.installateur.organisatie} vraagt uw toestemming om de onderhoudshistorie van uw installaties in te zien.`
    : `${aanvraag.installateur.organisatie} vraagt uw toestemming om uw gebouwinstallatiedata in te zien via Datastekker.`
}

// ── 05 · e-mail ───────────────────────────────────────────────────────────

export function Mail() {
  const navigate = useNavigate()
  const aanvraag = loadAanvraag()
  if (!aanvraag) return <NoRequest />
  const { installateur: i, gebouw: g, eigenaar: e } = aanvraag
  return (
    <div className="fr">
      <div className="mail">
        <div className="mail-c">
          <div className="mail-hd">
            <span className="g-mark" style={{ width: '36px', height: '36px', borderRadius: '9px' }}>
              <HouseMark />
            </span>
            <div className="mail-meta">
              <b>Goedkeuring nodig: installatiegegevens {g.adres}</b>GIR Toestemmingsportaal &lt;noreply@keyper.nl&gt; ·
              aan {e.email}
            </div>
          </div>
          <div className="mail-b">
            <h2>{i.organisatie} vraagt toegang</h2>
            <p>Beste heer/mevrouw De Vries,</p>
            <p>
              <b>{i.organisatie}</b> vraagt toestemming om{' '}
              {aanvraag.useCase === 'onderhoudsboekje'
                ? 'de onderhoudshistorie in te zien'
                : 'via Datastekker de installatiegegevens in te zien'}{' '}
              van{' '}
              <b>
                {g.adres}, {g.plaats}
              </b>
              . Als eigenaar van dit pand bepaalt u of u die toestemming geeft.
            </p>
            <p>
              Bij de volgende stap controleren we eerst via MijnOverheid of dit pand op uw naam staat. Uw
              burgerservicenummer wordt daarbij niet gedeeld met {i.organisatie}.
            </p>
            <div style={{ margin: '20px 0 6px' }}>
              <button className="g-btn g-p" style={{ padding: '12px 22px', fontSize: '14.5px' }} onClick={() => navigate('/verzoek')}>
                Open goedkeuringsverzoek
              </button>
            </div>
            <p style={{ fontSize: '12.5px', color: 'var(--g-soft)', marginTop: '14px' }}>
              Aanvraag {aanvraag.id} · vervalt automatisch na 14 dagen. Herkent u deze aanvraag niet? Dan hoeft u
              niets te doen.
            </p>
          </div>
          <div className="mail-ft">
            GIR Toestemmingsportaal · Postbus 1234, Den Haag
            <br />U ontvangt deze e-mail omdat een installateur een aanvraag heeft gedaan voor een pand waarvan u als
            eigenaar bent opgegeven.
          </div>
        </div>
      </div>
    </div>
  )
}

// ── 06 · verzoek bekijken: eerst eigendom bevestigen ─────────────────────

export function Verzoek() {
  const navigate = useNavigate()
  const aanvraag = loadAanvraag()
  if (!aanvraag) return <NoRequest />
  const { installateur: i, gebouw: g } = aanvraag

  const reject = () => {
    updateAanvraag({ afgewezen: new Date().toISOString() })
    navigate('/verzoek/afgewezen')
  }
  return (
    <ApprovalShell>
      <WhyNote />
      <div className="g-card">
        <div className="g-cb" style={{ padding: '26px' }}>
          <h2 style={{ fontSize: '19px', fontWeight: 700, lineHeight: 1.4, textAlign: 'center', marginBottom: '8px' }}>
            {dataText(aanvraag)}
          </h2>
          <p style={{ fontSize: '13.5px', color: 'var(--g-mute)', textAlign: 'center', lineHeight: 1.6, marginBottom: '18px' }}>
            Om te kunnen goedkeuren geeft u eerst via MijnOverheid toestemming om te laten controleren of u eigenaar
            bent van dit pand.
          </p>
          <div className="g-note n-blue" style={{ marginBottom: '18px' }}>
            <ShieldIcon />
            <div>
              <b style={{ fontWeight: 700 }}>Eigendomscheck via MijnOverheid</b>
              <br />U geeft toestemming om bij de Landelijke Voorziening Gebouwen (LVG) te laten controleren of {g.adres}{' '}
              op uw naam staat. {i.organisatie} ziet alleen de uitkomst.
            </div>
          </div>
          <dl className="g-dl" style={{ marginBottom: '20px' }}>
            <dt>Installateur</dt>
            <dd>{i.organisatie}</dd>
            <dt>Pand</dt>
            <dd>
              {g.adres}, {g.postcode} {g.plaats}
            </dd>
            <dt>VBO-id</dt>
            <dd className="mono" style={{ fontSize: '12px' }}>
              {g.vboId}
            </dd>
            <dt>Aanvraag</dt>
            <dd className="mono" style={{ fontSize: '12px' }}>
              {aanvraag.id}
            </dd>
          </dl>
          <button
            className="digid-btn"
            style={{ width: '100%', justifyContent: 'center' }}
            onClick={() => window.location.assign(consentRequestUrl(returnUrl()))}
          >
            <img src="/Logo_of_DigiD.png" alt="" />
            <span>Bevestig eigendom via MijnOverheid</span>
          </button>
          <div style={{ textAlign: 'center', marginTop: '13px' }}>
            <button
              onClick={reject}
              style={{
                background: 'none',
                fontSize: '13px',
                fontWeight: 600,
                color: 'var(--g-link)',
                textDecoration: 'underline',
                textUnderlineOffset: '3px',
              }}
            >
              Ik ben geen eigenaar van dit pand
            </button>
          </div>
        </div>
      </div>
    </ApprovalShell>
  )
}

// ── terug uit MijnOverheid ────────────────────────────────────────────────
// IR keeps the consent token and moves straight on to its own consent (10):
// the ownership check waits until both consents are given.

export function PortalTerug() {
  const navigate = useNavigate()
  const location = useLocation()
  const [failed, setFailed] = useState<'denied' | 'invalid' | null>(null)
  const aanvraag = loadAanvraag()
  const started = useRef(false)

  useEffect(() => {
    if (started.current || !aanvraag) return
    started.current = true
    const back = readPortalReturn(location.search, location.hash)
    if (back.status !== 'ok') {
      window.history.replaceState(null, '', location.pathname)
      setFailed(back.status)
      return
    }
    // The token is IR's to keep, not the address bar's.
    updateAanvraag({ consentToken: back.consentToken })
    navigate('/verzoek/goedkeuren', { replace: true })
  }, [aanvraag, location, navigate])

  if (!aanvraag) return <NoRequest />
  if (!failed) return null
  return (
    <ApprovalShell>
      <OutcomeCard tone="wait" chip="Geen toestemming" border="var(--g-amber-bd)">
        {failed === 'denied'
          ? 'U heeft in MijnOverheid geen toestemming gegeven om uw eigendom te laten controleren.'
          : 'Er is geen geldige toestemming uit MijnOverheid ontvangen.'}{' '}
        Zonder die controle kan het verzoek niet worden goedgekeurd.
        <button
          className="g-btn g-o"
          style={{ width: '100%', marginTop: '16px' }}
          onClick={() => window.location.assign(consentRequestUrl(returnUrl()))}
        >
          Opnieuw via MijnOverheid
        </button>
      </OutcomeCard>
    </ApprovalShell>
  )
}

function OutcomeCard({
  tone,
  chip,
  border,
  children,
}: {
  tone: 'ok' | 'bad' | 'wait'
  chip: string
  border: string
  children: React.ReactNode
}) {
  return (
    <div className="g-card" style={{ borderColor: border }}>
      <div className="g-cb">
        <span className={`g-chip c-${tone}`}>{chip}</span>
        <div style={{ fontSize: '13.5px', lineHeight: '1.6', marginTop: '12px' }}>{children}</div>
      </div>
    </div>
  )
}

// ── 10 / 16 · goedkeuringsscherm eigenaar (IR-toestemming, buiten GBO) ────

export function Goedkeuren() {
  const navigate = useNavigate()
  const aanvraag = loadAanvraag()
  const [check, setCheck] = useState<'idle' | 'checking' | Exclude<Outcome, 'owner'>>('idle')
  if (!aanvraag?.consentToken) return <NoRequest />
  const { installateur: i, gebouw: g } = aanvraag
  const onderhoud = aanvraag.useCase === 'onderhoudsboekje'

  // Both consents are in: now IR asks LVG, and registers the installer's
  // access only when the ownership is confirmed.
  const approve = async () => {
    setCheck('checking')
    const result = await checkOwnership(aanvraag.consentToken!, g.vboId)
    if (result.outcome !== 'owner') {
      setCheck(result.outcome)
      return
    }
    const now = new Date().toISOString()
    updateAanvraag({ goedgekeurd: now, eigendomBevestigd: now })
    navigate('/verzoek/geactiveerd')
  }

  if (check === 'not-owner' || check === 'unconfirmed') {
    return (
      <ApprovalShell>
        {check === 'not-owner' ? (
          <OutcomeCard tone="bad" chip="Geen eigenaarschap vastgesteld" border="#FCC7C2">
            Volgens de LVG staat <b>{g.adres}</b> niet op uw naam. Er worden geen gegevens gedeeld en het verzoek van{' '}
            {i.organisatie} is niet goedgekeurd.
            <Link to="/" className="g-btn g-o" style={{ width: '100%', marginTop: '16px', textDecoration: 'none' }}>
              Nieuwe aanvraag starten
            </Link>
          </OutcomeCard>
        ) : (
          <OutcomeCard tone="wait" chip="Geen toestemming" border="var(--g-amber-bd)">
            Het eigendom kon niet worden gecontroleerd, omdat er geen geldige toestemming is. Er worden geen gegevens
            gedeeld.
            <button
              className="g-btn g-o"
              style={{ width: '100%', marginTop: '16px' }}
              onClick={() => window.location.assign(consentRequestUrl(returnUrl()))}
            >
              Opnieuw via MijnOverheid
            </button>
          </OutcomeCard>
        )}
      </ApprovalShell>
    )
  }
  const reject = () => {
    updateAanvraag({ afgewezen: new Date().toISOString() })
    navigate('/verzoek/afgewezen')
  }
  return (
    <ApprovalShell>
      <WhyNote />
      <div className="g-card">
        <div className="g-cb" style={{ padding: '28px' }}>
          <div style={{ display: 'flex', justifyContent: 'center', gap: '14px', marginBottom: '18px' }}>
            <span style={{ width: '38px', height: '38px', borderRadius: '10px', background: '#EEF4FF', display: 'grid', placeItems: 'center', color: 'var(--g-blue)' }}>
              <ShieldIcon />
            </span>
            <span style={{ width: '1px', background: 'var(--g-bd)' }} />
            <span style={{ width: '38px', height: '38px', borderRadius: '10px', background: '#ECFDF3', display: 'grid', placeItems: 'center', color: 'var(--g-green-tx)' }}>
              <CheckIcon size={18} />
            </span>
          </div>
          <h2 style={{ fontSize: '19.5px', fontWeight: 700, lineHeight: 1.4, textAlign: 'center', marginBottom: '10px' }}>
            {dataText(aanvraag)}
          </h2>
          <p style={{ fontSize: '13.5px', color: 'var(--g-mute)', lineHeight: 1.6, marginBottom: '14px' }}>
            {onderhoud
              ? 'De onderhoudshistorie is eerder vastgelegd door een andere installateur. Met uw toestemming wordt die overgedragen:'
              : 'De toegang tot de gebouwinstallatiedata via Datastekker betreft de volgende installaties:'}
          </p>
          <div style={{ background: '#F8FAFC', borderRadius: '9px', padding: '16px 18px', marginBottom: '16px' }}>
            <ul style={{ listStyle: 'disc', paddingLeft: '18px', fontSize: '13.5px', lineHeight: 1.6, marginBottom: '11px' }}>
              {onderhoud ? (
                <li>
                  Leestoegang tot onderhoudshistorie van <b>Installatiebedrijf De Wit</b>, voor object{' '}
                  <span className="mono" style={{ fontSize: '12.5px' }}>
                    {g.vboId}
                  </span>
                  <br />
                  <span style={{ color: 'var(--g-mute)' }}>Keuringen, storingen en vervangen onderdelen — geen persoonsgegevens</span>
                </li>
              ) : (
                <li>
                  Leestoegang tot GIRDatastekkerAccessdata, voor object{' '}
                  <span className="mono" style={{ fontSize: '12.5px' }}>
                    {g.vboId}
                  </span>{' '}
                  met attributen: <span className="mono">*</span>
                </li>
              )}
            </ul>
            <span className="g-chip c-read">read</span> <span className="g-chip c-val">1 jaar geldig</span>
          </div>
          <div className="g-acc">
            Wat gebeurt er niet?<span className="ch">›</span>
          </div>
          <div className="g-acc">
            Hoe kan ik gegeven toestemmingen beheren?<span className="ch">›</span>
          </div>
          <div style={{ display: 'grid', gridTemplateColumns: '1fr 1fr', gap: '14px', marginTop: '20px' }}>
            <button className="g-btn g-ok" style={{ padding: '12px' }} onClick={approve} disabled={check === 'checking'}>
              {check === 'checking' ? 'Eigendom controleren bij de LVG…' : 'Goedkeuren'}
            </button>
            <button className="g-btn g-no" style={{ padding: '12px' }} onClick={reject} disabled={check === 'checking'}>
              Afwijzen
            </button>
          </div>
          <p style={{ fontSize: '11.5px', color: 'var(--g-soft)', textAlign: 'center', marginTop: '12px' }}>
            Deze toestemming aan {i.organisatie} wordt vastgelegd in het Installatie Register.
          </p>
        </div>
      </div>
    </ApprovalShell>
  )
}

// ── 11 · toestemming geactiveerd (en afgewezen) ───────────────────────────

export function Geactiveerd({ rejected = false }: { rejected?: boolean }) {
  const aanvraag = loadAanvraag()
  if (!aanvraag) return <NoRequest />
  const i = aanvraag.installateur
  return (
    <ApprovalShell>
      <div className="g-card">
        <div className="g-cb" style={{ padding: '34px 30px', textAlign: 'center' }}>
          <div
            style={{
              width: '56px',
              height: '56px',
              borderRadius: '50%',
              background: rejected ? '#FEF3F2' : '#ECFDF3',
              color: rejected ? 'var(--g-red-tx)' : 'var(--g-green-tx)',
              display: 'grid',
              placeItems: 'center',
              margin: '0 auto 16px',
            }}
          >
            {rejected ? '✕' : <CheckIcon size={26} />}
          </div>
          <h2 style={{ fontSize: '20px', fontWeight: 700, marginBottom: '10px' }}>
            {rejected ? 'Verzoek afgewezen' : 'Toestemming geactiveerd'}
          </h2>
          {!rejected && (
            <div className="g-note n-ok" style={{ margin: '0 auto 14px', justifyContent: 'center', maxWidth: '440px' }}>
              <span>✓</span>
              <div>Eigenaarschap vastgesteld: de LVG bevestigt dat u eigenaar bent van {aanvraag.gebouw.adres}.</div>
            </div>
          )}
          <p style={{ fontSize: '13.5px', color: 'var(--g-mute)', lineHeight: 1.65, maxWidth: '440px', margin: '0 auto 10px' }}>
            {rejected
              ? `${i.organisatie} krijgt geen toegang tot de gegevens van dit pand.`
              : `De gegeven toestemming is geregistreerd. ${i.organisatie} heeft nu toegang tot uw ${
                  aanvraag.useCase === 'onderhoudsboekje' ? 'onderhoudshistorie' : 'gebouwinstallatiedata'
                }, tot ${formatDay(aanvraag.reikwijdte.tot)}.`}
          </p>
          {!rejected && (
            <p style={{ fontSize: '13.5px', color: 'var(--g-mute)', lineHeight: 1.65 }}>
              U kunt uw toestemmingen beheren via de beheerlink in het GIR Toestemmingsportaal. U kunt deze pagina nu
              sluiten.
            </p>
          )}
          <Link to="/aanvraag" className="g-btn g-p" style={{ marginTop: '20px', textDecoration: 'none' }}>
            Terug naar installateur <ArrowRight />
          </Link>
        </div>
      </div>
    </ApprovalShell>
  )
}
