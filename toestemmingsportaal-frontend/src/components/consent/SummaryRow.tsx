import { ReactNode } from 'react'

type Item = { label: string; value: ReactNode }
type Props = { items: Item[] }

export default function SummaryRow({ items }: Props) {
  return (
    <div className="gov-card-inset summary">
      <dl>
        {items.map((it, i) => (
          <div key={i} style={{ display: 'contents' }}>
            <dt>{it.label}</dt>
            <dd>{it.value}</dd>
          </div>
        ))}
      </dl>
    </div>
  )
}
