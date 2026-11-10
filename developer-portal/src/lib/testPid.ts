/* The test PID block: the portal backend asks the own credential issuer for
   a pre-authorized offer of the test PID for a BRP mock person, and the
   portal shows its link as a QR code for the EUDI reference wallet. The
   backend has no test PID routes unless the deployment turns them on; then
   the block is not shown. */

export type TestPerson = { bsn: string; name: string }

export type TestPersons =
  | { enabled: false }
  | { enabled: true; persons: TestPerson[]; error: string }

export type TestPidOffer = { link: string } | { error: string }

const MESSAGES: Record<string, string> = {
  unknown_person: 'Deze persoon staat niet in de lijst met testpersonen.',
  no_data: 'test-pid-source kent dit BSN niet; de issuer maakte geen offer.',
  test_pid_not_configured:
    'De credential issuer biedt geen test-PID aan: de provider test-pid ontbreekt op het eudi-profiel (make provision-test-pid).',
  source_unavailable: 'test-pid-source is niet bereikbaar voor de credential issuer.',
  bad_source_data: 'test-pid-source gaf gegevens die de credential issuer niet kon gebruiken.',
  issuer_unavailable: 'De credential issuer is niet bereikbaar. Draait het compose-profiel credential-issuer?',
  issuer_error: 'De credential issuer gaf een onverwacht antwoord.',
}

export function testPidMessage(code: string): string {
  return MESSAGES[code] ?? `Geen test-PID-offer (${code}).`
}

export function personLabel(person: TestPerson): string {
  return `${person.name} (${person.bsn})`
}

/** The persons answer; status null is a request that got no answer. */
export function testPersonsFrom(status: number | null, body: unknown): TestPersons {
  if (status === 404) return { enabled: false }
  if (status === 200 && Array.isArray(body)) return { enabled: true, persons: body as TestPerson[], error: '' }
  return { enabled: true, persons: [], error: 'Testpersonen niet beschikbaar.' }
}

export function testPidOfferFrom(status: number, body: unknown): TestPidOffer {
  const answer = (body ?? {}) as { link?: unknown; error?: unknown }
  if (status === 201) {
    return typeof answer.link === 'string' && answer.link
      ? { link: answer.link }
      : { error: testPidMessage('issuer_error') }
  }
  return { error: testPidMessage(typeof answer.error === 'string' ? answer.error : `HTTP ${status}`) }
}
