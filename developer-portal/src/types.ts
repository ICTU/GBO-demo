// Domain types shared across multiple components.

export type Tab = 'issuance' | 'use' | 'eudi-issuance'
export type Outcome = 'allow' | 'deny' | 'error' | 'unknown'

export type Scenario = {
  id: string
  name: string
  desc: string
  tab: Tab
  expected_outcome?: Outcome
  user_saved: boolean
  payload: IssuancePayload | UsePayload | EudiPayload
}

export type IssuancePayload = {
  citizen_bsn: string
  dienstverlener_oin: string
  scopes: string[]
  validity_seconds?: number
  use_case?: string
}

export type UsePayload = {
  consent_id: string
  scope_id?: string
  belastingjaren?: number[]
  // Optional field-selection for the auto-generated query. Default = full set.
  // Used by scenarios that test out-of-scope fields (e.g. box2Inkomen).
  fields?: string[]
}

// EUDI-issuance is externally triggered (wallet scans a QR), so the payload
// captures what the user selected in the dev-portal to launch the run — not
// what the wallet later sends. The BSN comes exclusively from the wallet-PID;
// here we only capture the usecase-selection. The usecase-key maps to both
// the disclosure_settings-key in issuance-server-config and the path in the
// adapter-catalog. The same attestation_type can back multiple usecases
// (e.g. one per tax-year).
export type EudiPayload = {
  usecase: string
}

export type ApiCall = {
  id: string
  label: string
  method: string
  url: string
  status: number
  request_body?: unknown
  response_body?: unknown
  duration_ms?: number
}

// Issuance-response from consent-portal-backend
export type IssuanceResponse = {
  consent_id: string
  pseudonym: string
  pi: string
  trace_id?: string
  api_calls?: ApiCall[]
}

// Use-response from dienstverlener-backend
export type UseResponse = {
  allowed: boolean
  data?: { data?: { ingeschrevenPersoon?: { heeftBelastingjaarAangifte?: unknown[] } } }
  reason?: string
  trace_id: string
}

// Run-log entry for the history table
export type HistoryRun = {
  run_id: string
  scenario_name: string
  tab: Tab
  payload: IssuancePayload | UsePayload | EudiPayload
  trace_id: string
  outcome: Outcome
  ts: string
  consent_id?: string
  // Bubble-POSTed by burger-FE backends so dev-portal can render the response
  // in its result-panel when watching a flow. Self-triggered runs leave it
  // empty (the FE already has the body locally).
  response?: IssuanceResponse | UseResponse
}

export type Citizen = {
  bsn: string
  heeftBelastingjaarAangifte?: unknown[]
}

export type Organization = {
  oin: string
  name?: string
  sector?: string
}
