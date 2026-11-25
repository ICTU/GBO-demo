/* The credential lifecycle block: the batch settings of the own credential
   issuer, per source (provider) and per attestation type (credential
   configuration), and a timeline of what the wallets did with them. The
   portal only passes the settings on; the issuer decides what is valid.
   The backend has no lifecycle routes unless the deployment turns them on;
   then the block is not shown. */

export type ReusePolicy = { method: string; batch_size: number; reissue_trigger_unused: number }

export type ProviderSettings = { name: string; batch_size: number; overridden: boolean }

export type ConfigurationSettings = {
  id: string
  provider: string
  name: string
  reuse_policy: ReusePolicy | null
  /** The batch size an EUDI wallet is held to: the policy's or the provider's. */
  batch_size: number
  overridden: boolean
}

export type LifecycleSettings =
  | { enabled: false }
  | { enabled: true; providers: ProviderSettings[]; configurations: ConfigurationSettings[]; error: string }

export type LifecycleEvent = {
  at: string
  kind: string
  provider: string
  profile: string
  session: string
  previous_session?: string
  flow?: string
  configurations?: string[]
  grant_type?: string
  wallet_provider?: string
  configuration?: string
  proof_type?: string
  keys_offered?: number
  batch_size?: number
  batch_size_from?: string
  credentials?: number
  key_fingerprints?: string[]
  error?: string
  error_description?: string
  notification?: string
}

/** A wallet session and the sessions its refreshes started, newest event last. */
export type SessionChain = {
  /** The labels of the sessions in this chain, first one first. */
  sessions: string[]
  provider: string
  profile: string
  events: LifecycleEvent[]
  last: string
}

const MESSAGES: Record<string, string> = {
  invalid_setting: 'De issuer weigert deze instelling.',
  invalid_request: 'Het verzoek is onvolledig.',
  unknown_provider: 'De issuer kent deze bron niet.',
  unknown_configuration: 'De issuer kent dit attestatietype niet.',
  issuer_unavailable: 'De credential issuer is niet bereikbaar. Draait het compose-profiel credential-issuer?',
  events_unavailable: 'De issuer kon de tijdlijn niet lezen.',
}

/** The message for a refusal: the issuer's own reason when it gives one. */
export function lifecycleMessage(status: number | null, body: unknown): string {
  if (status === null) return 'De portal-backend is niet bereikbaar.'
  const answer = (body ?? {}) as { error?: unknown; error_description?: unknown }
  const code = typeof answer.error === 'string' ? answer.error : `HTTP ${status}`
  const base = MESSAGES[code] ?? `Mislukt (${code}).`
  if (typeof answer.error_description !== 'string' || !answer.error_description) return base
  return `${base.replace(/\.$/, '')}: ${answer.error_description}`
}

/** The settings answer; status null is a request that got no answer. */
export function lifecycleSettingsFrom(status: number | null, body: unknown): LifecycleSettings {
  if (status === 404) return { enabled: false }
  const answer = (body ?? {}) as { providers?: unknown; configurations?: unknown }
  if (status === 200 && Array.isArray(answer.providers) && Array.isArray(answer.configurations)) {
    return {
      enabled: true, error: '',
      providers: answer.providers as ProviderSettings[],
      configurations: answer.configurations as ConfigurationSettings[],
    }
  }
  return { enabled: true, providers: [], configurations: [], error: lifecycleMessage(status, body) }
}

export function lifecycleEventsFrom(status: number | null, body: unknown): LifecycleEvent[] | { error: string } {
  const events = (body as { events?: unknown } | undefined)?.events
  if (status === 200 && Array.isArray(events)) return events as LifecycleEvent[]
  return { error: lifecycleMessage(status, body) }
}

/** Parses a whole number of at least min from a form field. */
export function wholeNumber(value: string, min: number): number | null {
  const n = Number(value)
  return value.trim() !== '' && Number.isInteger(n) && n >= min ? n : null
}

/** Groups events by wallet session; a refresh joins the chain of the
    session it continues. Chains with the latest event first. */
export function sessionChains(events: LifecycleEvent[]): SessionChain[] {
  const chainOf = new Map<string, SessionChain>()
  const chains: SessionChain[] = []
  for (const e of [...events].sort((a, b) => a.at.localeCompare(b.at))) {
    let chain = chainOf.get(e.session)
    if (!chain && e.previous_session) chain = chainOf.get(e.previous_session)
    if (!chain) {
      chain = { sessions: [], provider: e.provider, profile: e.profile, events: [], last: e.at }
      chains.push(chain)
    }
    if (!chain.sessions.includes(e.session)) {
      chain.sessions.push(e.session)
      chainOf.set(e.session, chain)
    }
    chain.events.push(e)
    chain.last = e.at
  }
  return chains.sort((a, b) => b.last.localeCompare(a.last))
}

