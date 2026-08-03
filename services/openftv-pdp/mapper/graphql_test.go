package mapping

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"gitlab.com/digilab.overheid.nl/ecosystem/ftv/open-ftv/eam/models"
)

// ── helpers ────────────────────────────────────────────────────────────────

const testSDL = `
type Query {
  ingeschrevenPersoon(bsn: String!): IngeschrevenPersoon
}

interface IngeschrevenPersoon {
  bsn: String
  geslachtsnaam: String
  heeftHuwelijk: [Huwelijk!]
  heeftOuder: [NatuurlijkPersoon!]
}

type Ingezetene implements IngeschrevenPersoon {
  bsn: String
  geslachtsnaam: String
  heeftHuwelijk: [Huwelijk!]
  heeftOuder: [NatuurlijkPersoon!]
}

type Huwelijk {
  soortVerbintenis: String
  partners: [NatuurlijkPersoon!]
}

type NatuurlijkPersoon {
  geslachtsnaam: String
  geboortedatum: String
  heeftOuder: [NatuurlijkPersoon!]
  heeftHuwelijk: [Huwelijk!]
}
`

// writeSDL points the mapper at a throwaway policy dir and resets the
// once-cache so each test gets a clean load.
func writeSDL(t *testing.T, sdl string) {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "schema.graphql"), []byte(sdl), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GBO_SCHEMA_DIR", dir)
	resetFieldMap(t)
}

func resetFieldMap(t *testing.T) {
	t.Helper()
	fieldMapOnce = sync.Once{}
	fieldMapData = nil
	t.Cleanup(func() {
		fieldMapOnce = sync.Once{}
		fieldMapData = nil
	})
}

// parcFor builds the PARC shape FSC-Inway hands the PDP.
func parcFor(body string, headers map[string]any) *models.PARC {
	action := models.NewEntity("action", "POST", models.NewAttributeSet(nil))
	action.Attributes().AddAttributeKV(models.AttrBody, body)

	ctx := models.NewAttributeSet(nil)
	ctx.AddAttributeKV(models.AttrHeaders, headers)

	return &models.PARC{
		Principal: models.NewEntity("principal", "peer", models.NewAttributeSet(nil)),
		Action:    action,
		Resource:  models.NewEntity("resource", "bri", models.NewAttributeSet(nil)),
		Context:   ctx,
	}
}

func gqlBody(t *testing.T, query, opName string, vars map[string]any) string {
	t.Helper()
	b, err := json.Marshal(map[string]any{"query": query, "operationName": opName, "variables": vars})
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// tokenWithFlow builds an unsigned JWT carrying the `add.flow` claim, the
// way FSC-Manager signs it. The mapper reads it without verifying —
// FSC-Inway validated the signature before invoking the PDP.
func tokenWithFlow(t *testing.T, flow string) string {
	t.Helper()
	payload, err := json.Marshal(map[string]any{"add": map[string]any{"flow": flow}})
	if err != nil {
		t.Fatal(err)
	}
	return "Bearer header." + base64.RawURLEncoding.EncodeToString(payload) + ".sig"
}

func resolvedOf(t *testing.T, parc *models.PARC) map[string]any {
	t.Helper()
	v, ok := parc.Context.GetAttributeValue("resolved").(map[string]any)
	if !ok {
		t.Fatalf("no resolved attribute in context")
	}
	return v
}

func fieldIDs(res map[string]any) []string {
	fields, _ := res["fields"].([]map[string]any)
	out := make([]string, 0, len(fields))
	for _, f := range fields {
		out = append(out, f["id"].(string))
	}
	return out
}

func fieldByID(res map[string]any, id string) map[string]any {
	fields, _ := res["fields"].([]map[string]any)
	for _, f := range fields {
		if f["id"] == id {
			return f
		}
	}
	return nil
}

// ── field map from the SDL (D1) ────────────────────────────────────────────

func TestFieldMapFromSDL(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "s.graphql"), []byte(testSDL), 0o600); err != nil {
		t.Fatal(err)
	}
	m := loadFieldMap(dir)

	for key, want := range map[string]string{
		"Query.ingeschrevenPersoon":         "IngeschrevenPersoon",
		"Huwelijk.partners":                 "NatuurlijkPersoon",
		"NatuurlijkPersoon.heeftOuder":      "NatuurlijkPersoon",
		"IngeschrevenPersoon.heeftHuwelijk": "Huwelijk",
		"Ingezetene.heeftOuder":             "NatuurlijkPersoon",
		"NatuurlijkPersoon.heeftHuwelijk":   "Huwelijk",
	} {
		if got := m[key]; got != want {
			t.Errorf("fieldMap[%q] = %q, want %q", key, got, want)
		}
	}
	// Scalars carry no entry: only object-typed fields need a parent type.
	if _, ok := m["NatuurlijkPersoon.geslachtsnaam"]; ok {
		t.Error("scalar field should not appear in the field map")
	}
}

