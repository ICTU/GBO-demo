package mapping

import (
	"encoding/json"
	"testing"
	"testing/fstest"

	ftvgraphql "gbo-demo/ftv-graphql-mapper"

	"gitlab.com/digilab.overheid.nl/ecosystem/ftv/open-ftv/eam/models"
)

const ftvSDL = `scalar BSN
type Query { persoon(bsn: BSN!, jaar: Int): Persoon }
type Persoon { naam: String }`

func ftvTestCatalog(t *testing.T) *ftvgraphql.Catalog {
	t.Helper()
	catalog, err := ftvgraphql.LoadCatalog(fstest.MapFS{
		ftvgraphql.ManifestFile: {Data: []byte(`{ "services": { "bri": { "path": "/graphql", "schema": "bri.graphql" } } }`)},
		"bri.graphql":           {Data: []byte(ftvSDL)},
	})
	if err != nil {
		t.Fatal(err)
	}
	return catalog
}

// ftvParc is a PARC as OpenFTV builds it from the Inway's AuthZEN request.
func ftvParc(service, body string, headers map[string]any) *models.PARC {
	return &models.PARC{
		Principal: models.NewEntity("identity", "99999999900000000300",
			models.NewAttributeSet(models.NewAttribute(attrServiceName, service))),
		Action: models.NewEntity("name", "POST",
			models.NewAttributeSet(models.NewAttribute(models.AttrBody, body))),
		Resource: models.NewEntity("uri", "/graphql",
			models.NewAttributeSet(models.NewAttribute("path", "/graphql")), "parent-1"),
		Context: models.NewAttributeSet(models.NewAttribute(models.AttrHeaders, headers)),
	}
}

func graphqlAttr(t *testing.T, parc *models.PARC) map[string]any {
	t.Helper()
	v, ok := parc.Resource.Attributes().GetAttributeValue(AttrGraphQL).(map[string]any)
	if !ok {
		t.Fatalf("resource has no %q attribute", AttrGraphQL)
	}
	return v
}

func TestFTVGraphQLAddsTheFieldListToTheResource(t *testing.T) {
	in := ftvParc("bri", `{"query":"query ($j: Int) { persoon(bsn: \"1\", jaar: $j) { naam } }","variables":{"j":2024}}`,
		map[string]any{"Content-Type": "application/json"})
	out := mapFTVGraphQL(in, ftvTestCatalog(t))

	got := graphqlAttr(t, out)
	if got["profile"] != ftvgraphql.Profile || got["unverifiable"] != nil {
		t.Fatalf("output = %v", got)
	}
	fields, _ := got["fields"].([]any)
	if len(fields) != 2 {
		t.Fatalf("fields = %v", fields)
	}
	jaar := fields[0].(map[string]any)["args"].(map[string]any)["jaar"].(map[string]any)["value"]
	if jaar != json.Number("2024") {
		t.Errorf("jaar = %#v, want json.Number 2024", jaar)
	}
	if schema, _ := got["schema"].(map[string]any); schema["digest"] == "" || schema["digest"] == nil {
		t.Errorf("schema = %v, want a digest", got["schema"])
	}

	// The rest of the resource is kept, and the incoming PARC is untouched.
	if out.Resource.Type() != "uri" || out.Resource.ID() != "/graphql" || len(out.Resource.Parents()) != 1 {
		t.Errorf("resource = %s %s %v", out.Resource.Type(), out.Resource.ID(), out.Resource.Parents())
	}
	if out.Resource.Attributes().GetAttributeValue("path") != "/graphql" {
		t.Error("resource attribute path lost")
	}
	if in.Resource.Attributes().GetAttribute(AttrGraphQL) != nil {
		t.Error("the incoming resource was changed")
	}
	if out.Principal != in.Principal || out.Action != in.Action || out.Context != in.Context {
		t.Error("principal, action or context replaced")
	}
}

func TestFTVGraphQLFailsClosed(t *testing.T) {
	catalog := ftvTestCatalog(t)
	query := `{"query":"{ persoon(bsn: \"1\") { naam } }"}`
	jsonType := map[string]any{"Content-Type": "application/json"}
	for name, c := range map[string]struct {
		parc    *models.PARC
		catalog *ftvgraphql.Catalog
		code    string
		subcode string
	}{
		"unknown service":       {ftvParc("lvg", query, jsonType), catalog, ftvgraphql.CodeConfigError, ftvgraphql.SubSchemaUnavailable},
		"no service name":       {ftvParc("", query, jsonType), catalog, ftvgraphql.CodeConfigError, ftvgraphql.SubSchemaUnavailable},
		"catalog not loaded":    {ftvParc("bri", query, jsonType), nil, ftvgraphql.CodeConfigError, ftvgraphql.SubSchemaUnavailable},
		"no headers":            {ftvParc("bri", query, nil), catalog, ftvgraphql.CodeCoverageUnverifiable, ftvgraphql.SubUnsupportedTransport},
		"header not a string":   {ftvParc("bri", query, map[string]any{"Content-Type": []any{"application/json"}}), catalog, ftvgraphql.CodeCoverageUnverifiable, ftvgraphql.SubUnsupportedTransport},
		"not a GraphQL request": {ftvParc("bri", `{"hello":"world"}`, jsonType), catalog, ftvgraphql.CodeCoverageUnverifiable, ftvgraphql.SubInvalidBody},
	} {
		t.Run(name, func(t *testing.T) {
			u, _ := graphqlAttr(t, mapFTVGraphQL(c.parc, c.catalog))["unverifiable"].(map[string]any)
			if u["code"] != c.code || (c.subcode != "" && u["subcode"] != c.subcode) {
				t.Errorf("unverifiable = %v, want %s/%s", u, c.code, c.subcode)
			}
		})
	}
}

// OpenFTV may hand the headers over as map[string]string.
func TestFTVGraphQLReadsStringHeaders(t *testing.T) {
	parc := ftvParc("bri", `{"query":"{ persoon(bsn: \"1\") { naam } }"}`, nil)
	parc.Context = models.NewAttributeSet(models.NewAttribute(models.AttrHeaders, map[string]string{"Content-Type": "application/json"}))
	if u := graphqlAttr(t, mapFTVGraphQL(parc, ftvTestCatalog(t)))["unverifiable"]; u != nil {
		t.Errorf("unverifiable = %v", u)
	}
}

// Contract autosigning goes through the same PDP and is no HTTP request.
func TestFTVGraphQLLeavesOtherRequestsAlone(t *testing.T) {
	parc := &models.PARC{
		Principal: models.NewEntity("peer_id", "99999999900000000200", nil),
		Action:    models.NewEntity("name", "autosign_contract", nil),
		Resource:  models.NewEntity("contract", "pdp-admission-readiness", nil),
		Context:   models.NewAttributeSet(),
	}
	if out := mapFTVGraphQL(parc, ftvTestCatalog(t)); out != parc {
		t.Error("a non-HTTP request was mapped")
	}
}
