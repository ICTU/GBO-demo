import { useEffect, useState } from 'react'
import MijnOverheidLayout from '../components/MijnOverheidLayout'
import { AUTO_REDIRECT_MS, type Confirmation } from '../consentStart/startFlow'

const TEXT: Record<Confirmation['outcome'], { title: string; body: (name: string) => string }> = {
  granted: {
    title: 'Toestemming geregistreerd',
    body: (name) => `U gaf toestemming aan ${name}. U kunt deze later intrekken onder Toestemmingen.`,
  },
  denied: {
    title: 'Toestemming geweigerd',
    body: (name) => `U gaf ${name} geen toestemming. Er zijn geen gegevens gedeeld.`,
  },
  failed: {
    title: 'Toestemming niet geregistreerd',
    body: (name) => `Er ging iets mis bij het vastleggen van uw toestemming. ${name} krijgt dit te horen.`,
  },
}

type Props = { confirmation: Confirmation; onReturn: () => void }

// A short confirmation of the backend's answer, then the return URL from that
// answer. Nothing here is stored: a reload starts over at the start route.
export default function ToestemmingBevestiging({ confirmation, onReturn }: Props) {
  const [secondsLeft, setSecondsLeft] = useState(Math.ceil(AUTO_REDIRECT_MS / 1000))
  const name = confirmation.clientName || 'de dienstverlener'
  const text = TEXT[confirmation.outcome]

  useEffect(() => {
    const tick = setInterval(() => setSecondsLeft((s) => (s > 0 ? s - 1 : 0)), 1000)
    const timer = setTimeout(onReturn, AUTO_REDIRECT_MS)
    return () => {
      clearInterval(tick)
      clearTimeout(timer)
    }
  }, [onReturn])

  return (
    <MijnOverheidLayout
      activeNav="toestemmingen"
      breadcrumb={[{ label: 'Home' }, { label: 'Toestemmingen' }, { label: 'Bevestiging' }]}
    >
      <h1 className="page-title">{text.title}</h1>
      <p>{text.body(name)}</p>
      <p>
        U gaat over <strong>{secondsLeft}</strong> seconden automatisch terug naar {name}.
      </p>
      <div className="actions">
        <button className="btn btn-primary" onClick={onReturn}>
          Direct terug naar {name} →
        </button>
      </div>
    </MijnOverheidLayout>
  )
}
