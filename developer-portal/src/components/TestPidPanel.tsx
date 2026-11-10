import { useEffect, useState } from 'react'
import { QRCodeSVG } from 'qrcode.react'
import { createTestPidOffer, loadTestPersons } from '../api/devClient'
import { personLabel, type TestPersons, type TestPidOffer } from '../lib/testPid'

// A test PID for the EUDI reference wallet, which has no PID the demo
// trusts. EU-wallet only: the NL Wallet takes its PID from its own
// pre-production issuer. Hidden when the backend has no test PID routes.
export default function TestPidPanel() {
  const [persons, setPersons] = useState<TestPersons>({ enabled: false })
  const [bsn, setBsn] = useState('')
  const [busy, setBusy] = useState(false)
  const [offer, setOffer] = useState<TestPidOffer | null>(null)

  useEffect(() => {
    let active = true
    void loadTestPersons().then((loaded) => {
      if (!active) return
      setPersons(loaded)
      if (loaded.enabled && loaded.persons.length > 0) setBsn(loaded.persons[0].bsn)
    })
    return () => {
      active = false
    }
  }, [])

  if (!persons.enabled) return null

  const makeOffer = async () => {
    setBusy(true)
    setOffer(null)
    setOffer(await createTestPidOffer(bsn))
    setBusy(false)
  }

  return (
    <div className="panel">
      <div className="panel-h">
        <span className="t">Test-PID · EU-wallet</span>
      </div>
      <div className="panel-b" style={{ display: 'flex', flexDirection: 'column', gap: 12 }}>
        <div className="hint" style={{ fontSize: 12, color: 'var(--mute)' }}>
          Zet een test-PID van een BRP-mockpersoon in de GBO-testbuild van de EUDI-referentiewallet,
          zodat je daarna een product kunt ophalen of kunt inloggen. Niet voor de NL Wallet: die
          haalt zijn PID bij de eigen pre-productie-issuer.
        </div>
        {persons.error ? (
          <div style={{ color: 'var(--danger, #d33)', fontSize: 12 }}>{persons.error}</div>
        ) : (
          <div style={{ display: 'flex', gap: 8, alignItems: 'center', flexWrap: 'wrap' }}>
            <label htmlFor="test-pid-person" style={{ fontSize: 12 }}>Testpersoon</label>
            <select
              id="test-pid-person"
              className="sel mono"
              value={bsn}
              onChange={(e) => {
                setBsn(e.target.value)
                setOffer(null)
              }}
            >
              {persons.persons.map((person) => (
                <option key={person.bsn} value={person.bsn}>{personLabel(person)}</option>
              ))}
            </select>
            <button className="btn" onClick={makeOffer} disabled={busy || !bsn}>
              {busy ? 'bezig…' : offer && 'link' in offer ? 'Nieuwe QR' : 'Maak QR'}
            </button>
          </div>
        )}
        <div aria-live="polite">
          {offer && 'error' in offer && (
            <div style={{ color: 'var(--danger, #d33)', fontSize: 12 }}>{offer.error}</div>
          )}
          {offer && 'link' in offer && (
            <div style={{ display: 'flex', flexDirection: 'column', gap: 10, alignItems: 'center' }}>
              <QRCodeSVG value={offer.link} size={220} bgColor="#ffffff" fgColor="#000000" includeMargin />
              <div style={{ fontSize: 12, color: 'var(--mute)', textAlign: 'center' }}>
                Scan met de EUDI-referentiewallet. De QR is 5 minuten geldig en eenmalig te gebruiken.
              </div>
              <a className="btn mini" href={offer.link}>same-device link</a>
            </div>
          )}
        </div>
      </div>
    </div>
  )
}
