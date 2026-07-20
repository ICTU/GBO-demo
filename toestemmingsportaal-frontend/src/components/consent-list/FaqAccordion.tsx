import { useState } from 'react'

type Item = { q: string; a: string }

const ITEMS: Item[] = [
  {
    q: 'Wat is een toestemming?',
    a: 'Een toestemming geeft een organisatie het recht om bij een overheidsbron gegevens over u op te halen, zonder dat u die zelf hoeft op te sturen. U geeft per onderwerp aan welke gegevens gedeeld mogen worden en hoe lang.',
  },
  {
    q: 'Hoe trek ik een toestemming in?',
    a: 'Klik op de toestemming in het overzicht en kies "Toestemming intrekken". De organisatie kan vanaf dat moment uw gegevens niet meer ophalen.',
  },
  {
    q: 'Welke organisaties kunnen mij om toestemming vragen?',
    a: 'Alleen organisaties die zijn aangesloten op de Gemeenschappelijke Bronontsluiting (GBO) en die voldoen aan de voorwaarden van uw sector.',
  },
  {
    q: 'Ik heb een vraag over een toestemming.',
    a: 'Neem contact op met de helpdesk van MijnOverheid. Klik op "Contact" onderaan deze pagina.',
  },
]

const Chevron = ({ open }: { open: boolean }) => (
  <svg
    width="15"
    height="15"
    viewBox="0 0 24 24"
    fill="none"
    stroke="#0A2845"
    strokeWidth="2.5"
    className={`faq-chevron${open ? ' open' : ''}`}
  >
    <path d="M9 6l6 6-6 6" />
  </svg>
)

export default function FaqAccordion() {
  const [openIdx, setOpenIdx] = useState<number | null>(null)
  return (
    <div className="content-card">
      <h2 className="content-card-h2">Veelgestelde vragen</h2>
      <ul className="faq-list">
        {ITEMS.map((it, i) => {
          const open = openIdx === i
          return (
            <li key={i} className={`faq-item${open ? ' open' : ''}`}>
              <button
                className="faq-toggle"
                onClick={() => setOpenIdx(open ? null : i)}
                aria-expanded={open}
              >
                <Chevron open={open} />
                <span>{it.q}</span>
              </button>
              {open && <div className="faq-answer">{it.a}</div>}
            </li>
          )
        })}
      </ul>
    </div>
  )
}
