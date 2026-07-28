/* Teksten 1:1 overgenomen uit het design "GBO Landing.dc.html". */

export type PipelineStep = {
  name: string
  desc: string
}

export const PIPELINE_STEPS: PipelineStep[] = [
  {
    name: 'Afnemer',
    desc: 'De afnemende partij start een verzoek. Afhankelijk van de stroom draagt dat verzoek een toestemming-ID of een identifier uit de wallet.',
  },
  {
    name: 'FSC-Outway',
    desc: 'De uitgaande kant bij de afnemer. De outway zet het verzoek op het contract dat met de bronhouder is gesloten en verstuurt het over het federatienetwerk.',
  },
  {
    name: 'FSC-Inway',
    desc: 'Het transportkanaal van de bronhouder. FSC valideert het contract tussen deelnemers; alleen geregistreerde partijen uit de federatie komen binnen.',
  },
  {
    name: 'OpenFTV',
    desc: 'Toetst het verzoek aan het beleid van de bronhouder: is er een grondslag, past de gevraagde set bij het doel, en om welke persoon gaat het. Eén beleidsbron voor alle ingangen.',
  },
  {
    name: 'Bron',
    desc: 'Het registratiesysteem van de bronhouder levert uitsluitend de velden die het beleid heeft toegestaan.',
  },
]

export const BENEFITS: string[] = [
  'Aansluiting op Europese en internationale afspraken en standaarden',
  'Lagere implementatielast voor bronhouders',
  'Minder maatwerk en ad-hoc-implementaties',
  'Beter hergebruik van generieke voorzieningen',
  'Meer uniformiteit en interoperabiliteit tussen overheden, en tussen overheid en private partijen',
  'Een herbruikbare bronontsluiting voor nieuwe ontwikkelingen',
]

export function stepNumber(index: number): string {
  return String(index + 1).padStart(2, '0')
}
