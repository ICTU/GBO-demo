package graphqlsource

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/google/uuid"

	asimapping "gbo-demo/asi-mapping"
	"gbo-demo/asi-provider/internal/verification"
)

const (
	bsn          = "999991772"
	grantDir     = "/brp/graphql"
	idFamilyName = "https://gbo.example/attributes/family_name/1"
	idAddress    = "https://gbo.example/attributes/address/1"
)

// brpASI is the description the BRP mock publishes.
func brpASI(t *testing.T) *asimapping.Capability {
	t.Helper()
	raw, err := os.ReadFile("../../../brp-graphql-server/config/gbo-source-metadata.json")
	if err != nil {
		t.Fatal(err)
	}
	var document struct {
		Capabilities struct {
			ASI *asimapping.Capability `json:"asi"`
		} `json:"capabilities"`
	}
	if err := json.Unmarshal(raw, &document); err != nil || document.Capabilities.ASI == nil {
		t.Fatalf("the BRP publishes no asi: %v", err)
	}
	return document.Capabilities.ASI
}

// outway is a fake Outway: it records the last request and answers with
// status and body.
type outway struct {
	status   int
	body     string
	requests int
	got      *http.Request
	query    struct {
		Query     string            `json:"query"`
		Variables map[string]string `json:"variables"`
	}
}

func newOutway(t *testing.T, status int, body string) (*outway, *Source) {
	t.Helper()
	o := &outway{status: status, body: body}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		o.got = r
		o.requests++
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &o.query)
		w.WriteHeader(o.status)
		_, _ = io.WriteString(w, o.body)
	}))
	t.Cleanup(srv.Close)
	return o, newSource(t, srv.URL+"/")
}

func newSource(t *testing.T, outwayURL string) *Source {
	asi := brpASI(t)
	return New("rvig", outwayURL, grantDir, func(id string) (*asimapping.Capability, bool) { return asi, id == "rvig" })
}

// ask asks s for the attributes with ids, with the query the description
// derives.
func ask(t *testing.T, s *Source, ids ...string) (any, error) {
	t.Helper()
	asi, err := s.Description()
	if err != nil {
		t.Fatal(err)
	}
	var requests []asimapping.Request
	for _, id := range ids {
		requests = append(requests, asimapping.Request{Identifier: id})
	}
	document, err := asi.Plan(requests)
	if err != nil {
		t.Fatal(err)
	}
	return s.Ask(context.Background(), bsn, asi, document, ids)
}

const frouke = `{"data":{"ingeschrevenPersoon":{"__typename":"Ingezetene","geslachtsnaam":"Jansen","voorvoegsel":null}}}`

// The query goes through the grant-link, as the description derives it,
// with the BSN as its subject variable; Fsc-Transaction-Id is a UUID v7.
// The answer is the one at result_pointer.
func TestTheQueryThroughTheChain(t *testing.T) {
	o, s := newOutway(t, http.StatusOK, frouke)
	result, err := ask(t, s, idFamilyName, idAddress)
	if err != nil {
		t.Fatal(err)
	}
	if o.got.Method != http.MethodPost || o.got.URL.Path != grantDir {
		t.Fatalf("request = %s %s, want POST %s", o.got.Method, o.got.URL.Path, grantDir)
	}
	q := o.query.Query
	if !strings.Contains(q, "ingeschrevenPersoon(bsn: $bsn)") || !strings.Contains(q, "geslachtsnaam") ||
		!strings.Contains(q, "woontOp") || strings.Contains(q, "voornamen") || strings.Contains(q, bsn) {
		t.Errorf("query = %q, want the fields of the two attributes and the BSN as a variable", q)
	}
	if o.query.Variables["bsn"] != bsn || len(o.query.Variables) != 1 {
		t.Errorf("variables = %v", o.query.Variables)
	}
	if person, _ := result.(map[string]any); person["geslachtsnaam"] != "Jansen" {
		t.Errorf("result = %v", result)
	}
	tx := o.got.Header.Get("Fsc-Transaction-Id")
	id, err := uuid.Parse(tx)
	if err != nil || id.Version() != 7 {
		t.Fatalf("Fsc-Transaction-Id = %q, want a UUID v7", tx)
	}
}

// A person the source does not know: one request, no answer. An errors
// member that is null or an empty list reports no error.
func TestAnUnknownPerson(t *testing.T) {
	for name, body := range map[string]string{
		"no errors":         `{"data":{"ingeschrevenPersoon":null}}`,
		"G11g empty errors": `{"data":{"ingeschrevenPersoon":null},"errors":[]}`,
		"G11h null errors":  `{"data":{"ingeschrevenPersoon":null},"errors":null}`,
	} {
		t.Run(name, func(t *testing.T) {
			o, s := newOutway(t, http.StatusOK, body)
			result, err := ask(t, s, idFamilyName)
			if err != nil || result != nil || o.requests != 1 {
				t.Fatalf("Ask = %v, %v; %d requests", result, err, o.requests)
			}
		})
	}
}

// Without a current description the source is unavailable.
func TestWithoutADescription(t *testing.T) {
	s := New("rvig", "http://outway.invalid", grantDir, func(string) (*asimapping.Capability, bool) { return nil, false })
	if _, err := s.Description(); !errors.Is(err, verification.ErrSourceUnavailable) {
		t.Fatalf("Description() = %v, want ErrSourceUnavailable", err)
	}
}

// G11: a chain that cannot be reached is unavailable (503); one that refuses
// or answers what the ASIP cannot read is not (500). No error names the BSN.
func TestFailures(t *testing.T) {
	down := httptest.NewServer(http.NotFoundHandler())
	down.Close()
	for _, tc := range []struct {
		name        string
		source      func(t *testing.T) *Source
		unavailable bool
	}{
		{"unreachable", func(t *testing.T) *Source { return newSource(t, down.URL) }, true},
		{"outway 502", outwaySource(http.StatusBadGateway, `{"code":"ERROR_CODE_SERVICE_UNREACHABLE"}`), true},
		{"PDP denies", outwaySource(http.StatusUnauthorized, `{"message":"authorization server denied request: ","source":"inway","code":"UNAUTHORIZED"}`), false},
		{"GraphQL errors", outwaySource(http.StatusOK, `{"errors":[{"message":"bsn 999991772 unknown"}]}`), false},
		{"G11e errors of another shape: an object", outwaySource(http.StatusOK, `{"data":{"ingeschrevenPersoon":null},"errors":{"message":"failed"}}`), false},
		{"G11f errors of another shape: a string", outwaySource(http.StatusOK, `{"data":{"ingeschrevenPersoon":null},"errors":"failed"}`), false},
		{"not JSON", outwaySource(http.StatusOK, `<html>`), false},
		{"data of another shape", outwaySource(http.StatusOK, `{"data":"unexpected"}`), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ask(t, tc.source(t), idFamilyName, idAddress)
			if err == nil {
				t.Fatal("err = nil")
			}
			if errors.Is(err, verification.ErrSourceUnavailable) != tc.unavailable {
				t.Errorf("err = %v, unavailable = %v", err, tc.unavailable)
			}
			if strings.Contains(err.Error(), bsn) {
				t.Errorf("err names the BSN: %v", err)
			}
		})
	}
}

func outwaySource(status int, body string) func(t *testing.T) *Source {
	return func(t *testing.T) *Source {
		_, s := newOutway(t, status, body)
		return s
	}
}
