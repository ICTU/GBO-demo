import { SCOPE_GROUPS } from '../../data/scopeGroups'

type Props = { scopes: string[] }

export default function ScopeVeldenList({ scopes }: Props) {
  // For each consented scope: render the scope title + catalog fields.
  // Fields come from scopeGroups (hardcoded copy of the service catalog).
  const groups = scopes
    .map((s) => SCOPE_GROUPS.find((g) => g.code === s))
    .filter((g): g is (typeof SCOPE_GROUPS)[number] => !!g)

  if (!groups.length) {
    return (
      <div className="scope-velden-empty">
        Velden onbekend — scope-IDs: <code>{scopes.join(', ')}</code>
      </div>
    )
  }

  return (
    <ul className="scope-velden">
      {groups.map((g) => (
        <li key={g.code} className="scope-velden-title">{g.title}</li>
      ))}
    </ul>
  )
}