func TestFieldMapMissingDirIsEmptyNotFatal(t *testing.T) {
	if m := loadFieldMap(filepath.Join(t.TempDir(), "does-not-exist")); len(m) != 0 {
		t.Errorf("expected empty map, got %v", m)
	}
}

func TestFieldMapInvalidSDLIsSkipped(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "bad.graphql"), []byte("type Query {"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "good.graphql"), []byte(testSDL), 0o600); err != nil {
		t.Fatal(err)
	}
	m := loadFieldMap(dir)
	if m["Huwelijk.partners"] != "NatuurlijkPersoon" {
		t.Error("a broken SDL must not stop the valid ones from loading")
	}
}

// Without a field map every nested field lands under parent "?", which the
// closed-world policy has no rule for — fail closed, not fail open.
func TestUnknownParentTypeIsQuestionMark(t *testing.T) {
	writeSDL(t, "type Query { unrelated: String }")

	parc := GraphQLToContext(parcFor(
		gqlBody(t, `{ ingeschrevenPersoon(bsn: "PI-1") { geslachtsnaam } }`, "", nil),
		map[string]any{"Fsc-Authorization": tokenWithFlow(t, "dvtp:query")},
	))

	f := fieldByID(resolvedOf(t, parc), "Query.ingeschrevenPersoon.geslachtsnaam")
	if f == nil {
		t.Fatal("field not resolved")
	}
	if f["parent"] != "?" {
		t.Errorf("parent = %v, want \"?\"", f["parent"])
	}
}

// ── query walk ─────────────────────────────────────────────────────────────

func TestWalkResolvesTypeQualifiedFields(t *testing.T) {
	writeSDL(t, testSDL)

	parc := GraphQLToContext(parcFor(
		gqlBody(t, `{ ingeschrevenPersoon(bsn: "PI-1") { heeftHuwelijk { partners { geslachtsnaam } } } }`, "", nil),
		map[string]any{"Fsc-Authorization": tokenWithFlow(t, "dvtp:query")},
	))
	res := resolvedOf(t, parc)

	f := fieldByID(res, "Query.ingeschrevenPersoon.heeftHuwelijk.partners.geslachtsnaam")
	if f == nil {
		t.Fatalf("nested field missing; got %v", fieldIDs(res))
	}
	if f["parent"] != "NatuurlijkPersoon" {
		t.Errorf("parent = %v, want NatuurlijkPersoon", f["parent"])
	}
	if f["scalar"] != true {
		t.Errorf("leaf field should be scalar")
	}
	if edge := fieldByID(res, "Query.ingeschrevenPersoon.heeftHuwelijk"); edge["scalar"] != false {
		t.Errorf("field with a selection set should not be scalar")
	}
}

// The `known` axis is gone: without an SDL-derived notion of "the schema
// knew this field" a constant true was a check that always passed.
func TestResolvedFieldsCarryNoKnownAxis(t *testing.T) {
	writeSDL(t, testSDL)

	parc := GraphQLToContext(parcFor(
		gqlBody(t, `{ ingeschrevenPersoon(bsn: "PI-1") { geslachtsnaam } }`, "", nil),
		map[string]any{"Fsc-Authorization": tokenWithFlow(t, "dvtp:query")},
	))
	f := fieldByID(resolvedOf(t, parc), "Query.ingeschrevenPersoon")
	if _, ok := f["known"]; ok {
		t.Error("resolved fields should no longer carry a `known` axis")
	}
}

