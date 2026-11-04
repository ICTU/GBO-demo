package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"testing"

	asimapping "gbo-demo/asi-mapping"
	"gbo-demo/asi-provider/internal/accesstoken"
	"gbo-demo/asi-provider/internal/verification"
)

// fakeSource stands in for an authentic source behind FSC, known by its
// description: per BSN the answer at result_pointer, as the source gives it.
// Without a description it is unavailable, as without a current one in the
// Source Registry. With err set it fails every question.
type fakeSource struct {
	asi     *asimapping.Capability
	persons map[string]string
	err     error
}

func (f fakeSource) Description() (*asimapping.Capability, error) {
	if f.asi == nil {
		return nil, verification.ErrSourceUnavailable
	}
	return f.asi, nil
}

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
	AuthenticSource: asimapping.AuthenticSource{LegalName: "Diplomaregister (test)", Country: "NL"},
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

// releases is a source whose description the Source Registry replaces with
// each new release. With next set, the release changes while the source is
// asked: between the query and the response.
type releases struct {
	fakeSource
	next *asimapping.Capability
}

func (r *releases) Description() (*asimapping.Capability, error) { return r.fakeSource.Description() }

func (r *releases) Ask(ctx context.Context, bsn string, asi *asimapping.Capability, document string, ids []string) (any, error) {
	if r.next != nil {
		r.asi, r.next = r.next, nil
	}
	return r.fakeSource.Ask(ctx, bsn, asi, document, ids)
}

// described returns a copy of the BRP's description changed by change: the
// description in a new release.
func described(change func(*asimapping.Capability)) *asimapping.Capability {
	asi := *brpASI
	asi.Attributes = slices.Clone(brpASI.Attributes)
	change(&asi)
	return &asi
}

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

// A data service whose source cannot be reached still publishes its contract
// and discovery records: only /verify and /retrieve ask the source. Without
// a current description it publishes its contract, but no discovery records
// (E3): the ASIP cannot say which attributes the source holds.
func TestAnUnavailableSourceLeavesThePublishedEndpoints(t *testing.T) {
	for _, tc := range []struct {
		name    string
		source  fakeSource
		records int
	}{
		{"unreachable", fakeSource{asi: brpASI, err: verification.ErrSourceUnavailable}, http.StatusOK},
		{"E3 no current description", fakeSource{}, http.StatusServiceUnavailable},
	} {
		t.Run(tc.name, func(t *testing.T) {
			authz, qtsp := newTestAuthorizationServer(t), newTestQTSP(t)
			env := newTestServerWithSource(t, authz, qtsp, accesstoken.StaticKeys(authz.jwks), tc.source)
			for path, want := range map[string]int{
				"/brp/openapi.json": http.StatusOK, "/brp/" + dataserviceFileName: http.StatusOK, "/healthz": http.StatusOK,
				"/brp/dataservices": tc.records,
			} {
				rec := httptest.NewRecorder()
				env.handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
				if rec.Code != want {
					t.Errorf("GET %s = %d, want %d", path, rec.Code, want)
				}
			}
		})
	}
}
