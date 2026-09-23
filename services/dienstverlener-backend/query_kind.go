package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// queryKind is what one consumer asks its source. The same backend image runs
// for Hypotheek-BV (income data from the Belastingdienst) and for the
// Installatie Register (ownership check at LVG); everything else — the
// consent token, FSC, the denial handling, the logbook — is shared.
type queryKind struct {
	// defaultScope is sent as X-GBO-Scope when the request names none.
	defaultScope string
	// activity and recordName describe the call in the consumer's own
	// Logboek Dataverwerkingen.
	activity   string
	recordName string
	// build turns the request and the consent's claims into the GraphQL
	// request for the source. A request error is the caller's (HTTP 400).
	build func(req queryRequest, consent consentTokenPayload, fromDevPortal bool) (builtQuery, error)
}

type builtQuery struct {
	query     string
	variables map[string]any
	// deniedYears are requested belastingjaren the consent does not cover.
	deniedYears []int
}

var queryKinds = map[string]queryKind{
	"bd": {
		defaultScope: "bd:ib:2025",
		activity:     "https://logboek.hypotheek-bv.test/verwerkingsactiviteiten/hbv-inkomensgegevens-opvragen/v1",
		recordName:   "dataverwerking.inkomensgegevens-opvragen",
		build:        buildIncomeQuery,
	},
	"lvg": {
		defaultScope: "lvg:vbo:eigendom",
		activity:     "https://logboek.installatieregister.test/verwerkingsactiviteiten/ir-eigendom-controleren/v1",
		recordName:   "dataverwerking.eigendom-controleren",
		build:        buildOwnershipQuery,
	},
}

func lookupQueryKind(name string) (queryKind, error) {
	kind, ok := queryKinds[name]
	if !ok {
		return queryKind{}, fmt.Errorf("unknown QUERY_KIND %q", name)
	}
	return kind, nil
}

// buildIncomeQuery is Hypotheek-BV's question. Per-year consent has two
// consumer profiles:
//   - Browser flow (dienstverlener-mock): intersect requested years with the
//     consent's scopes and only query the covered ones, so the citizen sees
//     exactly the years they consented to and the rest comes back as
//     denied_years (greyed out in the UI).
//   - Dev-portal (X-Demo-Source): send the query exactly as requested — the
//     portal exists to demonstrate raw policy outcomes, so a year outside the
//     consent must produce the policy deny (YEAR_NOT_COVERED) with a full
//     trace, not a client-side pre-filter.
func buildIncomeQuery(req queryRequest, consent consentTokenPayload, fromDevPortal bool) (builtQuery, error) {
	jaren := req.Belastingjaren
	if len(jaren) == 0 {
		jaren = []int{2024, 2025}
	}
	queryable := jaren
	var deniedYears []int
	if !fromDevPortal {
		queryable, deniedYears = intersectYears(jaren, consentedYears(consent.Scopes))
	}
	// Even a zero-overlap request reaches the PDP: otherwise token
	// verification and online revocation could be bypassed locally.
	if len(queryable) == 0 {
		queryable = jaren
	}
	return builtQuery{
		query:       buildQuery(queryable, req.Fields),
		variables:   map[string]any{"bsn": consent.PI},
		deniedYears: deniedYears,
	}, nil
}

// buildOwnershipQuery is the Installatie Register's question: does the
// citizen of this consent own this verblijfsobject? LVG answers with the same
// VBO-id or null. The PI goes in $bsn like every DvTP query; lvg-sidecar
// resolves it.
func buildOwnershipQuery(req queryRequest, consent consentTokenPayload, _ bool) (builtQuery, error) {
	vboID := strings.TrimSpace(req.VboID)
	if vboID == "" {
		return builtQuery{}, errors.New("vbo_id is required")
	}
	return builtQuery{
		query:     `query($bsn: BSN!, $vboId: String!) { vbo(bsn: $bsn, vboId: $vboId) { vboId } }`,
		variables: map[string]any{"bsn": consent.PI, "vboId": vboID},
	}, nil
}

// consentedYears extracts the belastingjaren covered by granted scopes of
// the form bd:ib:<year>.
func consentedYears(scopes []string) []int {
	var years []int
	for _, s := range scopes {
		rest, ok := strings.CutPrefix(s, "bd:ib:")
		if !ok {
			continue
		}
		if y, err := strconv.Atoi(rest); err == nil {
			years = append(years, y)
		}
	}
	return years
}

// intersectYears splits the requested years into the ones covered by the
// consent (queryable) and the rest (denied).
func intersectYears(requested, consented []int) (allowed, denied []int) {
	set := make(map[int]bool, len(consented))
	for _, y := range consented {
		set[y] = true
	}
	for _, y := range requested {
		if set[y] {
			allowed = append(allowed, y)
		} else {
			denied = append(denied, y)
		}
	}
	return allowed, denied
}

// buildQuery renders the GraphQL query against the BD bron-schema. The
// query uses `bsn` as its argument, but the actual value passed in the
// variable is a PI. This matches the EUDI shape exactly. The sidecar at
// the source resolves PI→BSN (subject_id_type=pseudonym), so the source
// always sees a BSN. The `$bsn` variable name is kept explicit so the PDP
// AST-parser picks it up as the bsn argument.
//
// The belastingjaren filter travels INSIDE the query: the bron returns
// all aangiften for a person, so per-year consent is only enforceable by
// policy when the PDP can see the requested years (rule DVT0001's
// years_in_scopes check). Default = the two most recent years.
//
// `fields` is an optional field-selection; empty = default set of 5
// fields. Bedrag-fields (verzamelinkomen, box*Inkomen) only exist on the
// concrete AangifteIH type, so they are wrapped in an inline fragment
// with their scalar leaves selected. Scenarios that want to test
// out-of-scope fields (e.g. box2Inkomen) set fields explicitly.
func buildQuery(jaren []int, fields []string) string {
	if len(jaren) == 0 {
		jaren = []int{2024, 2025}
	}
	if len(fields) == 0 {
		fields = []string{"belastingjaar", "verzamelinkomen", "box1Inkomen", "status", "indieningsdatum"}
	}
	var plain, bedragen []string
	for _, f := range fields {
		if bedragFields[f] {
			bedragen = append(bedragen, f+" { waarde valuta }")
		} else {
			plain = append(plain, f)
		}
	}
	selection := strings.Join(plain, " ")
	if len(bedragen) > 0 {
		selection += " ... on AangifteIH { " + strings.Join(bedragen, " ") + " }"
	}
	jarenJSON, _ := json.Marshal(jaren)
	return fmt.Sprintf(`query($bsn: BSN!) { ingeschrevenPersoon(bsn: $bsn) { heeftBelastingjaarAangifte(belastingjaren: %s) { %s } } }`,
		string(jarenJSON), selection)
}

// bedragFields are the AangifteIH fields of type Bedrag in the BD schema.
var bedragFields = map[string]bool{
	"verzamelinkomen": true,
	"box1Inkomen":     true,
	"box2Inkomen":     true,
	"box3Inkomen":     true,
}
