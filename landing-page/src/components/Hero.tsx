import type { CSSProperties } from 'react'

/* Zes deelnemers rond één bron, in een veld van 400×400 met de knopen om
   de 60° op straal 150. Over de helft van de spaken loopt een puls naar
   buiten en over de andere helft naar binnen; dat heen-en-weer is wat het
   beeld tot uitwisseling maakt in plaats van tot een sterretje. De pulsen
   dragen data-flow, dus ze erven de gbo-flow-animatie én de regel die hem
   stilzet bij prefers-reduced-motion. */
const HUB_NODES = [
  { x: 350, y: 200 },
  { x: 275, y: 330 },
  { x: 125, y: 330 },
  { x: 50, y: 200 },
  { x: 125, y: 70 },
  { x: 275, y: 70 },
]

type Pulse = { x: number; y: number; dur: string; delay: string }

/* Negatieve delays: de animatie begint dan halverwege in plaats van dat
   alle zes pulsen tegelijk uit het midden vertrekken. */
const PULSES_OUT: Pulse[] = [
  { x: 350, y: 200, dur: '3.4s', delay: '0s' },
  { x: 125, y: 330, dur: '4.1s', delay: '-1.2s' },
  { x: 125, y: 70, dur: '3.8s', delay: '-2.4s' },
]

const PULSES_IN: Pulse[] = [
  { x: 275, y: 330, dur: '3.6s', delay: '0s' },
  { x: 50, y: 200, dur: '4.4s', delay: '-1.8s' },
  { x: 275, y: 70, dur: '3.2s', delay: '-0.6s' },
]

function pulseStyle(p: Pulse, reverse: boolean): CSSProperties {
  return {
    animationDuration: p.dur,
    animationDelay: p.delay,
    ...(reverse ? { animationDirection: 'reverse' } : null),
  }
}

function HeroArt() {
  return (
    <div className="hero-art" aria-hidden="true">
      <svg viewBox="0 0 400 400" focusable="false">
        <circle className="hero-art-ring" cx="200" cy="200" r="150" />
        <circle className="hero-art-ring" cx="200" cy="200" r="196" />

        <g className="hero-art-spoke">
          {HUB_NODES.map((n) => (
            <path key={`${n.x}-${n.y}`} d={`M200 200 L${n.x} ${n.y}`} />
          ))}
        </g>

        <g className="hero-art-pulse">
          {PULSES_OUT.map((p) => (
            <path
              key={`${p.x}-${p.y}`}
              data-flow=""
              d={`M200 200 L${p.x} ${p.y}`}
              pathLength={100}
              strokeDasharray="9 91"
              style={pulseStyle(p, false)}
            />
          ))}
        </g>

        <g className="hero-art-pulse hero-art-pulse--soft">
          {PULSES_IN.map((p) => (
            <path
              key={`${p.x}-${p.y}`}
              data-flow=""
              d={`M200 200 L${p.x} ${p.y}`}
              pathLength={100}
              strokeDasharray="9 91"
              style={pulseStyle(p, true)}
            />
          ))}
        </g>

        <g className="hero-art-node">
          {HUB_NODES.map((n) => (
            <circle key={`${n.x}-${n.y}`} cx={n.x} cy={n.y} r={7} />
          ))}
        </g>

        <circle className="hero-art-core" cx="200" cy="200" r="26" />
      </svg>
    </div>
  )
}

export default function Hero() {
  return (
    <section className="hero">
      <HeroArt />
      <div className="hero-topbar">
        <div className="shell hero-topbar-row">
          <span className="wordmark">GBO</span>
          <nav className="hero-nav">
            <a href="#wat-is-gbo">Wat is GBO</a>
            <a href="#stromen">Stromen</a>
            <a href="#pipeline">Pipeline</a>
            <a href="#uitproberen" className="is-primary">
              Zelf uitproberen
            </a>
          </nav>
        </div>
      </div>
      <div className="hero-body">
        {/* De shell houdt de volle breedte — zou hij zelf op 54% staan, dan
            centreert zijn eigen margin: 0 auto het tekstblok. */}
        <div className="shell">
          <div className="hero-stack">
            <h1>
              Gemeenschappelijke Bronontsluiting
              <br />
              Demo-omgeving
            </h1>
            <p className="hero-lede">
              Gemeenschappelijke Bronontsluiting is de gestandaardiseerde ontsluiting waarmee
              bronhouders hun gegevens direct interoperabel en herbruikbaar beschikbaar stellen.
              Deze demo draait live op de Simulatieomgeving van het Federatief Datastelsel.
            </p>
            <a href="#uitproberen" className="btn-invert">
              Zelf uitproberen
            </a>
          </div>
        </div>
      </div>
    </section>
  )
}