const KIND_LABELS: Record<string, string> = {
  session_started: 'sessie gestart',
  token_issued: 'token',
  token_refused: 'token geweigerd',
  credential_issued: 'uitgegeven',
  credential_deferred: 'uitgesteld',
  credential_refused: 'geweigerd',
  notification_received: 'notificatie',
}

export function kindLabel(kind: string): string {
  return KIND_LABELS[kind] ?? kind
}

const GRANTS: Record<string, string> = {
  'urn:ietf:params:oauth:grant-type:pre-authorized_code': 'pre-authorized code',
  authorization_code: 'authorization code (login)',
  refresh_token: 'refresh token',
}

const PROOFS: Record<string, string> = {
  attestation: 'attestation proof (key attestation)',
  jwt: 'jwt proof',
  'jwt+key_attestation': 'jwt proof met key attestation',
}

function batchText(e: LifecycleEvent): string {
  if (!e.batch_size) return ''
  return `batch size ${e.batch_size} (${e.batch_size_from === 'reuse_policy' ? 'beleid van het type' : 'bron'})`
}

/** What an event says, in one line. */
export function describe(e: LifecycleEvent): string {
  switch (e.kind) {
    case 'session_started':
      return `${e.flow === 'authorization_code' ? 'Login' : 'Aanbod'} voor ${(e.configurations ?? []).join(', ')}`
    case 'token_issued':
      return [GRANTS[e.grant_type ?? ''] ?? e.grant_type, e.wallet_provider && `wallet provider ${e.wallet_provider}`]
        .filter(Boolean).join(' · ')
    case 'credential_issued':
      return [`${e.credentials ?? 0} ${e.credentials === 1 ? 'exemplaar' : 'exemplaren'} van ${e.configuration}`,
        `${e.keys_offered ?? 0} sleutels aangeboden`, batchText(e), PROOFS[e.proof_type ?? ''] ?? e.proof_type]
        .filter(Boolean).join(' · ')
    case 'token_refused':
      return [GRANTS[e.grant_type ?? ''] ?? e.grant_type, `${e.error}${e.error_description ? `: ${e.error_description}` : ''}`]
        .filter(Boolean).join(' · ')
    case 'credential_deferred':
      return [`${e.configuration}: de bron antwoordde nog niet`, `${e.keys_offered ?? 0} sleutels aangeboden`]
        .join(' · ')
    case 'credential_refused':
      return [`${e.error}${e.error_description ? `: ${e.error_description}` : ''}`,
        e.keys_offered ? `${e.keys_offered} sleutels aangeboden` : '', batchText(e)]
        .filter(Boolean).join(' · ')
    case 'notification_received':
      return e.notification ?? ''
    default:
      return ''
  }
}

/* ── Test relying party (#737) ───────────────────────────────────────────
   Two fictional relying parties ask a wallet for an attestation of the own
   issuer. Each presentation names the copy by the fingerprint of its
   holder key, so the block can put it next to the issuance it came from. */

export type RPParty = { id: string; name: string; client_id: string }

export type RPCredential = { id: string; name: string; vct: string; claims: string[][] }

export type RPCatalog =
  | { enabled: false }
  | { enabled: true; parties: RPParty[]; credentials: RPCredential[]; error: string }

export type RPEvent = {
  at: string
  kind: 'presented' | 'declined' | 'rejected' | string
  session: string
  relying_party: string
  relying_party_name: string
  configuration: string
  vct: string
  claims: string[]
  key_fingerprint?: string
  status?: string
  error?: string
  error_description?: string
}

export type RPRequest = { link: string; expires_at: string; session: string } | { error: string }

/** Where an open request stands: a request is answered once, and expires. */
export type RequestState = { state: 'open' } | { state: 'answered'; event: RPEvent } | { state: 'expired' }

/** Where a presented copy came from: which issuance, which copy; or how
    many issuances it could have come from, when more than one. */
export type CopyOrigin = { chain: SessionChain; copy: number; of: number } | { ambiguous: number }

const RP_MESSAGES: Record<string, string> = {
  unknown_relying_party: 'De testpartij bestaat niet (meer).',
  unknown_credential: 'De testpartij is niet geregistreerd voor dit attestatietype. Draai make provision-test-relying-parties opnieuw.',
  invalid_claims: 'Kies een of meer claims, elk één keer.',
}

