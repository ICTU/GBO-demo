package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"testing"

	"github.com/getkin/kin-openapi/openapi3"
	"github.com/santhosh-tekuri/jsonschema/v6"

	asimapping "gbo-demo/asi-mapping"
	"gbo-demo/asi-provider/internal/accesstoken"
	"gbo-demo/asi-provider/internal/verification"
)

// Discovery records of slice 6a (#533): one dataService per attribute and
// data service, valid against the ETSI schema, pointing at the data
// service's endpoint and contract.

func get(t *testing.T, env testEnv, path string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	env.handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
	return rec
}

type records struct {
	DataServices []struct {
		AttributeIdentifier string `json:"attributeIdentifier"`
		EndpointDescription string `json:"endpointDescription"`
		EndpointURI         string `json:"endpointURI"`
		Provider            struct {
			LegalName   string          `json:"legalName"`
			Identifiers json.RawMessage `json:"identifiers"`
		} `json:"provider"`
		Country string `json:"country"`
	} `json:"dataServices"`
}

// dataServicesSchema compiles definitions/dataServices of the embedded ETSI
// schema.
func dataServicesSchema(t *testing.T) *jsonschema.Schema {
	t.Helper()
	doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(mustReadOpenAPIFile(dataserviceFile)))
	if err != nil {
		t.Fatal(err)
	}
	c := jsonschema.NewCompiler()
	if err := c.AddResource("dataservice.json", doc); err != nil {
		t.Fatal(err)
	}
	s, err := c.Compile("dataservice.json#/definitions/dataServices")
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// The records list the attributes of the source's current description, and
// its country (E1).
func TestDataServiceRecords(t *testing.T) { // D1, D2, D8, E1
	env := newTestServer(t)
	schema := dataServicesSchema(t)
	for _, tc := range []struct {
		ds, provider string
		ids          []string
	}{
		{"brp", "GBO demo ASIP (test)", []string{idAddress, idBirthDate, idFamilyName, idGivenName, idNationality}},
		{"duo", "DUO (test)", []string{idDiploma}},
	} {
		rec := get(t, env, "/"+tc.ds+"/dataservices")
		if rec.Code != http.StatusOK || rec.Header().Get("Content-Type") != "application/json" {
			t.Fatalf("GET /%s/dataservices = %d %s", tc.ds, rec.Code, rec.Header().Get("Content-Type"))
		}
		inst, err := jsonschema.UnmarshalJSON(bytes.NewReader(rec.Body.Bytes()))
		if err != nil {
			t.Fatal(err)
		}
		if err := schema.Validate(inst); err != nil {
			t.Fatalf("%s records do not conform to 19478-dataservice-schema.json: %v", tc.ds, err)
		}
		var got records
		decode(t, rec, &got)
		var ids []string
		for _, r := range got.DataServices {
			ids = append(ids, r.AttributeIdentifier)
			if r.EndpointURI != testPublicURL+"/"+tc.ds+BasePath || r.EndpointDescription != testPublicURL+"/"+tc.ds+"/openapi.json" ||
				r.Provider.LegalName != tc.provider || r.Country != "NL" || r.Provider.Identifiers != nil {
				t.Errorf("%s record = %+v", tc.ds, r)
			}
		}
		if !slices.Equal(ids, tc.ids) {
			t.Errorf("%s records for %v, want one per attribute %v", tc.ds, ids, tc.ids)
		}
	}
}

