import { ScopeGroup } from '../../data/scopeGroups'

type Props = {
  scope: ScopeGroup
  checked: boolean
  onChange: (checked: boolean) => void
}

export default function ScopeToggle({ scope, checked, onChange }: Props) {
  return (
    <label className={`scope-card${checked ? '' : ' unchecked'}`}>
      <input
        type="checkbox"
        className="gov-check"
        checked={checked}
        onChange={(e) => onChange(e.target.checked)}
        aria-label={scope.title}
      />
      <div>
        <div className="scope-card-title">{scope.title}</div>
        <div className="scope-card-blurb">{scope.blurb}</div>
      </div>
    </label>
  )
}
