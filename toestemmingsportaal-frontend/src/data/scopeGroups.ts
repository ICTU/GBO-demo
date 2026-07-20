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
    fields: ['belastingjaar', 'verzamelinkomen', 'inkomenUitBox1', 'grondslag', 'peilDatum'],
  },
  {
    code: 'bd:ib:2024',
    title: 'Belastingdienst — Inkomstenbelasting 2024',
    blurb:
      'Hetzelfde type gegevens, maar over belastingjaar 2024. Hypotheekverleners vragen vaak om twee opeenvolgende jaren.',
    fields: ['belastingjaar', 'verzamelinkomen', 'inkomenUitBox1', 'grondslag', 'peilDatum'],
  },
]
