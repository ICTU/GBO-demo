import { useState, type CSSProperties } from 'react'
import { PIPELINE_STEPS, stepNumber } from '../data/content'

export default function Pipeline() {
  const [step, setStep] = useState(0)
  const active = PIPELINE_STEPS[step]

  return (
    <section id="pipeline" data-reveal className="section section--dark reveal">
      <div className="shell">
        <div className="grid-12 pipeline-intro">
          <h2 className="h2-serif span-5">De reis van één verzoek</h2>
          <p className="prose prose--on-dark span-7">
            Of een verzoek nu via toestemming of via de wallet binnenkomt, achter FSC volgt het
            dezelfde route. Beleid wordt op één plek getoetst en gegevens worden op één plek
            opgehaald. Kies een halte voor de details.
          </p>
        </div>

        <div className="pipeline-rail">
          <div className="pipeline-rail-line" />
          <svg
            viewBox="0 0 100 1"
            preserveAspectRatio="none"
            aria-hidden="true"
            focusable="false"
            className="pipeline-rail-flow"
          >
            <path
              data-flow=""
              d="M0 0.5 H100"
              pathLength={100}
              fill="none"
              stroke="#8fcbe8"
              strokeWidth="2"
              strokeDasharray="8 92"
              vectorEffect="non-scaling-stroke"
            />
          </svg>
          <div
            className="pipeline-steps"
            style={{ '--step-count': PIPELINE_STEPS.length } as CSSProperties}
          >
            {PIPELINE_STEPS.map((s, i) => (
              <button
                key={s.name}
                type="button"
                className="pipeline-step"
                aria-pressed={i === step}
                onClick={() => setStep(i)}
              >
                <span className="pipeline-step-dot" aria-hidden="true" />
                <span className="pipeline-step-num">{stepNumber(i)}</span>
                <span className="pipeline-step-name">{s.name}</span>
              </button>
            ))}
          </div>
        </div>

        <div className="grid-12 pipeline-detail">
          <div className="span-4">
            <div className="pipeline-detail-num">{stepNumber(step)}</div>
            <div className="pipeline-detail-name">{active.name}</div>
          </div>
          <p className="pipeline-detail-desc span-8">{active.desc}</p>
        </div>
      </div>
    </section>
  )
}
