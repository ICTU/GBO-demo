type Props = {
  active: 'actief' | 'verlopen'
  onChange: (tab: 'actief' | 'verlopen') => void
  counts?: { actief: number; verlopen: number }
}

export default function ConsentTabs({ active, onChange, counts }: Props) {
  const tabs: { id: 'actief' | 'verlopen'; label: string }[] = [
    { id: 'actief', label: 'Actief' },
    { id: 'verlopen', label: 'Verlopen' },
  ]
  return (
    <div className="consent-tabs" role="tablist">
      {tabs.map((t) => (
        <button
          key={t.id}
          role="tab"
          aria-selected={active === t.id}
          className={`consent-tab${active === t.id ? ' active' : ''}`}
          onClick={() => onChange(t.id)}
        >
          {t.label}
          {counts && <span className="consent-tab-count"> ({counts[t.id]})</span>}
        </button>
      ))}
    </div>
  )
}
