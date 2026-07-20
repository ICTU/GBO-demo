import { ReactNode } from 'react'

type Props = { items: Array<{ label: string; value: ReactNode }> }

export default function DetailGrid({ items }: Props) {
  return (
    <dl className="detail-grid">
      {items.map((it, i) => (
        <div key={i} className="detail-grid-row">
          <dt>{it.label}</dt>
          <dd>{it.value}</dd>
        </div>
      ))}
    </dl>
  )
}
