const PREFIX = 'gbo.consent-token.'

export function storeConsentToken(consentId: string, token: string): void {
  if (consentId && token) sessionStorage.setItem(PREFIX + consentId, token)
}

export function consentTokenFor(consentId: string): string {
  return consentId ? sessionStorage.getItem(PREFIX + consentId) ?? '' : ''
}
