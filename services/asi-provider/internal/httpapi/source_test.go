package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	asimapping "gbo-demo/asi-mapping"
	"gbo-demo/asi-provider/internal/accesstoken"
	"gbo-demo/asi-provider/internal/verification"
)

// fakeSource stands in for an authentic source behind FSC, known by its
// description: per BSN the answer at result_pointer, as the source gives it.
// With err set it fails every question.
type fakeSource struct {
	asi     *asimapping.Capability
	persons map[string]string
	err     error
}

func (f fakeSource) Description() (*asimapping.Capability, error) { return f.asi, nil }

func (f fakeSource) Ask(_ context.Context, bsn string, _ *asimapping.Capability, _ string, _ []string) (any, error) {
	if f.err != nil {
		return nil, f.err
	}
	raw, ok := f.persons[bsn]
	if !ok {
		return nil, nil
	}
	var answer any
	if err := json.Unmarshal([]byte(raw), &answer); err != nil {
		return nil, err
	}
	return answer, nil
}

const (
	// testBase and duoBase are the base paths of the test data services.
	testBase = "/brp" + BasePath
	duoBase  = "/duo" + BasePath
)

// brpASI is the description the BRP mock publishes.
var brpASI = func() *asimapping.Capability {
	raw, err := os.ReadFile("../../../brp-graphql-server/config/gbo-source-metadata.json")
	if err != nil {
		panic(err)
	}
	var document struct {
		Capabilities struct {
			ASI *asimapping.Capability `json:"asi"`
		} `json:"capabilities"`
	}
	if err := json.Unmarshal(raw, &document); err != nil {
		panic(err)
	}
	return document.Capabilities.ASI
}()

// duoASI describes a diploma register: another authentic source, with its
// own data service.
var duoASI = &asimapping.Capability{
	GraphQL: asimapping.GraphQL{
		Document:        "query Asi($bsn: BSN!) { diploma(bsn: $bsn) { ...Selected } }",
		SubjectVariable: "bsn", ResultPointer: "/data/diploma",
	},
	Attributes: []asimapping.Attribute{{
		Identifier: idDiploma,
		Fields:     map[string]asimapping.Field{"qualification": {Pointer: "/kwalificatie"}},
	}},
}

// testCatalogue is GBO's attribute catalogue, with the diploma.
var testCatalogue = func() asimapping.Catalogue {
	catalogue, err := asimapping.LoadCatalogue("../../../../schemas/asi-attributes.json")
	if err != nil {
		panic(err)
	}
	catalogue[idDiploma] = &asimapping.ValueSchema{
		Type: "object", Required: []string{"qualification"},
		Properties: map[string]*asimapping.ValueSchema{"qualification": {Type: "string"}},
	}
	return catalogue
}()

var diplomas = fakeSource{asi: duoASI, persons: map[string]string{bsnFrouke: `{"kwalificatie":"MSc"}`}}

// The persons of the BRP mock the tests use, as the BRP answers. Tom is not
// resident: he has no address in the attribute's shape.
var testPersons = fakeSource{asi: brpASI, persons: map[string]string{
	bsnFrouke: `{"__typename":"Ingezetene","voorvoegsel":null,"geslachtsnaam":"Jansen","voornamen":"Frouke",
		"geboortedatum":"2000-03-24","heeftNationaliteit":[{"nationaliteit":"NL"},{"nationaliteit":"BE"}],
		"woontOp":{"straatnaam":"Coolsingel","huisnummer":12,"huisletter":"B","huisnummertoevoeging":null,
			"postcode":"3011 AD","woonplaatsnaam":"Rotterdam"}}`,
	bsnSanne: `{"__typename":"Ingezetene","voorvoegsel":null,"geslachtsnaam":"Meijer","voornamen":"Sanne"}`,
	bsnTom:   `{"__typename":"NietIngezetene","voorvoegsel":null,"geslachtsnaam":"Willems","voornamen":"Tom","heeftNationaliteit":[{"nationaliteit":"BE"}]}`,
}}

// A source that cannot be reached is 503, one that refuses 500: neither is
// Unknown, which says the source holds no data (G10).
func TestSourceFailureIsNotUnknown(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		want int
	}{
		{"unavailable", verification.ErrSourceUnavailable, http.StatusServiceUnavailable},
		{"refused", errors.New("refused by policy"), http.StatusInternalServerError},
	} {
		t.Run(tc.name, func(t *testing.T) {
			authz, qtsp := newTestAuthorizationServer(t), newTestQTSP(t)
			env := newTestServerWithSource(t, authz, qtsp, accesstoken.StaticKeys(authz.jwks), fakeSource{asi: brpASI, err: tc.err})
			for _, op := range []struct{ path, body string }{
				{"/verify", verifyBody(idFamilyName, `{"family_name":"Jansen"}`)},
				{"/retrieve", `{"attributeIdentifiers":[{"attributeIdentifier":"` + idFamilyName + `"}]}`},
			} {
				rec := post(t, env, op.path, env.person(bsnFrouke), op.body)
				assertProblem(t, rec, tc.want)
			}
		})
	}
}

// A data service whose source is unavailable, also for want of a current
// description in the Source Registry, still publishes its description and
// discovery endpoints: only /verify and /retrieve ask the source.
func TestAnUnavailableSourceLeavesThePublishedEndpoints(t *testing.T) {
	authz, qtsp := newTestAuthorizationServer(t), newTestQTSP(t)
	env := newTestServerWithSource(t, authz, qtsp, accesstoken.StaticKeys(authz.jwks), fakeSource{asi: brpASI, err: verification.ErrSourceUnavailable})
	for _, path := range []string{"/brp/openapi.json", "/brp/dataservices", "/brp/" + dataserviceFileName, "/healthz"} {
		rec := httptest.NewRecorder()
		env.handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		if rec.Code != http.StatusOK {
			t.Errorf("GET %s = %d, want 200", path, rec.Code)
		}
	}
}
