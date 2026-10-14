package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"testing"

	"gbo-demo/asi-provider/internal/accesstoken"
	"gbo-demo/asi-provider/internal/verification"
)

// fakeSource stands in for the BRP behind FSC: per BSN the attribute values
// as the source adapter maps them. With err set it fails every call.
type fakeSource struct {
	persons map[string]map[string]string
	err     error
}

func (f fakeSource) Value(_ context.Context, bsn, name string) (json.RawMessage, bool, error) {
	if f.err != nil {
		return nil, false, f.err
	}
	v, ok := f.persons[bsn][name]
	return json.RawMessage(v), ok, nil
}

const (
	// testBase and duoBase are the base paths of the test data services.
	testBase = "/brp" + BasePath
	duoBase  = "/duo" + BasePath
)

// brpCatalogue is the BRP data service's; the diploma is DUO's.
var brpCatalogue = verification.Catalogue{
	idFamilyName: {Name: "family_name"},
	idGivenName:  {Name: "given_name"},
	idAddress:    {Name: "address"},
}

var duoCatalogue = verification.Catalogue{idDiploma: {Name: "education_qualification"}}

var diplomas = fakeSource{persons: map[string]map[string]string{
	bsnFrouke: {"education_qualification": `{"qualification":"MSc"}`},
}}

// The persons of the BRP mock the tests use. Tom is not resident: he has no
// address in the attribute's shape.
var testPersons = fakeSource{persons: map[string]map[string]string{
	bsnFrouke: {
		"family_name": `{"family_name":"Jansen"}`,
		"given_name":  `{"given_name":"Frouke"}`,
		"birth_date":  `{"birth_date":"2000-03-24"}`,
		"nationality": `{"nationalities":["NL","BE"]}`,
		"address":     `{"street":"Coolsingel","house_number":"12 B","postal_code":"3011 AD","city":"Rotterdam","country":"NL"}`,
	},
	bsnSanne: {
		"family_name": `{"family_name":"Meijer"}`,
		"given_name":  `{"given_name":"Sanne"}`,
	},
	bsnTom: {
		"family_name": `{"family_name":"Willems"}`,
		"given_name":  `{"given_name":"Tom"}`,
		"nationality": `{"nationalities":["BE"]}`,
	},
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
			env := newTestServerWithSource(t, authz, qtsp, accesstoken.StaticKeys(authz.jwks), fakeSource{err: tc.err})
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
