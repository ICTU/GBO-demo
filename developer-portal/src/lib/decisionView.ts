// Reads the policy's response document from the OPA console decision log
// into what the decision view renders. Three shapes occur:
//
//   profile — a deny carries the FTV GraphQL profile's decision context:
//             graphql.client (what the consumer was told) and graphql.admin
//             (the request's code, the mapper's message, the schema digest
//             and the denied fields with each rule's trace).
//   gbo     — decisions logged before that: context.denied_fields and
//             reason_admin.code.
//   legacy  — the oldest traces: reason_admin.evaluated as one cascade.
//
// An allow has context.granted in every shape.

export type Step = {
  code: string
  label: string
  expected: string
  status: 'pass' | 'fail' | 'skipped'
}

export type RuleOutcome = { rule: string; code?: string; steps?: Step[] }

export type FieldOutcome = {
  path: string
  key?: string
  code?: string
  rules: RuleOutcome[]
}

export type DecisionView = {
  shape: 'profile' | 'gbo' | 'legacy' | 'none'
  allowed: boolean
  granted?: FieldOutcome[]
  denied?: FieldOutcome[]
  // The text the consumer was given, e.g. "COVERAGE_UNVERIFIABLE PARSE_ERROR".
  consumerCode?: string
  // The request's own code, unhidden, e.g. "COVERAGE_UNVERIFIABLE INVALID_QUERY".
  adminCode?: string
  mapperMessage?: string
  schemaDigest?: string
  legacyEvaluated?: { code: string; status: Step['status'] }[]
}

type Obj = Record<string, unknown>

const isObj = (v: unknown): v is Obj => typeof v === 'object' && v !== null && !Array.isArray(v)
const str = (v: unknown): string | undefined => (typeof v === 'string' && v !== '' ? v : undefined)
const list = (v: unknown): unknown[] | undefined => (Array.isArray(v) ? v : undefined)

const codeWithSubcode = (o: Obj): string | undefined => {
  const code = str(o.code)
  if (!code) return undefined
  const sub = str(o.subcode)
  return sub ? `${code} ${sub}` : code
}

const rules = (v: unknown): RuleOutcome[] =>
  (list(v) ?? []).filter(isObj).map((r) => ({
    rule: str(r.rule) ?? '—',
    code: str(r.code),
    steps: list(r.steps) as Step[] | undefined,
  }))

const granted = (ctx: Obj): FieldOutcome[] | undefined =>
  list(ctx.granted)?.filter(isObj).map((g) => ({
    path: str(g.field) ?? '(geen veld)',
    key: str(g.key),
    rules: [{ rule: str(g.rule) ?? '—', steps: list(g.steps) as Step[] | undefined }],
  }))

export function readDecision(result: unknown): DecisionView {
  const doc = isObj(result) ? result : {}
  const ctx = isObj(doc.context) ? doc.context : {}
  const allowed = doc.decision === true
  const graphql = isObj(ctx.graphql) ? ctx.graphql : undefined

  if (graphql) {
    const admin = isObj(graphql.admin) ? graphql.admin : {}
    const client = isObj(graphql.client) ? graphql.client : {}
    const denied = list(admin.denied_fields)?.filter(isObj).map((d) => ({
      path: (list(d.path) ?? []).map(String).join('.'),
      key: str(d.key),
      code: str(d.code),
      rules: rules(d.trace),
    }))
    return {
      shape: 'profile',
      allowed,
      granted: granted(ctx),
      denied,
      consumerCode: codeWithSubcode(client),
      adminCode: codeWithSubcode(admin),
      mapperMessage: str(admin.message),
      schemaDigest: str(admin.schema_digest),
    }
  }

  const reasonAdmin = isObj(ctx.reason_admin) ? ctx.reason_admin : {}
  const deniedFields = list(ctx.denied_fields)
  const grantedFields = granted(ctx)
  if (deniedFields || grantedFields) {
    return {
      shape: 'gbo',
      allowed,
      granted: grantedFields,
      denied: deniedFields?.filter(isObj).map((d) => ({
        path: str(d.field) ?? '(geen veld)',
        key: str(d.key),
        code: str(d.code),
        rules: rules(d.evaluated),
      })),
      adminCode: str(reasonAdmin.code),
    }
  }

  const evaluated = list(reasonAdmin.evaluated)
  if (evaluated) {
    return {
      shape: 'legacy',
      allowed,
      adminCode: str(reasonAdmin.code),
      legacyEvaluated: evaluated.filter(isObj).map((e) => ({
        code: str(e.code) ?? '',
        status: (str(e.status) ?? 'skipped') as Step['status'],
      })),
    }
  }

  return { shape: 'none', allowed }
}