func TestWalkAliasUsesFieldNameNotAlias(t *testing.T) {
	writeSDL(t, testSDL)

	parc := GraphQLToContext(parcFor(
		gqlBody(t, `{ p: ingeschrevenPersoon(bsn: "PI-1") { naam: geslachtsnaam } }`, "", nil),
		map[string]any{"Fsc-Authorization": tokenWithFlow(t, "dvtp:query")},
	))
	res := resolvedOf(t, parc)
	if fieldByID(res, "Query.ingeschrevenPersoon.geslachtsnaam") == nil {
		t.Errorf("alias should not change the resolved field id; got %v", fieldIDs(res))
	}
}

func TestWalkFragmentSpreadAndInlineFragment(t *testing.T) {
	writeSDL(t, testSDL)

	q := `
	{ ingeschrevenPersoon(bsn: "PI-1") { ...persoon ... on Ingezetene { heeftOuder { geboortedatum } } } }
	fragment persoon on Ingezetene { geslachtsnaam }`

	res := resolvedOf(t, GraphQLToContext(parcFor(
		gqlBody(t, q, "", nil),
		map[string]any{"Fsc-Authorization": tokenWithFlow(t, "dvtp:query")},
	)))

	if res["coverage_unverifiable"] != false {
		t.Fatal("valid fragments should not mark coverage unverifiable")
	}
	f := fieldByID(res, "Query.ingeschrevenPersoon.geslachtsnaam")
	if f == nil || f["parent"] != "Ingezetene" {
		t.Errorf("fragment type condition should set the parent type, got %v", f)
	}
	if o := fieldByID(res, "Query.ingeschrevenPersoon.heeftOuder.geboortedatum"); o == nil || o["parent"] != "NatuurlijkPersoon" {
		t.Errorf("inline fragment field wrong: %v", o)
	}
}

func TestCoverageUnverifiable(t *testing.T) {
	writeSDL(t, testSDL)
	auth := map[string]any{"Fsc-Authorization": tokenWithFlow(t, "dvtp:query")}

	cases := []struct {
		name  string
		query string
	}{
		{"parse error", `{ ingeschrevenPersoon(bsn: }`},
		{"unknown fragment", `{ ingeschrevenPersoon(bsn: "x") { ...nope } }`},
		{"fragment cycle", `
			{ ingeschrevenPersoon(bsn: "x") { ...a } }
			fragment a on Ingezetene { heeftOuder { ...a } }`},
		{"empty query", ``},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res := resolvedOf(t, GraphQLToContext(parcFor(gqlBody(t, tc.query, "", nil), auth)))
			if res["coverage_unverifiable"] != true {
				t.Errorf("expected coverage_unverifiable for %s", tc.name)
			}
		})
	}
}

func TestDepthCapMarksUnverifiable(t *testing.T) {
	writeSDL(t, testSDL)

	// heeftOuder recurses on itself, so the query can nest past the cap.
	q := "{ ingeschrevenPersoon(bsn: \"x\") " + strings.Repeat("{ heeftOuder ", gqlMaxDepth+2) +
		"{ geboortedatum }" + strings.Repeat(" }", gqlMaxDepth+2)

	res := resolvedOf(t, GraphQLToContext(parcFor(
		gqlBody(t, q, "", nil),
		map[string]any{"Fsc-Authorization": tokenWithFlow(t, "dvtp:query")},
	)))
	if res["coverage_unverifiable"] != true {
		t.Error("nesting past the depth cap should mark coverage unverifiable")
	}
}

// ── operationName ──────────────────────────────────────────────────────────

