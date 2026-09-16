// Citizen-facing text for a failed retrieval, chosen by denial_code (see
// services/dienstverlener-backend/denial.go). Anything not named here gets
// the generic message, which does not mention consent.

export type DenialMessage = {
  title: string
  body: string
  retry: boolean
  reconsent: boolean
}

// A Map, so network input like "constructor" cannot hit a prototype key.
const CITIZEN_MESSAGES = new Map<string, DenialMessage>([
  [
    'CONSENT_WITHDRAWN',
    {
      title: 'Uw toestemming is ingetrokken',
      body:
        'U heeft uw toestemming om inkomensgegevens op te halen bij de Belastingdienst ' +
        'ingetrokken. Daarom hebben wij die gegevens niet opgehaald.',
      retry: false,
      reconsent: true,
    },
  ],
  [
    'CONSENT_EXPIRED',
    {
      title: 'Uw toestemming is verlopen',
      body:
        'Uw toestemming om inkomensgegevens op te halen bij de Belastingdienst is verlopen. ' +
        'Toestemming geldt een beperkte tijd.',
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
