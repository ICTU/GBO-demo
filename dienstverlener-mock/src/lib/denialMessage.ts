// What the citizen is told when their data could not be retrieved.
//
// The choice is made by denial_code, which the backend has already judged
// disclosable (services/dienstverlener-backend/denial.go). This module never
// looks at `reason`: that is technical text, and a citizen-facing sentence
// must not depend on prose produced by another component.
//
// Anything not named here — an administrative policy denial, a transport
// failure, a code added upstream after this was written — falls through to
// one honest message that does not mention consent. Telling someone their
// consent might be the problem when it is not points them at the one thing
// they would go and "fix" themselves.

export type DenialMessage = {
  title: string
  body: string
  /** Retrying helps only when the failure could be temporary. */
  retry: boolean
  /** Granting consent again is the route out. */
  reconsent: boolean
}

// A Map rather than an object: the code arrives over the network, and a
// plain object lookup would answer for keys like "constructor".
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

/** The codes this UI is willing to name. Exported for the tests. */
export const NAMED_DENIAL_CODES = [...CITIZEN_MESSAGES.keys()]