func TestOperationNameSelectsTheExecutedOperation(t *testing.T) {
	writeSDL(t, testSDL)

	q := `
	query Benign { ingeschrevenPersoon(bsn: "x") { geslachtsnaam } }
	query Real { ingeschrevenPersoon(bsn: "x") { heeftHuwelijk { partners { geboortedatum } } } }`

	res := resolvedOf(t, GraphQLToContext(parcFor(
		gqlBody(t, q, "Real", nil),
		map[string]any{"Fsc-Authorization": tokenWithFlow(t, "dvtp:query")},
	)))

	if fieldByID(res, "Query.ingeschrevenPersoon.heeftHuwelijk.partners.geboortedatum") == nil {
		t.Errorf("named operation not walked; got %v", fieldIDs(res))
	}
	if fieldByID(res, "Query.ingeschrevenPersoon.geslachtsnaam") != nil {
		t.Error("walked the wrong operation — the unexecuted one leaked into coverage")
	}
}

func TestMultipleOperationsWithoutNameIsUnverifiable(t *testing.T) {
	writeSDL(t, testSDL)

	q := `
	query A { ingeschrevenPersoon(bsn: "x") { geslachtsnaam } }
	query B { ingeschrevenPersoon(bsn: "x") { heeftOuder { geboortedatum } } }`

	res := resolvedOf(t, GraphQLToContext(parcFor(
		gqlBody(t, q, "", nil),
		map[string]any{"Fsc-Authorization": tokenWithFlow(t, "dvtp:query")},
	)))
	if res["coverage_unverifiable"] != true {
		t.Error("ambiguous document should deny, not authorize the first operation")
	}
}

func TestUnmatchedOperationNameIsUnverifiable(t *testing.T) {
	writeSDL(t, testSDL)

	res := resolvedOf(t, GraphQLToContext(parcFor(
		gqlBody(t, `query A { ingeschrevenPersoon(bsn: "x") { geslachtsnaam } }`, "Missing", nil),
		map[string]any{"Fsc-Authorization": tokenWithFlow(t, "dvtp:query")},
	)))
	if res["coverage_unverifiable"] != true {
		t.Error("a named operation that is absent must not fall back to another")
	}
}

// ── arguments ──────────────────────────────────────────────────────────────

func TestArgsFlattening(t *testing.T) {
	writeSDL(t, testSDL)

	q := `query Q($jaren: [String!], $leeg: String) {
		ingeschrevenPersoon(bsn: "PI-1", filter: {jaar: 2025, tags: ["a", "b"]}, jaren: $jaren, leeg: $leeg) { geslachtsnaam }
	}`
	res := resolvedOf(t, GraphQLToContext(parcFor(
		gqlBody(t, q, "Q", map[string]any{"jaren": []any{"2024", "2025"}, "leeg": ""}),
		map[string]any{"Fsc-Authorization": tokenWithFlow(t, "dvtp:query")},
	)))

	args, _ := res["args"].(map[string]any)
	for key, want := range map[string]any{
		"bsn":           "PI-1",
		"filter.jaar":   "2025",
		"filter.tags.0": "a",
		"filter.tags.1": "b",
	} {
		if args[key] != want {
			t.Errorf("args[%q] = %v, want %v", key, args[key], want)
		}
	}
	if _, ok := args["leeg"]; ok {
		t.Error("an empty string variable counts as not supplied")
	}
	if _, ok := args["vars.jaren"]; !ok {
		t.Error("request variables should be mirrored under vars.*")
	}
}

// ── flow dispatch ──────────────────────────────────────────────────────────

func TestFlowComesFromSignedClaimOnly(t *testing.T) {
	writeSDL(t, testSDL)
	body := gqlBody(t, `{ ingeschrevenPersoon(bsn: "x") { geslachtsnaam } }`, "", nil)

	t.Run("from token claim", func(t *testing.T) {
		parc := GraphQLToContext(parcFor(body, map[string]any{
			"Fsc-Authorization": tokenWithFlow(t, "eudi:attestation:brp"),
		}))
		if got := parc.Context.GetAttributeValue("flow"); got != "eudi:attestation:brp" {
			t.Errorf("flow = %v", got)
		}
	})

	// The header let a caller name the regime it wanted to be judged
	// under. It is no longer read at all.
	t.Run("header is ignored", func(t *testing.T) {
		parc := GraphQLToContext(parcFor(body, map[string]any{"X-GBO-Flow": "eudi:attestation"}))
		if got := parc.Context.GetAttributeValue("flow"); got != "" {
			t.Errorf("flow = %v, want empty — the header must not be trusted", got)
		}
	})

	// No claim used to fall back to dvtp:query, silently selecting the
	// consent-based regime for a request that said nothing.
	t.Run("no claim does not default", func(t *testing.T) {
		parc := GraphQLToContext(parcFor(body, map[string]any{}))
		if got := parc.Context.GetAttributeValue("flow"); got != "" {
			t.Errorf("flow = %v, want empty", got)
		}
	})
}

