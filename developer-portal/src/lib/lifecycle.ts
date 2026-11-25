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
