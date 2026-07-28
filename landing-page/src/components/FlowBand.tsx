/* Eén bron, één gestandaardiseerde ontsluiting, drie uitgangen. De
   stippellijnen lopen mee met de gbo-flow-animatie uit tokens.css. */
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
                stroke="#a8c4d4"
                strokeWidth="1.4"
                vectorEffect="non-scaling-stroke"
              />
              <path
                data-flow=""
                d="M0 50 H100"
                pathLength={100}
                fill="none"
                stroke="#01689b"
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
              <g fill="none" stroke="#a8c4d4" strokeWidth="1.4" vectorEffect="non-scaling-stroke">
                <path d="M0 50 C 50 50, 50 17, 100 17" pathLength={100} />
                <path d="M0 50 H100" pathLength={100} />
                <path d="M0 50 C 50 50, 50 83, 100 83" pathLength={100} />
              </g>
              {/* Alle drie de stromen horen bij de use cases; OOTS wordt later
                  toegevoegd maar ligt hier niet stil. */}
              <g
                data-flow=""
                fill="none"
                stroke="#01689b"
                strokeWidth="1.8"
                strokeDasharray="7 93"
                vectorEffect="non-scaling-stroke"
              >
                <path d="M0 50 C 50 50, 50 17, 100 17" pathLength={100} />
                <path d="M0 50 H100" pathLength={100} />
                <path d="M0 50 C 50 50, 50 83, 100 83" pathLength={100} />
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
                <div className="flow-output-desc">
                  Delen via Toestemming naar Private partijen
                </div>
              </div>
            </div>
          </div>
        </div>
      </div>
    </div>
  )
}
