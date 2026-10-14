package brpsource

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"

	"gbo-demo/asi-provider/internal/verification"
)

const (
	bsn      = "999991772"
	grantDir = "/brp/graphql"
)

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
	return o, New(srv.URL+"/", grantDir)
}

func brpPerson(fields string) string {
	return `{"data":{"ingeschrevenPersoon":{` + fields + `}}}`
}

const frouke = `"geslachtsnaam":"Jansen","voorvoegsel":null,"voornamen":"Frouke","geboortedatum":"2000-03-24",` +
	`"heeftNationaliteit":[{"nationaliteit":"NL"},{"nationaliteit":"BE"}],` +
	`"woontOp":{"straatnaam":"Coolsingel","huisnummer":12,"huisletter":"B","huisnummertoevoeging":null,"postcode":"3011 AD","woonplaatsnaam":"Rotterdam"}`

func value(t *testing.T, s *Source, name string) (string, bool) {
	t.Helper()
	values, err := s.Values(context.Background(), bsn, []string{name})
	if err != nil {
		t.Fatalf("Values(%s): %v", name, err)
	}
	raw, ok := values[name]
	return string(raw), ok
}

// The BRP fields become the attribute's JSON object (G1, G4, G6).
func TestMapsTheBRPFields(t *testing.T) {
	_, s := newOutway(t, http.StatusOK, brpPerson(frouke))
	for name, want := range map[string]string{
		"family_name": `{"family_name":"Jansen"}`,
		"given_name":  `{"given_name":"Frouke"}`,
		"birth_date":  `{"birth_date":"2000-03-24"}`,
		"nationality": `{"nationalities":["NL","BE"]}`,
		"address":     `{"city":"Rotterdam","country":"NL","house_number":"12 B","postal_code":"3011 AD","street":"Coolsingel"}`,
	} {
		if got, ok := value(t, s, name); !ok || got != want {
			t.Errorf("%s = %s, %v; want %s", name, got, ok, want)
		}
	}
}

// The prefix goes before the surname; letter and addition after the number,
// each separated by a space (G7).
func TestJoinsPrefixAndHouseNumber(t *testing.T) {
	_, s := newOutway(t, http.StatusOK, brpPerson(`"geslachtsnaam":"Dijk","voorvoegsel":"van",`+
		`"woontOp":{"straatnaam":"Oudegracht","huisnummer":233,"huisletter":"","huisnummertoevoeging":"bis","postcode":"3511 NK","woonplaatsnaam":"Utrecht"}`))
	if got, _ := value(t, s, "family_name"); got != `{"family_name":"van Dijk"}` {
		t.Errorf("family_name = %s", got)
	}
	if got, _ := value(t, s, "address"); !strings.Contains(got, `"house_number":"233 bis"`) {
		t.Errorf("address = %s", got)
	}
}

// No value: a person the BRP does not know (G8), a non-resident without a
// Dutch address (G5), fields the BRP leaves empty.
func TestNoValue(t *testing.T) {
	for _, tc := range []struct{ name, body, attribute string }{
		{"unknown person", `{"data":{"ingeschrevenPersoon":null}}`, "family_name"},
		{"non-resident address", brpPerson(`"geslachtsnaam":"Willems"`), "address"},
		{"address without postcode", brpPerson(`"woontOp":{"straatnaam":"Coolsingel","huisnummer":12,"postcode":null,"woonplaatsnaam":"Rotterdam"}`), "address"},
		{"address without street", brpPerson(`"woontOp":{"straatnaam":"","huisnummer":12,"postcode":"3011 AD","woonplaatsnaam":"Rotterdam"}`), "address"},
		{"address without number", brpPerson(`"woontOp":{"straatnaam":"Coolsingel","postcode":"3011 AD","woonplaatsnaam":"Rotterdam"}`), "address"},
		{"address without city", brpPerson(`"woontOp":{"straatnaam":"Coolsingel","huisnummer":12,"postcode":"3011 AD"}`), "address"},
		{"no given names", brpPerson(`"voornamen":null`), "given_name"},
		{"no birth date", brpPerson(`"geboortedatum":null`), "birth_date"},
		{"no nationality", brpPerson(`"heeftNationaliteit":[]`), "nationality"},
		{"no surname", brpPerson(`"voorvoegsel":"van"`), "family_name"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, s := newOutway(t, http.StatusOK, tc.body)
			if got, ok := value(t, s, tc.attribute); ok {
				t.Fatalf("%s = %s, want no value", tc.attribute, got)
			}
		})
	}
}

