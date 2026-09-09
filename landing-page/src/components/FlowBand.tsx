import { useElementSize } from '../hooks/useElementSize'

/* Eén bron, één gestandaardiseerde ontsluiting, vier uitgangen. De
   stippellijnen lopen mee met de gbo-flow-animatie uit tokens.css.

   De waaier wordt in echte pixels getekend in plaats van in een vaste
   viewBox van 100×100. Dat vak is namelijk smal en hoog — rond 65×236 —
   en met preserveAspectRatio="none" werd de tekening horizontaal 0,65×
   samengedrukt en verticaal 2,36× uitgerekt. Een streepje is overal even
   lang in padcoördinaten, dus onder die vervorming smeerde het uit op de
   steile stukken van de curve. Op 1:1 is er geen vervorming meer.

   De uitgangen verdelen hun gridrij in vier gelijke fracties (zie
   tokens.css), dus de rails komen uit op 1/8, 3/8, 5/8 en 7/8 van de
   hoogte. Komt er een uitgang bij of valt er een af, dan moeten deze
   breuken én grid-template-rows mee. */
const FAN_STOPS = [0.125, 0.375, 0.625]
const FAN_STOP_FUTURE = 0.875

function fanRail(w: number, h: number, stop: number): string {
  return `M0 ${h / 2} C ${w / 2} ${h / 2}, ${w / 2} ${h * stop}, ${w} ${h * stop}`
}

export default function FlowBand() {
  /* De maat komt van het omhullende vak, niet van de SVG zelf. Een SVG die
     zijn viewBox uit zijn eigen hoogte afleidt én in de layout meetelt,
     praat tegen zichzelf: de rij groeide dan bij elke meting mee. De SVG
     ligt daarom absoluut in dit vak en telt niet mee voor de rijhoogte. */
  const [fanRef, fan] = useElementSize<HTMLDivElement>()
  const drawFan = fan.width > 0 && fan.height > 0

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

            {/* Recht en horizontaal, dus hier vervormt een niet-uniforme
                schaal de streepjes niet: ze lopen mee met één as. */}
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

            <div className="flow-fan" ref={fanRef}>
              <svg
                viewBox={drawFan ? `0 0 ${fan.width} ${fan.height}` : undefined}
                aria-hidden="true"
                focusable="false"
                className="flow-fan-svg"
              >
                {drawFan && (
                  <>
                    <g
                      fill="none"
                      stroke="currentColor"
                      strokeWidth="1.4"
                      vectorEffect="non-scaling-stroke"
                    >
                      {FAN_STOPS.map((stop) => (
                        <path key={stop} d={fanRail(fan.width, fan.height, stop)} pathLength={100} />
                      ))}
                      <path
                        className="flow-rail--future"
                        d={fanRail(fan.width, fan.height, FAN_STOP_FUTURE)}
                        pathLength={100}
                      />
                    </g>
                    {/* Alle drie de use cases horen erbij; OOTS wordt later
                        toegevoegd maar ligt hier niet stil. Nieuwe toepassingen
                        krijgen geen puls: daar loopt nog niets overheen. */}
                    <g
                      className="flow-pulse"
                      data-flow=""
                      fill="none"
                      strokeWidth="1.8"
                      strokeDasharray="7 93"
                      vectorEffect="non-scaling-stroke"
                    >
                      {FAN_STOPS.map((stop) => (
                        <path key={stop} d={fanRail(fan.width, fan.height, stop)} pathLength={100} />
                      ))}
                    </g>
                  </>
                )}
              </svg>
            </div>

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
