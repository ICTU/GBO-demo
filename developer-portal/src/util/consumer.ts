// Which consumer a DvTP use-run goes through. In the demo each source has one
// consumer: LVG's is the Installatie Register (ir-backend, its own FSC peer),
// every other source's is Hypotheek-BV (dienstverlener-backend).

export type Consumer = {
  name: string
  proxy: string
  hostPort: number
}

export const HYPOTHEEK_BV: Consumer = { name: 'Hypotheek-BV', proxy: '/dvtp-api', hostPort: 9406 }
export const INSTALLATIE_REGISTER: Consumer = { name: 'Installatie Register', proxy: '/ir-api', hostPort: 9410 }

export function isLvgScope(scope?: string): boolean {
  return !!scope && scope.startsWith('lvg:')
}

export function consumerFor(scope?: string): Consumer {
  return isLvgScope(scope) ? INSTALLATIE_REGISTER : HYPOTHEEK_BV
}
