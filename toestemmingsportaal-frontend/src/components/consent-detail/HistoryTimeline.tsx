import { formatHistorieDate } from '../../data/onderwerpMap'

export type HistoryEvent = {
  label: string
  at: string // ISO
  type: 'uitgegeven' | 'ingetrokken' | 'verlopen'
}

type Props = { events: HistoryEvent[] }

export default function HistoryTimeline({ events }: Props) {
  return (
    <ol className="timeline">
      {events.map((e, i) => (
        <li key={i} className="timeline-item">
          <span className={`timeline-dot ${e.type}`} aria-hidden />
          <div className="timeline-body">
            <div className="timeline-label">{e.label}</div>
            <div className="timeline-date">{formatHistorieDate(e.at)}</div>
          </div>
        </li>
      ))}
    </ol>
  )
}