func TestIsEUDIFlow(t *testing.T) {
	for flow, want := range map[string]bool{
		"eudi:attestation":     true,
		"eudi:attestation:brp": true, // the leak: exact match missed this
		"dvtp:query":           false,
		"":                     false,
	} {
		if got := isEUDIFlow(flow); got != want {
			t.Errorf("isEUDIFlow(%q) = %v, want %v", flow, got, want)
		}
	}
}

// ── BSN handling ───────────────────────────────────────────────────────────

// The wallet-disclosed BSN must not reach the policy engine, whose
// decision log is shipped to Loki. Regression for the akte-van-overlijden
// flow, which an exact flow match routed down the consent branch with the
// BSN untouched.
func TestBSNNeverReachesTheEngineForAnyEUDIFlow(t *testing.T) {
	writeSDL(t, testSDL)

	const bsn = "999991772"
	bsnk := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]string{"pi": "PI-resolved"})
	}))
	defer bsnk.Close()
	t.Setenv("GBO_BSNK_URL", bsnk.URL)

	for _, flow := range []string{"eudi:attestation", "eudi:attestation:brp"} {
		t.Run(flow, func(t *testing.T) {
			parc := GraphQLToContext(parcFor(
				gqlBody(t, `{ ingeschrevenPersoon(bsn: $bsn) { geslachtsnaam } }`, "", map[string]any{"bsn": bsn}),
				map[string]any{"Fsc-Authorization": tokenWithFlow(t, flow)},
			))

			dump, err := json.Marshal(map[string]any{
				"body":     parc.Action.Attributes().GetAttributeValue(models.AttrBody),
				"resource": parc.Context.GetAttributeValue("resource"),
				"resolved": parc.Context.GetAttributeValue("resolved"),
				"pip":      parc.Context.GetAttributeValue("pip"),
			})
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(string(dump), bsn) {
				t.Fatalf("BSN reached the engine input: %s", dump)
			}

			pip, _ := parc.Context.GetAttributeValue("pip").(map[string]any)
			pid, _ := pip["pid"].(map[string]any)
			if pid["pi"] != "PI-resolved" {
				t.Errorf("pip.pid.pi = %v, want the pseudonym", pid["pi"])
			}
		})
	}
}

// BSNk unreachable: scrub to "" so the policy denies PID_NOT_PRESENT.
// Passing the BSN through on failure would be the worst outcome.
func TestPseudonymiseFailureScrubsRatherThanPassesThrough(t *testing.T) {
	writeSDL(t, testSDL)

	const bsn = "999991772"
	down := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer down.Close()
	t.Setenv("GBO_BSNK_URL", down.URL)

	parc := GraphQLToContext(parcFor(
		gqlBody(t, `{ ingeschrevenPersoon(bsn: $bsn) { geslachtsnaam } }`, "", map[string]any{"bsn": bsn}),
		map[string]any{"Fsc-Authorization": tokenWithFlow(t, "eudi:attestation")},
	))

	pip, _ := parc.Context.GetAttributeValue("pip").(map[string]any)
	pid, _ := pip["pid"].(map[string]any)
	if pid["pi"] != "" {
		t.Errorf("pip.pid.pi = %v, want empty on pseudonymise failure", pid["pi"])
	}
	dump, _ := json.Marshal(parc.Context.GetAttributeValue("resource"))
	if strings.Contains(string(dump), bsn) {
		t.Errorf("BSN passed through on failure: %s", dump)
	}
}

