export type ScopeGroup = {
  code: string
  title: string
  blurb: string
  fields: string[]
}

export const SCOPE_GROUPS: ScopeGroup[] = [
  {
    code: 'bd:ib:2025',
    title: 'Belastingdienst — Inkomstenbelasting 2025',
    blurb:
      'Bevat uw aangiftegegevens over het belastingjaar 2025. De hypotheekverlener gebruikt deze om uw inkomen te toetsen voor een hypotheekaanvraag.',
    fields: ['belastingjaar', 'verzamelinkomen', 'box1Inkomen', 'status', 'indieningsdatum'],
  },
  {
    code: 'bd:ib:2024',
    title: 'Belastingdienst — Inkomstenbelasting 2024',
    blurb:
      'Hetzelfde type gegevens, maar over belastingjaar 2024. Hypotheekverleners vragen vaak om twee opeenvolgende jaren.',
    fields: ['belastingjaar', 'verzamelinkomen', 'box1Inkomen', 'status', 'indieningsdatum'],
  },
  {
    code: 'lvg:vbo:eigendom',
    title: 'Landelijke Voorziening Gebouwen — Eigendom verblijfsobject',
    blurb:
      'Het Installatie Register controleert of het pand waarvoor u een verzoek kreeg op uw naam staat. Zij krijgen alleen ja of nee terug.',
    fields: ['vboId'],
  },
]

// The groups the dienstverlener asked for, in the order it asked. A scope the
// portal cannot describe is returned in unknown: the citizen cannot consent to
// what the portal cannot show, so the caller refuses rather than drops it.
export function requestedScopeGroups(requested: string[]): { groups: ScopeGroup[]; unknown: string[] } {
  const groups: ScopeGroup[] = []
  const unknown: string[] = []
  for (const code of new Set(requested)) {
    const group = SCOPE_GROUPS.find((s) => s.code === code)
    if (group) groups.push(group)
    else unknown.push(code)
  }
  return { groups, unknown }
}
