type Props = { tab: 'actief' | 'verlopen' }

export default function EmptyState({ tab }: Props) {
  return (
    <div className="consent-empty">
      {tab === 'actief'
        ? 'U heeft geen actieve toestemmingen.'
        : 'U heeft geen verlopen of ingetrokken toestemmingen.'}
    </div>
  )
}