export function rpCatalogFrom(status: number | null, body: unknown): RPCatalog {
  if (status === 404) return { enabled: false }
  const answer = (body ?? {}) as { relying_parties?: unknown; credentials?: unknown }
  if (status === 200 && Array.isArray(answer.relying_parties) && Array.isArray(answer.credentials)) {
    return { enabled: true, error: '', parties: answer.relying_parties as RPParty[], credentials: answer.credentials as RPCredential[] }
  }
  return { enabled: true, parties: [], credentials: [], error: lifecycleMessage(status, body) }
}

export function rpRequestFrom(status: number | null, body: unknown): RPRequest {
  const answer = (body ?? {}) as { link?: unknown; expires_at?: unknown; error?: unknown }
  const session = (body as { session?: unknown } | undefined)?.session
  if (status === 201 && typeof answer.link === 'string' && typeof answer.expires_at === 'string' && typeof session === 'string') {
    return { link: answer.link, expires_at: answer.expires_at, session }
  }
  if (typeof answer.error === 'string' && RP_MESSAGES[answer.error]) return { error: RP_MESSAGES[answer.error] }
  return { error: lifecycleMessage(status, body) }
}

export function rpEventsFrom(status: number | null, body: unknown): RPEvent[] | { error: string } {
  const events = (body as { events?: unknown } | undefined)?.events
  if (status === 200 && Array.isArray(events)) return events as RPEvent[]
  return { error: lifecycleMessage(status, body) }
}

/** The issuance a presented copy came from: one of the same attestation
    type (VCT), issued before the presentation, with the copy's key
    fingerprint. The relying party verifies the VCT, not the configuration:
    two configurations of one type (two tax years) both answer a request
    for either. vctOf gives the VCT of an issuer configuration; without
    one, the configurations must be equal. A wallet may use a key again in a
    later issuance, so more than one candidate is ambiguous, not a guess. */
export function copyOrigin(
  chains: SessionChain[],
  presented: Pick<RPEvent, 'key_fingerprint' | 'configuration' | 'vct' | 'at'>,
  vctOf: (configuration: string) => string | undefined = () => undefined,
): CopyOrigin | null {
  const fp = presented.key_fingerprint
  if (!fp) return null
  const sameType = (configuration: string | undefined) => {
    const vct = configuration ? vctOf(configuration) : undefined
    return vct !== undefined ? vct === presented.vct : configuration === presented.configuration
  }
  const found: { chain: SessionChain; copy: number; of: number }[] = []
  for (const chain of chains) {
    for (const e of chain.events) {
      const i = (e.key_fingerprints ?? []).indexOf(fp)
      if (e.kind === 'credential_issued' && i >= 0 && sameType(e.configuration) && e.at <= presented.at) {
        found.push({ chain, copy: i + 1, of: e.key_fingerprints!.length })
      }
    }
  }
  if (found.length > 1) return { ambiguous: found.length }
  return found[0] ?? null
}

/** The chain a presentation belongs under, if it has exactly one origin. */
export function originChain(origin: CopyOrigin | null): SessionChain | null {
  return origin && 'chain' in origin ? origin.chain : null
}

const STATUS_LABELS: Record<string, string> = { valid: 'geldig', revoked: 'ingetrokken', expired: 'verlopen' }

/** What a presentation event says, in one line. */
export function describeRP(e: RPEvent, origin: CopyOrigin | null): string {
  const what = `${e.configuration} (${e.claims.join(', ')})`
  switch (e.kind) {
    case 'presented':
      return [`getoond aan ${e.relying_party_name}: ${what}`,
        !origin ? 'uitgifte niet in de tijdlijn'
          : 'ambiguous' in origin ? `exemplaar in ${origin.ambiguous} uitgiftes, niet eenduidig`
            : `exemplaar ${origin.copy} van ${origin.of}`,
        STATUS_LABELS[e.status ?? ''] ?? e.status].filter(Boolean).join(' · ')
    case 'declined':
      return `geweigerd door de wallet voor ${e.relying_party_name}: ${what}${e.error ? ` (${e.error})` : ''}`
    case 'rejected':
      return `afgewezen door ${e.relying_party_name}: ${e.error_description ?? e.error ?? ''}`
    default:
      return ''
  }
}

/** Whether a request was answered (its event is in the timeline) or has
    expired; the form then takes its link away. */
export function requestState(req: { session: string; expires_at: string }, events: RPEvent[], now: Date): RequestState {
  const event = events.find((e) => e.session === req.session)
  if (event) return { state: 'answered', event }
  if (now.getTime() >= new Date(req.expires_at).getTime()) return { state: 'expired' }
  return { state: 'open' }
}

/** The claim paths as the form shows them. */
export function claimLabel(path: string[]): string {
  return path.join('.')
}