// The record's description is the contract whose server is the record's
// endpoint, and it names the authorization server's metadata (D3).
func TestARecordLeadsToItsContract(t *testing.T) {
	env := newTestServer(t)
	var got records
	decode(t, get(t, env, "/brp/dataservices"), &got)
	r := got.DataServices[0]
	path := r.EndpointDescription[len(testPublicURL):]
	var contract struct {
		Servers []struct {
			URL string `json:"url"`
		} `json:"servers"`
		Components struct {
			SecuritySchemes struct {
				OAuth2 map[string]any `json:"oauth2"`
			} `json:"securitySchemes"`
		} `json:"components"`
	}
	served := get(t, env, path)
	decode(t, served, &contract)
	loader := openapi3.NewLoader()
	loader.IsExternalRefsAllowed = true
	loader.ReadFromURIFunc = func(_ *openapi3.Loader, u *url.URL) ([]byte, error) { return openapiFiles.ReadFile(u.Path) }
	doc, err := loader.LoadFromDataWithPath(served.Body.Bytes(), &url.URL{Path: "openapi.json"})
	if err != nil || doc.Validate(context.Background()) != nil {
		t.Fatalf("the served contract is not a valid OpenAPI document: %v", err)
	}
	if len(contract.Servers) != 1 || contract.Servers[0].URL != r.EndpointURI {
		t.Errorf("contract servers = %+v, want the record's endpoint %s", contract.Servers, r.EndpointURI)
	}
	if got := contract.Components.SecuritySchemes.OAuth2["x-oauth-authorization-server"]; got != testIssuer+"/.well-known/oauth-authorization-server" {
		t.Errorf("x-oauth-authorization-server = %v", got)
	}
}

// The records follow the public URL, as the contracts do (D7), and a data
// service that does not exist has none (D6).
func TestRecordsFollowThePublicURL(t *testing.T) {
	if rec := get(t, newTestServer(t), "/nope/dataservices"); rec.Code != http.StatusNotFound {
		t.Errorf("GET /nope/dataservices = %d, want 404", rec.Code)
	}
	authz := newTestAuthorizationServer(t)
	public := "http://192.168.1.20:9424"
	belgian := described(func(asi *asimapping.Capability) {
		asi.AuthenticSource.Country = "BE"
		asi.Attributes = asi.Attributes[:1]
	})
	h, err := NewHandler(Config{PublicURL: public, Issuer: testIssuer, DataServices: []DataService{{
		ID: "brp", Provider: Provider{LegalName: "x"},
		Verification: verification.NewService(fakeSource{asi: belgian}, testCatalogue),
		Tokens:       accesstoken.NewValidator(testIssuer, public+"/brp"+BasePath, public, accesstoken.StaticKeys(authz.jwks)),
	}}})
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/brp/dataservices", nil))
	var got records
	decode(t, rec, &got)
	if len(got.DataServices) != 1 || got.DataServices[0].EndpointURI != public+"/brp/authsrc-api" || got.DataServices[0].Country != "BE" {
		t.Errorf("records = %+v", got.DataServices)
	}
}

// A new release with an attribute fewer or more changes the records of the
// next request, without a restart (E2).
func TestANewReleaseChangesTheRecords(t *testing.T) {
	source := &releases{fakeSource: testPersons}
	authz, qtsp := newTestAuthorizationServer(t), newTestQTSP(t)
	env := newTestServerWithSource(t, authz, qtsp, accesstoken.StaticKeys(authz.jwks), source)
	for _, tc := range []struct {
		name string
		asi  *asimapping.Capability
		want []string
	}{
		{"an attribute fewer", described(func(asi *asimapping.Capability) {
			asi.Attributes = slices.DeleteFunc(asi.Attributes, func(a asimapping.Attribute) bool { return a.Identifier == idNationality })
		}), []string{idAddress, idBirthDate, idFamilyName, idGivenName}},
		{"the attribute back", brpASI, []string{idAddress, idBirthDate, idFamilyName, idGivenName, idNationality}},
	} {
		source.asi = tc.asi
		var got records
		decode(t, get(t, env, "/brp/dataservices"), &got)
		var ids []string
		for _, r := range got.DataServices {
			ids = append(ids, r.AttributeIdentifier)
		}
		if !slices.Equal(ids, tc.want) {
			t.Errorf("%s: records for %v, want %v", tc.name, ids, tc.want)
		}
	}
}
