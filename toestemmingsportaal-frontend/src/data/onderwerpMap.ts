// Mapping from scope IDs to citizen-friendly topic text.
// NOTE: hardcoded — ideally sourced from the service catalog (dienstencatalogus.json).

export function deriveOnderwerp(scopes: string[]): string {
  const has2025 = scopes.includes('bd:ib:2025')
  const has2024 = scopes.includes('bd:ib:2024')
  if (has2025 && has2024) return 'Inkomensgegevens (IB 2025 + 2024)'
  if (has2025) return 'Inkomensgegevens (IB 2025)'
  if (has2024) return 'Inkomensgegevens (IB 2024)'
  return scopes.join(', ')
}

const OIN_NAMES: Record<string, string> = {
  '00000001234567890000': 'Hypotheek-BV',
}

export function deriveAfnemerName(oin: string): string {
  return OIN_NAMES[oin] ?? oin
}

export function deriveDoel(useCase: string): string {
  if (useCase === 'hypotheek') return 'Hypotheek-aanvraag (aankoop woning)'
  return useCase
}

export function formatGeldigTot(iso: string): string {
  if (!iso) return ''
  const d = new Date(iso)
  if (isNaN(d.getTime())) return iso
  return d.toLocaleDateString('nl-NL', { year: 'numeric', month: 'short', day: 'numeric' })
}

export function formatHistorieDate(iso: string): string {
  if (!iso) return ''
  const d = new Date(iso)
  if (isNaN(d.getTime())) return iso
  return d.toLocaleDateString('nl-NL', { year: 'numeric', month: 'short', day: 'numeric' }) +
    ' · ' + d.toLocaleTimeString('nl-NL', { hour: '2-digit', minute: '2-digit' })
}
