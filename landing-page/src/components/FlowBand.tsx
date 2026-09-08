/* Eén bron, één gestandaardiseerde ontsluiting, vier uitgangen. De
   stippellijnen lopen mee met de gbo-flow-animatie uit tokens.css.

   De y-waarden van de rails zijn de middens van de vier uitgangen. Die
   delen in tokens.css hun gridrij in vier gelijke fracties, en de viewBox
   is 100 hoog, dus 12.5 / 37.5 / 62.5 / 87.5. Komt er een uitgang bij of
   valt er een af, dan moeten deze waarden én grid-template-rows mee. */
const FAN_RAILS = [
  'M0 50 C 50 50, 50 12.5, 100 12.5',
  'M0 50 C 50 50, 50 37.5, 100 37.5',
  'M0 50 C 50 50, 50 62.5, 100 62.5',
]

const FAN_RAIL_FUTURE = 'M0 50 C 50 50, 50 87.5, 100 87.5'

export default function FlowBand() {
  return (
    <div className="flowband">
      <div className="flowcard">
        <div className="flowcard-head">
          <p>Eén ontsluiting van de bron, in plaats van een aparte koppeling per use case.</p>
        </div>
        <div className="flowcard-body">
          <div className="flowgrid">
            <div className="flow-source">
              <div className="flow-source-title">Overheidsbron</div>
              <div className="flow-source-sub">bronhouder houdt regie</div>
            </div>

            <svg
              viewBox="0 0 100 100"
              preserveAspectRatio="none"
              aria-hidden="true"
              focusable="false"
              className="flow-link"
            >
              <path
                d="M0 50 H100"
                pathLength={100}
                fill="none"
                stroke="currentColor"
                strokeWidth="1.4"
                vectorEffect="non-scaling-stroke"
              />
              <path
                className="flow-pulse"
                data-flow=""
                d="M0 50 H100"
                pathLength={100}
                fill="none"
                strokeWidth="1.8"
                strokeDasharray="7 93"
                vectorEffect="non-scaling-stroke"
              />
            </svg>

            <div className="flow-hub">
              <div className="flow-hub-title">GBO</div>
              <div className="flow-hub-sub">
                Gestandaardiseerde ontsluiting: afspraken, standaarden en generieke voorzieningen.
              </div>
            </div>

            <svg
              viewBox="0 0 100 100"
              preserveAspectRatio="none"
              aria-hidden="true"
              focusable="false"
              className="flow-fan"
            >
              <g
                fill="none"
                stroke="currentColor"
                strokeWidth="1.4"
                vectorEffect="non-scaling-stroke"
              >
                {FAN_RAILS.map((d) => (
                  <path key={d} d={d} pathLength={100} />
                ))}
                <path className="flow-rail--future" d={FAN_RAIL_FUTURE} pathLength={100} />
              </g>
              {/* Alle drie de use cases horen erbij; OOTS wordt later toegevoegd
                  maar ligt hier niet stil. Nieuwe toepassingen krijgen geen puls:
                  daar loopt nog niets overheen. */}
              <g
                className="flow-pulse"
                data-flow=""
                fill="none"
                strokeWidth="1.8"
                strokeDasharray="7 93"
                vectorEffect="non-scaling-stroke"
              >
                {FAN_RAILS.map((d) => (
                  <path key={d} d={d} pathLength={100} />
                ))}
              </g>
            </svg>

            <div className="flow-outputs">
              <div className="flow-output">
                <div className="flow-output-name">EUDI</div>
                <div className="flow-output-desc">Europese Digitale Identiteit wallet</div>
              </div>
              <div className="flow-output">
                <div className="flow-output-name">OOTS</div>
                <div className="flow-output-desc">Once-Only Technical System</div>
              </div>
              <div className="flow-output">
                <div className="flow-output-name">DvTP</div>
                <div className="flow-output-desc">Delen via Toestemming naar Private partijen</div>
              </div>
              <div className="flow-output flow-output--future">
                <div className="flow-output-name">Nieuwe toepassingen</div>
                <div className="flow-output-desc">Zonder nieuwe koppeling per use case</div>
              </div>
            </div>
          </div>
        </div>
      </div>
    </div>
  )
}