// The query goes through the grant-link, with only the attribute's fields
// and the BSN as a variable; Fsc-Transaction-Id is a UUID v7 (G12).
func TestTheQueryThroughTheChain(t *testing.T) {
	o, s := newOutway(t, http.StatusOK, brpPerson(frouke))
	value(t, s, "family_name")
	if o.got.Method != http.MethodPost || o.got.URL.Path != grantDir {
		t.Fatalf("request = %s %s, want POST %s", o.got.Method, o.got.URL.Path, grantDir)
	}
	q := o.query.Query
	if !strings.Contains(q, "ingeschrevenPersoon(bsn: $bsn) { voorvoegsel geslachtsnaam }") || strings.Contains(q, bsn) {
		t.Errorf("query = %q, want only the name fields and the BSN as a variable", q)
	}
	if o.query.Variables["bsn"] != bsn {
		t.Errorf("variables = %v", o.query.Variables)
	}
	value(t, s, "address")
	if q := o.query.Query; !strings.Contains(q, "... on Ingezetene { woontOp {") || strings.Contains(q, "geslachtsnaam") {
		t.Errorf("address query = %q", q)
	}
	tx := o.got.Header.Get("Fsc-Transaction-Id")
	id, err := uuid.Parse(tx)
	if err != nil || id.Version() != 7 {
		t.Fatalf("Fsc-Transaction-Id = %q, want a UUID v7", tx)
	}
}

// A chain that cannot be reached is unavailable; one that refuses or answers
// errors is not. No error names the BSN (G10, G13).
func TestFailures(t *testing.T) {
	down := httptest.NewServer(http.NotFoundHandler())
	down.Close()
	for _, tc := range []struct {
		name        string
		source      func(t *testing.T) *Source
		unavailable bool
	}{
		{"unreachable", func(*testing.T) *Source { return New(down.URL, grantDir) }, true},
		{"outway 502", outwaySource(http.StatusBadGateway, `{"code":"ERROR_CODE_SERVICE_UNREACHABLE"}`), true},
		{"PDP denies", outwaySource(http.StatusUnauthorized, `{"message":"authorization server denied request: ","source":"inway","code":"UNAUTHORIZED"}`), false},
		{"GraphQL errors", outwaySource(http.StatusOK, `{"errors":[{"message":"bsn 999991772 unknown"}]}`), false},
		{"not JSON", outwaySource(http.StatusOK, `<html>`), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := tc.source(t).Values(context.Background(), bsn, []string{"family_name", "address"})
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

// An attribute without a query is a fault, not a missing value; the chain is
// not asked.
func TestAttributeWithoutAQuery(t *testing.T) {
	o, s := newOutway(t, http.StatusOK, brpPerson(frouke))
	if _, err := s.Values(context.Background(), bsn, []string{"family_name", "education_qualification"}); err == nil {
		t.Fatal("err = nil")
	}
	if o.got != nil {
		t.Fatal("the chain was asked")
	}
}

// Several attributes: one request, with the fields of each once; a person
// without a resident's address has the other values (Q1, Q2, Q8).
func TestOneQueryForSeveralAttributes(t *testing.T) {
	o, s := newOutway(t, http.StatusOK, brpPerson(`"geslachtsnaam":"Willems","voorvoegsel":null,"voornamen":"Tom"`))
	values, err := s.Values(context.Background(), bsn, []string{"family_name", "given_name", "address", "family_name"})
	if err != nil || o.requests != 1 {
		t.Fatalf("Values = %v; %d requests, want one", err, o.requests)
	}
	q := o.query.Query
	if !strings.Contains(q, "{ voorvoegsel geslachtsnaam voornamen ... on Ingezetene { woontOp {") || strings.Count(q, "geslachtsnaam") != 1 ||
		strings.Contains(q, "heeftNationaliteit") || strings.Contains(q, bsn) {
		t.Errorf("query = %q, want the fields of the three attributes once and the BSN as a variable", q)
	}
	if string(values["family_name"]) != `{"family_name":"Willems"}` || string(values["given_name"]) != `{"given_name":"Tom"}` {
		t.Errorf("values = %s", values)
	}
	if _, ok := values["address"]; ok {
		t.Errorf("a non-resident has an address value: %s", values["address"])
	}
}

// A person the BRP does not know: one request, no values (Q3).
func TestAnUnknownPersonHasNoValues(t *testing.T) {
	o, s := newOutway(t, http.StatusOK, `{"data":{"ingeschrevenPersoon":null}}`)
	values, err := s.Values(context.Background(), bsn, []string{"family_name", "address"})
	if err != nil || len(values) != 0 || o.requests != 1 {
		t.Fatalf("Values = %v, %v; %d requests", values, err, o.requests)
	}
}
