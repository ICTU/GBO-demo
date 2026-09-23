// Which consumer a DvTP use-run goes through. In the demo each source has one
// consumer: LVG's is the Installatie Register (ir-backend, its own FSC peer),
// every other source's is Hypotheek-BV (dienstverlener-backend).

import { BD_BRON, LVG_BRON, type BronProfile } from '../data/bronnen'

export type Consumer = {
  name: string
  proxy: string
  hostPort: number
  // The consumer's half of the use-chain in the architecture strip: its
  // backend, its own FSC peer (Outway + Manager, and the peer name the
  // dev-portal backend uses for its transaction log) and the bron it asks.
  backendSvc: string
  outwayName: string
  outwaySvc: string
  managerSvc: string
  fscPeer: string
  bron: BronProfile
}

export const HYPOTHEEK_BV: Consumer = {
  name: 'Hypotheek-BV',
  proxy: '/dvtp-api',
  hostPort: 9406,
  backendSvc: 'dienstverlener-backend',
  outwayName: 'HV-Outway',
  outwaySvc: 'hv-outway',
  managerSvc: 'hv-manager',
  fscPeer: 'hv',
  bron: BD_BRON,
}

export const INSTALLATIE_REGISTER: Consumer = {
  name: 'Installatie Register',
  proxy: '/ir-api',
  hostPort: 9410,
  backendSvc: 'ir-backend',
  outwayName: 'IR-Outway',
  outwaySvc: 'ir-outway',
  managerSvc: 'ir-manager',
  fscPeer: 'ir',
  bron: LVG_BRON,
}

export const CONSUMERS: Consumer[] = [HYPOTHEEK_BV, INSTALLATIE_REGISTER]

// The verblijfsobject an LVG question asks about unless the user names another.
export const DEFAULT_VBO_ID = '0632010000099412'

export function isLvgScope(scope?: string): boolean {
  return !!scope && scope.startsWith('lvg:')
}

export function consumerFor(scope?: string): Consumer {
  return isLvgScope(scope) ? INSTALLATIE_REGISTER : HYPOTHEEK_BV
}

// Which consumer ran a trace, from its services: the backend, or the bron it
// reached. Undefined until a span of either arrives.
export function consumerForServices(services: string[]): Consumer | undefined {
  return CONSUMERS.find((c) =>
    services.some((svc) => svc === c.backendSvc || svc === c.bron.gatewaySvc || svc === c.bron.bronSvc),
  )
}
