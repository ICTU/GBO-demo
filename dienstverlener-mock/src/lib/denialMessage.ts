// Citizen-facing text for a failed retrieval, chosen by denial_code (see
// services/dienstverlener-backend/consumer/denial.go). Anything not named here gets
// the generic message, which does not mention consent.

export type DenialMessage = {
  title: string
  body: string
  retry: boolean
  reconsent: boolean
}

// A Map, so network input like "constructor" cannot hit a prototype key.
// The policy does not tell a dienstverlener why a field was refused, so the
// message names no cause; it offers the one useful action, consenting again.
const CITIZEN_MESSAGES = new Map<string, DenialMessage>([
  [
    'FIELD_NOT_PERMITTED',
    {
      title: 'Deze gegevens mogen wij niet ophalen',
      body:
        'Wij mogen uw inkomensgegevens niet ophalen bij de Belastingdienst. ' +
        'U kunt opnieuw toestemming geven.',
      retry: false,
      reconsent: true,
    },
  ],
])

const GENERIC: DenialMessage = {
  title: 'Ophalen mislukt',
  body: 'We konden uw inkomensgegevens nu niet ophalen.',
  retry: true,
  reconsent: false,
}

export function denialMessage(code?: string | null): DenialMessage {
  if (!code) return GENERIC
  return CITIZEN_MESSAGES.get(code) ?? GENERIC
}

export const NAMED_DENIAL_CODES = [...CITIZEN_MESSAGES.keys()]