// DvTP carries a PI, not a BSN, and needs no pseudonymisation — so no
// BSNk call is made. The consent lookup now happens in the policy.
func TestDvTPFlowSetsNoPIPAndDoesNotCallBSNk(t *testing.T) {
	writeSDL(t, testSDL)

	called := false
	bsnk := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		called = true
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer bsnk.Close()
	t.Setenv("GBO_BSNK_URL", bsnk.URL)

	parc := GraphQLToContext(parcFor(
		gqlBody(t, `{ ingeschrevenPersoon(bsn: $bsn) { geslachtsnaam } }`, "", map[string]any{"bsn": "PI-abc"}),
		map[string]any{"Fsc-Authorization": tokenWithFlow(t, "dvtp:query")},
	))

	if called {
		t.Error("DvTP must not call BSNk — it already carries a pseudonym")
	}
	if parc.Context.GetAttributeValue("pip") != nil {
		t.Error("the mapper no longer supplies consent; the policy retrieves it")
	}
}

// ── body decoding + context plumbing ───────────────────────────────────────

func TestDecodeGraphQLBodyAcceptsPlainAndBase64(t *testing.T) {
	raw := `{"query":"{ a }","operationName":"Op","variables":{"x":1}}`

	for name, body := range map[string]string{
		"plain":  raw,
		"base64": base64.StdEncoding.EncodeToString([]byte(raw)),
	} {
		t.Run(name, func(t *testing.T) {
			q, op, vars := decodeGraphQLBody(body)
			if q != "{ a }" || op != "Op" {
				t.Errorf("query=%q op=%q", q, op)
			}
			if vars["x"] != float64(1) {
				t.Errorf("variables not decoded: %v", vars)
			}
		})
	}
}

func TestNonGraphQLBodyIsUnverifiableNotAnError(t *testing.T) {
	writeSDL(t, testSDL)

	res := resolvedOf(t, GraphQLToContext(parcFor(
		"this is not json",
		map[string]any{"Fsc-Authorization": tokenWithFlow(t, "dvtp:query")},
	)))
	if res["coverage_unverifiable"] != true {
		t.Error("an undecodable body must fail closed")
	}
}

func TestMissingBodyLeavesPARCUntouched(t *testing.T) {
	parc := &models.PARC{
		Principal: models.NewEntity("principal", "peer", models.NewAttributeSet(nil)),
		Action:    models.NewEntity("action", "GET", models.NewAttributeSet(nil)),
		Resource:  models.NewEntity("resource", "bri", models.NewAttributeSet(nil)),
		Context:   models.NewAttributeSet(nil),
	}
	if got := GraphQLToContext(parc); got != parc {
		t.Error("a request without a body should pass through unchanged")
	}
}

func TestTraceIDAndScopePlumbing(t *testing.T) {
	writeSDL(t, testSDL)

	parc := GraphQLToContext(parcFor(
		gqlBody(t, `{ ingeschrevenPersoon(bsn: "x") { geslachtsnaam } }`, "", nil),
		map[string]any{
			"Fsc-Authorization":  tokenWithFlow(t, "dvtp:query"),
			"Fsc-Transaction-Id": "tx-123",
			"X-Gbo-Scope":        "bd:ib:2025",
		},
	))

	if got := parc.Context.GetAttributeValue("trace_id"); got != "tx-123" {
		t.Errorf("trace_id = %v", got)
	}
	fsc, _ := parc.Context.GetAttributeValue("fsc").(map[string]any)
	if fsc["transaction_id"] != "tx-123" {
		t.Errorf("fsc.transaction_id = %v", fsc["transaction_id"])
	}
	resource, _ := parc.Context.GetAttributeValue("resource").(map[string]any)
	if resource["scope"] != "bd:ib:2025" {
		t.Errorf("resource.scope = %v", resource["scope"])
	}
}

// Introspection fields are excluded from the data-field set by the engine,
// but the walk must still resolve them rather than choke.
func TestIntrospectionQueryWalksWithoutError(t *testing.T) {
	writeSDL(t, testSDL)

	res := resolvedOf(t, GraphQLToContext(parcFor(
		gqlBody(t, `{ __schema { types { name } } }`, "", nil),
		map[string]any{"Fsc-Authorization": tokenWithFlow(t, "dvtp:query")},
	)))
	if res["coverage_unverifiable"] != false {
		t.Error("introspection should parse cleanly")
	}
}
