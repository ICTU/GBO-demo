// The Installatie Register's own state for one demo run: the installer's
// request, the owner's decision and the DvTP consent token IR keeps for its
// recurring ownership checks. None of this is GBO — in the pilot it lives in
// IR (Keyper). The demo switches between installer and owner in one browser,
// so sessionStorage is enough.

export type UseCase = 'datastekker' | 'onderhoudsboekje'

export type Aanvraag = {
  id: string
  useCase: UseCase
  installateur: { naam: string; email: string; organisatie: string; kvk: string }
  gebouw: { adres: string; postcode: string; plaats: string; vboId: string }
  eigenaar: { naam: string; email: string }
  reikwijdte: { segment: string; van: string; tot: string }
  aangemaakt: string
  verstuurd?: string
  eigendomBevestigd?: string
  goedgekeurd?: string
  afgewezen?: string
  // The signed DvTP consent token from MijnOverheid. IR keeps it for the
  // recurring check at LVG; its PI is readable by BSNk only.
  consentToken?: string
}

const KEY = 'keyper.aanvraag'

export const BUILDINGS = [
  { adres: 'Meidoornhof 12', postcode: '3448 XR', plaats: 'Woerden', vboId: '0632010000099412' },
  { adres: 'Meidoornhof 12A', postcode: '3448 XR', plaats: 'Woerden', vboId: '0632010000099413' },
  { adres: 'Meidoornhof 12B', postcode: '3448 XR', plaats: 'Woerden', vboId: '0632010000099414' },
]

function isoDate(d: Date): string {
  return d.toISOString().slice(0, 10)
}

export function newAanvraag(useCase: UseCase, now = new Date()): Aanvraag {
  const nextYear = new Date(now)
  nextYear.setFullYear(now.getFullYear() + 1)
  const stamp = isoDate(now).split('-').join('')
  return {
    id: `TN-GIR-${stamp}-0003`,
    useCase,
    installateur: {
      naam: 'P. Jansen',
      email: 'p.jansen@warmtetechniekjansen.nl',
      organisatie: 'Warmtetechniek Jansen',
      kvk: '34215876',
    },
    gebouw: BUILDINGS[0],
    eigenaar: { naam: 'J. de Vries', email: 'j.devries@voorbeeld.nl' },
    reikwijdte: { segment: '51 Warmteopwekking', van: isoDate(now), tot: isoDate(nextYear) },
    aangemaakt: now.toISOString(),
  }
}

export function loadAanvraag(): Aanvraag | null {
  try {
    const raw = sessionStorage.getItem(KEY)
    return raw ? (JSON.parse(raw) as Aanvraag) : null
  } catch {
    return null
  }
}

export function saveAanvraag(aanvraag: Aanvraag): Aanvraag {
  sessionStorage.setItem(KEY, JSON.stringify(aanvraag))
  return aanvraag
}

export function updateAanvraag(change: Partial<Aanvraag>): Aanvraag | null {
  const current = loadAanvraag()
  return current ? saveAanvraag({ ...current, ...change }) : null
}

export function clearAanvraag() {
  sessionStorage.removeItem(KEY)
}

export function toepassing(useCase: UseCase): string {
  return useCase === 'onderhoudsboekje' ? 'Digitaal Onderhoudsboekje' : 'Datastekker toegang'
}

// "21-9-2026, 09:12:16", as the GIR portal writes timestamps.
export function formatStamp(iso?: string): string {
  if (!iso) return ''
  const d = new Date(iso)
  if (isNaN(d.getTime())) return iso
  return (
    d.toLocaleDateString('nl-NL', { day: 'numeric', month: 'numeric', year: 'numeric' }) +
    ', ' +
    d.toLocaleTimeString('nl-NL', { hour: '2-digit', minute: '2-digit', second: '2-digit' })
  )
}

export function formatDay(isoDay: string): string {
  const d = new Date(isoDay)
  if (isNaN(d.getTime())) return isoDay
  return d.toLocaleDateString('nl-NL', { day: 'numeric', month: 'long', year: 'numeric' })
}
